//go:build integration

package worker

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/store"
)

// These integration tests exercise the scan pipeline's Ephemeral guarantees and
// re-scan triage against a real Postgres. They use nil/fake scanners so no
// Semgrep/osv/ZAP binaries or network are required. Run:
//
//	TEST_DATABASE_URL=postgres://klaro_app:klaro_app@localhost:5432/klaro?sslmode=disable \
//	TEST_SYSTEM_DATABASE_URL=postgres://klaro_system:klaro_system@localhost:5432/klaro?sslmode=disable \
//	SCAN_WORK_TMPFS_ASSERT=0 go test -tags integration ./internal/worker/ -run Scan -v

const (
	devOrg     = "00000000-0000-0000-0000-000000000001"
	devProject = "00000000-0000-0000-0000-000000000002"
)

func testStore(t *testing.T) *store.Store {
	appDSN := os.Getenv("TEST_DATABASE_URL")
	if appDSN == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	s, err := store.New(context.Background(), appDSN, os.Getenv("TEST_SYSTEM_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// stageZip writes a .zip staging archive for token under dir with the given files.
func stageZip(t *testing.T, dir, token string, files map[string]string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, token+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// createScanRow inserts a pending scan and returns its id.
func createScanRow(t *testing.T, s *store.Store, sc *model.Scan) string {
	t.Helper()
	err := s.RunInOrg(context.Background(), devOrg, func(tx pgx.Tx) error {
		return s.CreateScan(context.Background(), tx, sc)
	})
	if err != nil {
		t.Fatal(err)
	}
	return sc.ID
}

func jobDirCount(t *testing.T, workDir string) int {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "job-") {
			n++
		}
	}
	return n
}

// TestEphemeralCleanup: after a SAST scan (success and forced failure) the per-job
// tmpfs work directory must be gone ([EPHEM-01] cleanup guarantee).
func TestEphemeralCleanup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	work := t.TempDir()
	src := t.TempDir()

	// success path (nil scanners → 0 findings, but Acquire+cleanup still exercised)
	st := "upload"
	tok := "tokok"
	sc := &model.Scan{ProjectID: devProject, Type: model.ScanTypeSAST, Status: model.ScanStatusPending,
		Trigger: model.ScanTriggerManual, SourceType: &st, SourceRef: &tok}
	id := createScanRow(t, s, sc)
	stageZip(t, src, tok, map[string]string{"main.go": "package main\n"})

	d := &ScanDeps{Store: s, WorkDir: work, SrcDir: src}
	job := model.ScanJob{ScanID: id, OrgID: devOrg, ProjectID: devProject,
		Type: model.ScanTypeSAST, Source: model.ScanSource{Type: "upload", Token: tok}}
	if err := d.processScan(ctx, job); err != nil {
		t.Fatalf("processScan: %v", err)
	}
	if n := jobDirCount(t, work); n != 0 {
		t.Errorf("after success: %d job dirs remain, want 0", n)
	}

	// failure path: missing staging → Acquire fails → status failed, no orphan dir
	sc2 := &model.Scan{ProjectID: devProject, Type: model.ScanTypeSAST, Status: model.ScanStatusPending,
		Trigger: model.ScanTriggerManual, SourceType: &st, SourceRef: &tok}
	id2 := createScanRow(t, s, sc2)
	job2 := model.ScanJob{ScanID: id2, OrgID: devOrg, ProjectID: devProject,
		Type: model.ScanTypeSAST, Source: model.ScanSource{Type: "upload", Token: "missing"}}
	_ = d.processScan(ctx, job2) // expected error
	if n := jobDirCount(t, work); n != 0 {
		t.Errorf("after failure: %d job dirs remain, want 0", n)
	}
	var status string
	_ = s.RunInOrg(ctx, devOrg, func(tx pgx.Tx) error {
		sc, e := s.GetScan(ctx, tx, id2)
		if e == nil {
			status = string(sc.Status)
		}
		return e
	})
	if status != "failed" {
		t.Errorf("failed scan status = %q want failed", status)
	}
}

// TestNoSourceInDB: a canary string placed in the source content must never appear
// in any scans/scan_findings text column ([EPHEM-01] no source persistence).
func TestNoSourceInDB(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	work := t.TempDir()
	src := t.TempDir()
	const canary = "CANARY_SECRET_zzz_DO_NOT_PERSIST_9182"

	st := "upload"
	tok := "canarytok"
	sc := &model.Scan{ProjectID: devProject, Type: model.ScanTypeSAST, Status: model.ScanStatusPending,
		Trigger: model.ScanTriggerManual, SourceType: &st, SourceRef: &tok}
	id := createScanRow(t, s, sc)
	stageZip(t, src, tok, map[string]string{"secret.go": "package x\nconst k = \"" + canary + "\"\n"})

	// fakeSAST returns a finding referencing only the path (no source body echoed),
	// modeling normalized engine output.
	d := &ScanDeps{Store: s, WorkDir: work, SrcDir: src, Semgrep: fakeSAST{}}
	job := model.ScanJob{ScanID: id, OrgID: devOrg, ProjectID: devProject,
		Type: model.ScanTypeSAST, Source: model.ScanSource{Type: "upload", Token: tok}}
	if err := d.processScan(ctx, job); err != nil {
		t.Fatalf("processScan: %v", err)
	}

	// scan across every text/jsonb column for the canary (BYPASSRLS sys pool).
	var hits int
	err := s.Sys().QueryRow(ctx, `
	  SELECT
	    (SELECT count(*) FROM scans WHERE
	       coalesce(target_url,'')||coalesce(source_type,'')||coalesce(source_ref,'')||coalesce(mode,'') LIKE '%'||$1||'%')
	  + (SELECT count(*) FROM scan_findings WHERE
	       coalesce(rule_id,'')||coalesce(title,'')||coalesce(file_path,'')||coalesce(finding_hash,'')
	       ||coalesce(ignore_reason,'')||coalesce(cwe,'')||coalesce(confidence,'')||coalesce(package,'')
	       ||coalesce(package_version,'')||coalesce(evidence::text,'') LIKE '%'||$1||'%')
	`, canary).Scan(&hits)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Errorf("canary source content leaked into DB in %d rows — [EPHEM-01] violated", hits)
	}
	// and the work dir is gone
	if n := jobDirCount(t, work); n != 0 {
		t.Errorf("%d job dirs remain, want 0", n)
	}
}

