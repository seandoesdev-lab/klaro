package worker

import (
	"net/http"
	"testing"

	"github.com/klaro/load-test/internal/model"
)

// ruleSet turns findings into a rule_id->severity map for easy assertions.
func ruleSet(fs []model.ScanFinding) map[string]model.Severity {
	m := make(map[string]model.Severity, len(fs))
	for _, f := range fs {
		m[f.RuleID] = f.Severity
	}
	return m
}

func TestAnalyzeHeaders(t *testing.T) {
	const loc = "https://staging.example.com"
	cases := []struct {
		name    string
		headers http.Header
		want    map[string]model.Severity // expected rule_id -> severity subset
		absent  []string                  // rule_ids that must NOT appear
	}{
		{
			name:    "no security headers at all",
			headers: http.Header{},
			want: map[string]model.Severity{
				"dast.missing-hsts":                   model.SeverityHigh,
				"dast.missing-csp":                    model.SeverityMedium,
				"dast.missing-x-frame-options":        model.SeverityLow,
				"dast.missing-x-content-type-options": model.SeverityLow,
				"dast.missing-x-xss-protection":       model.SeverityInfo,
			},
		},
		{
			name: "fully hardened response",
			headers: http.Header{
				"Strict-Transport-Security": {"max-age=63072000; includeSubDomains"},
				"Content-Security-Policy":   {"default-src 'self'"},
				"X-Frame-Options":           {"DENY"},
				"X-Content-Type-Options":    {"nosniff"},
				"X-Xss-Protection":          {"1; mode=block"},
			},
			want: map[string]model.Severity{},
			absent: []string{
				"dast.missing-hsts", "dast.weak-hsts", "dast.missing-csp",
				"dast.missing-x-frame-options", "dast.missing-x-content-type-options",
				"dast.missing-x-xss-protection",
			},
		},
		{
			name: "weak hsts max-age",
			headers: http.Header{
				"Strict-Transport-Security": {"max-age=3600"},
				"Content-Security-Policy":   {"default-src 'self'"},
				"X-Frame-Options":           {"DENY"},
				"X-Content-Type-Options":    {"nosniff"},
				"X-Xss-Protection":          {"0"},
			},
			want: map[string]model.Severity{
				"dast.weak-hsts": model.SeverityHigh,
			},
			absent: []string{"dast.missing-hsts"},
		},
		{
			name: "server version disclosure is low",
			headers: http.Header{
				"Strict-Transport-Security": {"max-age=63072000"},
				"Content-Security-Policy":   {"default-src 'self'"},
				"X-Frame-Options":           {"DENY"},
				"X-Content-Type-Options":    {"nosniff"},
				"X-Xss-Protection":          {"0"},
				"Server":                    {"nginx/1.25.3"},
			},
			want: map[string]model.Severity{
				"dast.server-version-disclosure": model.SeverityLow,
			},
			absent: []string{"dast.server-software-disclosure"},
		},
		{
			name: "server software without version is info",
			headers: http.Header{
				"Strict-Transport-Security": {"max-age=63072000"},
				"Content-Security-Policy":   {"default-src 'self'"},
				"X-Frame-Options":           {"DENY"},
				"X-Content-Type-Options":    {"nosniff"},
				"X-Xss-Protection":          {"0"},
				"Server":                    {"cloudflare"},
			},
			want: map[string]model.Severity{
				"dast.server-software-disclosure": model.SeverityInfo,
			},
			absent: []string{"dast.server-version-disclosure"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ruleSet(AnalyzeHeaders(tc.headers, loc))
			for rule, sev := range tc.want {
				if got[rule] != sev {
					t.Errorf("rule %s: got severity %q want %q", rule, got[rule], sev)
				}
			}
			for _, rule := range tc.absent {
				if _, ok := got[rule]; ok {
					t.Errorf("rule %s should be absent but was present", rule)
				}
			}
		})
	}
}

func TestAnalyzeHeadersHashStable(t *testing.T) {
	h := http.Header{}
	a := AnalyzeHeaders(h, "https://a.example.com")
	b := AnalyzeHeaders(h, "https://a.example.com")
	if len(a) != len(b) {
		t.Fatalf("finding count differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].FindingHash != b[i].FindingHash {
			t.Errorf("hash unstable for %s: %s vs %s", a[i].RuleID, a[i].FindingHash, b[i].FindingHash)
		}
		if a[i].FindingHash == "" {
			t.Errorf("empty hash for %s", a[i].RuleID)
		}
		if a[i].Status != model.FindingOpen {
			t.Errorf("finding %s status = %q want open", a[i].RuleID, a[i].Status)
		}
	}
	// A different location must yield a different hash for the same rule.
	c := AnalyzeHeaders(h, "https://b.example.com")
	if a[0].FindingHash == c[0].FindingHash {
		t.Errorf("hash should differ across locations")
	}
}

func TestStubSAST(t *testing.T) {
	fs := stubSAST("repo")
	if len(fs) < 2 {
		t.Fatalf("expected at least 2 stub findings, got %d", len(fs))
	}
	for _, f := range fs {
		if f.FilePath == nil || f.Line == nil {
			t.Errorf("sast finding %s missing file/line", f.RuleID)
		}
		if f.FindingHash == "" {
			t.Errorf("sast finding %s missing hash", f.RuleID)
		}
	}
}
