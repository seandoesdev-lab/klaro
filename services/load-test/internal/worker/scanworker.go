package worker

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/scanner"
	"github.com/klaro/load-test/internal/store"
)

// ScanDeps are the collaborators the scan worker needs. Semgrep/OSV/ZAP are the
// real engines; WorkDir/SrcDir are the tmpfs roots for [EPHEM-01].
type ScanDeps struct {
	Queue queue.ScanQueue
	Store *store.Store

	// HTTPClient is used for the DAST header fetch; defaults to a 10s client.
	HTTPClient *http.Client

	// Real engines. Injected by cmd/worker; nil-safe defaults are constructed for
	// header-only DAST so unit paths keep working.
	Semgrep scanner.SASTScanner
	OSV     scanner.SASTScanner
	ZAP     scanner.DASTScanner

	WorkDir        string // tmpfs, e.g. /scan-work
	SrcDir         string // shared tmpfs staging, e.g. /scan-src
	MaxConcurrency int    // SCAN_MAX_CONCURRENCY (>=1)

	// zapMu serializes access to the single ZAP daemon (design #4).
	zapMu sync.Mutex
}

func (d *ScanDeps) client() *http.Client {
	if d.HTTPClient != nil {
		return d.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// RunScan consumes scan jobs until ctx is cancelled, dispatching each to a bounded
// worker pool (design #4 semaphore). Intended to run as its own goroutine.
func RunScan(ctx context.Context, d *ScanDeps) {
	max := d.MaxConcurrency
	if max < 1 {
		max = 1
	}
	sem := make(chan struct{}, max)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		job, err := d.Queue.DequeueScan(ctx)
		if err != nil {
			continue // timeout or transient; loop again
		}
		sem <- struct{}{}
		go func(job model.ScanJob) {
			defer func() { <-sem }()
			if err := d.processScan(ctx, job); err != nil {
				log.Printf("scan %s failed: %v", job.ScanID, err)
			}
		}(job)
	}
}

func (d *ScanDeps) processScan(ctx context.Context, job model.ScanJob) (err error) {
	id := job.ScanID
	// org 스코프 실행 헬퍼(Phase 1 패턴): 각 DB write 를 RLS 관통 org tx 로 감싼다.
	setStatus := func(to model.ScanStatus, score *int) error {
		return d.Store.RunInOrg(ctx, job.OrgID, func(tx pgx.Tx) error {
			return d.Store.UpdateScanStatus(ctx, tx, id, to, score)
		})
	}
	// panic 안전망: 소스 cleanup 은 각 case 에서 defer 로 이미 등록되므로 panic 시에도 실행됨.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("scan %s panicked: %v", id, r)
			_ = setStatus(model.ScanStatusFailed, nil)
			err = nil
		}
	}()

	if e := setStatus(model.ScanStatusRunning, nil); e != nil {
		return e
	}

	var findings []model.ScanFinding
	switch job.Type {
	case model.ScanTypeSAST:
		findings, err = d.runSAST(ctx, job)
	case model.ScanTypeDAST:
		findings, err = d.runDAST(ctx, job)
	default:
		_ = setStatus(model.ScanStatusFailed, nil)
		return nil
	}
	if err != nil {
		_ = setStatus(model.ScanStatusFailed, nil)
		return err
	}

	for i := range findings {
		findings[i].ScanID = id
	}

	// #7/#8 재스캔 triage: 이전 completed 스캔 대비 ignored 승계 + fixed 판정.
	findings, err = d.applyTriage(ctx, job, findings)
	if err != nil {
		_ = setStatus(model.ScanStatusFailed, nil)
		return err
	}

	if err = d.Store.RunInOrg(ctx, job.OrgID, func(tx pgx.Tx) error {
		return d.Store.SaveFindings(ctx, tx, findings)
	}); err != nil {
		_ = setStatus(model.ScanStatusFailed, nil)
		return err
	}
	score := model.ComputeScore(findings)
	return setStatus(model.ScanStatusCompleted, &score)
}