// TestRescanTriage: re-scanning carries forward an ignored finding and marks a
// disappeared finding as fixed ([SC-04] / design #8).
func TestRescanTriage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	work := t.TempDir()
	src := t.TempDir()

	hashA := model.FindingHashParts("sast.semgrep.ruleA", "a.go", "code a")
	hashB := model.FindingHashParts("sast.semgrep.ruleB", "b.go", "code b")
	hashC := model.FindingHashParts("sast.semgrep.ruleC", "c.go", "code c")

	run := func(findings []model.ScanFinding) string {
		st := "upload"
		tok := "t" + model.FindingHashParts(findings[0].RuleID)[:8]
		sc := &model.Scan{ProjectID: devProject, Type: model.ScanTypeSAST, Status: model.ScanStatusPending,
			Trigger: model.ScanTriggerManual, SourceType: &st, SourceRef: &tok}
		id := createScanRow(t, s, sc)
		stageZip(t, src, tok, map[string]string{"main.go": "package main\n"})
		d := &ScanDeps{Store: s, WorkDir: work, SrcDir: src, Semgrep: fakeSAST{out: findings}}
		job := model.ScanJob{ScanID: id, OrgID: devOrg, ProjectID: devProject,
			Type: model.ScanTypeSAST, Source: model.ScanSource{Type: "upload", Token: tok}}
		if err := d.processScan(ctx, job); err != nil {
			t.Fatalf("processScan: %v", err)
		}
		return id
	}

	mk := func(rule, hash string) model.ScanFinding {
		return model.ScanFinding{RuleID: rule, Severity: model.SeverityHigh, Title: rule,
			FindingHash: hash, Status: model.FindingOpen}
	}

	// scan 1: A + B open
	id1 := run([]model.ScanFinding{mk("sast.semgrep.ruleA", hashA), mk("sast.semgrep.ruleB", hashB)})

	// ignore A on scan 1
	var findingAID string
	_ = s.RunInOrg(ctx, devOrg, func(tx pgx.Tx) error {
		fs, _ := s.ListFindings(ctx, tx, id1)
		for _, f := range fs {
			if f.FindingHash == hashA {
				findingAID = f.ID
			}
		}
		reason := "false positive"
		return s.UpdateFindingStatus(ctx, tx, findingAID, model.FindingIgnored, &reason)
	})
	if findingAID == "" {
		t.Fatal("finding A not found")
	}

	// scan 2: A + C (B disappeared)
	id2 := run([]model.ScanFinding{mk("sast.semgrep.ruleA", hashA), mk("sast.semgrep.ruleC", hashC)})

	byHash := map[string]model.ScanFinding{}
	_ = s.RunInOrg(ctx, devOrg, func(tx pgx.Tx) error {
		fs, e := s.ListFindings(ctx, tx, id2)
		for _, f := range fs {
			byHash[f.FindingHash] = f
		}
		return e
	})
	if a := byHash[hashA]; a.Status != model.FindingIgnored {
		t.Errorf("A status = %q want ignored (carried forward)", a.Status)
	}
	if b := byHash[hashB]; b.Status != model.FindingFixed {
		t.Errorf("B status = %q want fixed (disappeared)", b.Status)
	}
	if c := byHash[hashC]; c.Status != model.FindingOpen {
		t.Errorf("C status = %q want open (new)", c.Status)
	}
}

// fakeSAST is an in-memory SASTScanner for triage/ephemeral tests (no binary).
type fakeSAST struct{ out []model.ScanFinding }

func (f fakeSAST) Name() string { return "fake-sast" }
func (f fakeSAST) Scan(ctx context.Context, srcDir string) ([]model.ScanFinding, error) {
	// return copies so the worker can mutate status safely
	cp := make([]model.ScanFinding, len(f.out))
	copy(cp, f.out)
	return cp, nil
}
