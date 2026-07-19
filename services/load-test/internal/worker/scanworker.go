package worker

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/store"
)

// ScanDeps are the collaborators the scan worker needs.
type ScanDeps struct {
	Queue queue.ScanQueue
	Store *store.Store
	// HTTPClient is used for DAST fetches; defaults to a 10s-timeout client.
	HTTPClient *http.Client
}

func (d ScanDeps) client() *http.Client {
	if d.HTTPClient != nil {
		return d.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// RunScan consumes scan jobs until ctx is cancelled. Intended to run as its own
// goroutine alongside the load-test worker loop.
func RunScan(ctx context.Context, d ScanDeps) {
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
		if err := d.processScan(ctx, job); err != nil {
			log.Printf("scan %s failed: %v", job.ScanID, err)
		}
	}
}

func (d ScanDeps) processScan(ctx context.Context, job model.ScanJob) error {
	id := job.ScanID
	if err := d.Store.UpdateScanStatus(ctx, id, model.ScanStatusRunning, nil); err != nil {
		return err
	}

	var findings []model.ScanFinding
	switch job.Type {
	case model.ScanTypeDAST:
		fs, err := d.runDAST(ctx, job.TargetURL)
		if err != nil {
			_ = d.Store.UpdateScanStatus(ctx, id, model.ScanStatusFailed, nil)
			return err
		}
		findings = fs
	case model.ScanTypeSAST:
		findings = stubSAST(job.TargetURL)
	default:
		_ = d.Store.UpdateScanStatus(ctx, id, model.ScanStatusFailed, nil)
		return nil
	}

	for i := range findings {
		findings[i].ScanID = id
	}
	if err := d.Store.SaveFindings(ctx, findings); err != nil {
		_ = d.Store.UpdateScanStatus(ctx, id, model.ScanStatusFailed, nil)
		return err
	}
	score := model.ComputeScore(findings)
	return d.Store.UpdateScanStatus(ctx, id, model.ScanStatusCompleted, &score)
}

// runDAST fetches the target and analyzes response headers. The pure analysis
// lives in AnalyzeHeaders so it can be tested without the network.
func (d ScanDeps) runDAST(ctx context.Context, targetURL string) ([]model.ScanFinding, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return AnalyzeHeaders(resp.Header, targetURL), nil
}

// minStrongHSTSMaxAge is the max-age (seconds, ~180d) below which an HSTS header
// is considered weak.
const minStrongHSTSMaxAge = 15552000

// AnalyzeHeaders inspects HTTP response headers and returns security findings.
// It is a pure function: given the same headers and location it always yields
// the same findings (including stable finding hashes).
func AnalyzeHeaders(h http.Header, location string) []model.ScanFinding {
	var out []model.ScanFinding
	add := func(ruleID string, sev model.Severity, title string) {
		out = append(out, model.ScanFinding{
			RuleID:      ruleID,
			Severity:    sev,
			Title:       title,
			FindingHash: model.FindingHash(ruleID, location),
			Status:      model.FindingOpen,
		})
	}

	// Strict-Transport-Security: missing or weak max-age -> high.
	hsts := h.Get("Strict-Transport-Security")
	if hsts == "" {
		add("dast.missing-hsts", model.SeverityHigh,
			"Missing Strict-Transport-Security header")
	} else if maxAge := hstsMaxAge(hsts); maxAge < minStrongHSTSMaxAge {
		add("dast.weak-hsts", model.SeverityHigh,
			"Weak Strict-Transport-Security max-age (below 180 days)")
	}

	// Content-Security-Policy: missing -> medium.
	if h.Get("Content-Security-Policy") == "" {
		add("dast.missing-csp", model.SeverityMedium,
			"Missing Content-Security-Policy header")
	}

	// X-Frame-Options: missing -> low (clickjacking).
	if h.Get("X-Frame-Options") == "" {
		add("dast.missing-x-frame-options", model.SeverityLow,
			"Missing X-Frame-Options header (clickjacking risk)")
	}

	// X-Content-Type-Options: missing -> low (MIME sniffing).
	if h.Get("X-Content-Type-Options") == "" {
		add("dast.missing-x-content-type-options", model.SeverityLow,
			"Missing X-Content-Type-Options header (MIME sniffing risk)")
	}

	// Server header version disclosure: version -> low, bare software -> info.
	if server := h.Get("Server"); server != "" {
		if hasVersion(server) {
			add("dast.server-version-disclosure", model.SeverityLow,
				"Server header discloses software version: "+server)
		} else {
			add("dast.server-software-disclosure", model.SeverityInfo,
				"Server header discloses software: "+server)
		}
	}

	// X-XSS-Protection: missing -> info (legacy hardening).
	if h.Get("X-XSS-Protection") == "" {
		add("dast.missing-x-xss-protection", model.SeverityInfo,
			"Missing X-XSS-Protection header")
	}

	return out
}

// hstsMaxAge extracts the max-age directive (seconds) from an HSTS header value,
// returning 0 if absent or unparseable.
func hstsMaxAge(v string) int {
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(strings.ToLower(part))
		if !strings.HasPrefix(part, "max-age") {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			return 0
		}
		n := 0
		for _, ch := range strings.TrimSpace(part[eq+1:]) {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		return n
	}
	return 0
}

// hasVersion reports whether a header value contains a version-looking token
// (a digit adjacent to a '.' or '/'), e.g. "nginx/1.25.3" or "Apache/2.4".
func hasVersion(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c >= '0' && c <= '9' {
			// only treat as a version if preceded by '/' or '.' somewhere
			if strings.ContainsAny(v, "/.") {
				return true
			}
		}
	}
	return false
}

// stubSAST returns representative placeholder findings. Full SAST integration
// (Semgrep + GitHub PR checkout into tmpfs) is future work; these rows are real
// but clearly labeled as stubs so the UI and triage flow can be exercised.
func stubSAST(location string) []model.ScanFinding {
	fp1 := "config/database.go"
	ln1 := 42
	fp2 := "internal/auth/token.go"
	ln2 := 88
	return []model.ScanFinding{
		{
			RuleID:      "sast.hardcoded-secret",
			Severity:    model.SeverityMedium,
			Title:       "[STUB] Hardcoded credential detected — full SAST (Semgrep/GitHub) integration is future",
			FilePath:    &fp1,
			Line:        &ln1,
			FindingHash: model.FindingHash("sast.hardcoded-secret", "config/database.go:42"),
			Status:      model.FindingOpen,
		},
		{
			RuleID:      "sast.weak-crypto",
			Severity:    model.SeverityLow,
			Title:       "[STUB] Weak cryptographic algorithm (MD5) — full SAST (Semgrep/GitHub) integration is future",
			FilePath:    &fp2,
			Line:        &ln2,
			FindingHash: model.FindingHash("sast.weak-crypto", "internal/auth/token.go:88"),
			Status:      model.FindingOpen,
		},
	}
}