// runSAST acquires the Ephemeral source tree and runs Semgrep + osv in parallel,
// each under its own timeout. cleanup() is deferred immediately after Acquire so
// the source is destroyed on every exit path ([EPHEM-01]).
func (d *ScanDeps) runSAST(ctx context.Context, job model.ScanJob) ([]model.ScanFinding, error) {
	src := scanner.Source{
		Type:    job.Source.Type,
		RepoURL: job.Source.RepoURL,
		Ref:     job.Source.Ref,
		Token:   job.Source.Token,
	}
	acqCtx, cancelAcq := context.WithTimeout(ctx, scanner.CloneTimeoutSec*time.Second)
	dir, cleanup, err := scanner.Acquire(acqCtx, d.WorkDir, d.SrcDir, src)
	cancelAcq()
	if err != nil {
		return nil, err
	}
	defer cleanup() // ★ [EPHEM-01] 소스 소멸 보장 (성공/에러/타임아웃/panic)

	type res struct {
		fs  []model.ScanFinding
		err error
	}
	sgCh := make(chan res, 1)
	ovCh := make(chan res, 1)

	go func() {
		if d.Semgrep == nil {
			sgCh <- res{}
			return
		}
		c, cancel := context.WithTimeout(ctx, scanner.SemgrepTimeoutSec*time.Second)
		defer cancel()
		fs, e := d.Semgrep.Scan(c, dir)
		sgCh <- res{fs, e}
	}()
	go func() {
		if d.OSV == nil {
			ovCh <- res{}
			return
		}
		c, cancel := context.WithTimeout(ctx, scanner.OSVTimeoutSec*time.Second)
		defer cancel()
		fs, e := d.OSV.Scan(c, dir)
		ovCh <- res{fs, e}
	}()

	sg := <-sgCh
	ov := <-ovCh
	if sg.err != nil {
		return nil, sg.err
	}
	if ov.err != nil {
		return nil, ov.err
	}
	return append(sg.fs, ov.fs...), nil
}

// runDAST runs the offline header analysis plus (if a ZAP engine is configured)
// an OWASP ZAP baseline/active scan, deduping overlapping topics (design #10).
// ZAP is serialized via zapMu since a single daemon is shared.
func (d *ScanDeps) runDAST(ctx context.Context, job model.ScanJob) ([]model.ScanFinding, error) {
	header, err := d.analyzeHeaders(ctx, job.TargetURL)
	if err != nil {
		return nil, err
	}
	if d.ZAP == nil {
		return header, nil // header-only DAST (no ZAP configured)
	}
	mode := scanner.ParseMode(job.Mode)
	timeout := scanner.ZAPBaselineTimeoutSec
	if mode == scanner.ModeActive {
		timeout = scanner.ZAPActiveTimeoutSec
	}
	zctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	d.zapMu.Lock()
	zap, zerr := d.ZAP.Scan(zctx, job.TargetURL, mode)
	d.zapMu.Unlock()
	if zerr != nil {
		return nil, zerr
	}
	return scanner.DedupDAST(header, zap), nil
}

// analyzeHeaders performs the single network fetch and delegates to the pure
// scanner.AnalyzeHeaders. Kept as a method so DAST stays testable without network.
func (d *ScanDeps) analyzeHeaders(ctx context.Context, targetURL string) ([]model.ScanFinding, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return scanner.AnalyzeHeaders(resp.Header, targetURL), nil
}

// applyTriage carries forward triage state across re-scans (#8):
//   - a current finding whose hash was previously `ignored` inherits that status/reason
//   - a previous-scan finding whose hash is absent this time is appended as `fixed`
//
// The lookup runs in an org tx so RLS scopes it to the tenant.
func (d *ScanDeps) applyTriage(ctx context.Context, job model.ScanJob, current []model.ScanFinding) ([]model.ScanFinding, error) {
	var prev []model.ScanFinding
	err := d.Store.RunInOrg(ctx, job.OrgID, func(tx pgx.Tx) error {
		var e error
		prev, e = d.Store.GetLatestCompletedScanFindings(ctx, tx, job.ProjectID, job.Type, job.ScanID)
		return e
	})
	if err != nil {
		return nil, err
	}
	if len(prev) == 0 {
		return current, nil
	}

	prevByHash := make(map[string]model.ScanFinding, len(prev))
	for _, p := range prev {
		prevByHash[p.FindingHash] = p
	}
	curHashes := make(map[string]bool, len(current))
	for i := range current {
		curHashes[current[i].FindingHash] = true
		if p, ok := prevByHash[current[i].FindingHash]; ok && p.Status == model.FindingIgnored {
			current[i].Status = model.FindingIgnored
			current[i].IgnoreReason = p.IgnoreReason
		}
	}
	// previous findings absent this run → fixed rows on the current scan.
	for _, p := range prev {
		if p.Status == model.FindingFixed {
			continue // already resolved; don't re-carry
		}
		if !curHashes[p.FindingHash] {
			p.ID = ""
			p.ScanID = job.ScanID
			p.Status = model.FindingFixed
			p.IgnoreReason = nil
			current = append(current, p)
		}
	}
	return current, nil
}
