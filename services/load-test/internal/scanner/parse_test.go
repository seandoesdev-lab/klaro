package scanner

import (
	"net/http"
	"testing"

	"github.com/klaro/load-test/internal/model"
)

// bySev turns findings into rule_id -> severity for assertions.
func bySev(fs []model.ScanFinding) map[string]model.Severity {
	m := make(map[string]model.Severity, len(fs))
	for _, f := range fs {
		m[f.RuleID] = f.Severity
	}
	return m
}
func byRule(fs []model.ScanFinding) map[string]model.ScanFinding {
	m := make(map[string]model.ScanFinding, len(fs))
	for _, f := range fs {
		m[f.RuleID] = f
	}
	return m
}

func TestParseSemgrep(t *testing.T) {
	const srcDir = "/scan-work/job-abc"
	data := []byte(`{"results":[
	  {"check_id":"go.lang.security.audit.crypto.use-of-md5.use-of-md5",
	   "path":"/scan-work/job-abc/internal/auth/token.go",
	   "start":{"line":88},
	   "extra":{"message":"MD5 is a weak hash",
	     "severity":"WARNING",
	     "lines":"  h := md5.New()  ",
	     "metadata":{"cwe":["CWE-327: Use of a Broken or Risky Cryptographic Algorithm"],"confidence":"HIGH","references":["https://owasp.org"]}}},
	  {"check_id":"generic.secrets.hardcoded",
	   "path":"/scan-work/job-abc/config/db.go",
	   "start":{"line":42},
	   "extra":{"message":"Hardcoded secret","severity":"ERROR","lines":"pw := \"root\"",
	     "metadata":{"security-severity":"9.1","cwe":"CWE-798"}}}
	]}`)
	fs, err := parseSemgrep(data, srcDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("want 2 findings, got %d", len(fs))
	}
	m := byRule(fs)
	md5 := m["sast.semgrep.go.lang.security.audit.crypto.use-of-md5.use-of-md5"]
	if md5.Severity != model.SeverityMedium { // WARNING → medium
		t.Errorf("md5 severity = %q want medium", md5.Severity)
	}
	if md5.FilePath == nil || *md5.FilePath != "internal/auth/token.go" {
		t.Errorf("md5 file_path = %v want repo-relative", md5.FilePath)
	}
	if md5.Line == nil || *md5.Line != 88 {
		t.Errorf("md5 line = %v want 88", md5.Line)
	}
	if md5.CWE == nil || *md5.CWE == "" {
		t.Errorf("md5 cwe missing")
	}
	if md5.FindingHash == "" {
		t.Errorf("md5 hash missing")
	}
	secret := m["sast.semgrep.generic.secrets.hardcoded"]
	if secret.Severity != model.SeverityCritical { // security-severity 9.1 → critical
		t.Errorf("secret severity = %q want critical (CVSS 9.1)", secret.Severity)
	}
}

func TestSemgrepHashStableAcrossLineShift(t *testing.T) {
	mk := func(line int) []byte {
		return []byte(`{"results":[{"check_id":"r","path":"/w/a.go","start":{"line":` +
			itoa(line) + `},"extra":{"message":"m","severity":"ERROR","lines":"x := 1","metadata":{}}}]}`)
	}
	a, _ := parseSemgrep(mk(10), "/w")
	b, _ := parseSemgrep(mk(200), "/w")
	if a[0].FindingHash != b[0].FindingHash {
		t.Errorf("hash must be stable across line shift: %s vs %s", a[0].FindingHash, b[0].FindingHash)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestParseOSV(t *testing.T) {
	data := []byte(`{"results":[{"source":{"path":"go.mod"},"packages":[
	  {"package":{"name":"golang.org/x/net","version":"0.1.0","ecosystem":"Go"},
	   "vulnerabilities":[{"id":"GO-2023-1234","summary":"HTTP/2 rapid reset",
	     "severity":[{"type":"CVSS_V3","score":"7.5"}]}]},
	  {"package":{"name":"lodash","version":"4.17.0","ecosystem":"npm"},
	   "vulnerabilities":[{"id":"GHSA-xxxx","summary":"Prototype pollution",
	     "severity":[{"type":"CVSS_V3","score":"CVSS:3.1/AV:N/AC:L"}],
	     "database_specific":{"severity":"CRITICAL"}}]}
	]}]}`)
	fs, err := parseOSV(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("want 2 osv findings, got %d", len(fs))
	}
	m := byRule(fs)
	net := m["osv.GO-2023-1234"]
	if net.Severity != model.SeverityHigh { // CVSS 7.5 → high
		t.Errorf("net severity = %q want high", net.Severity)
	}
	if net.Package == nil || *net.Package != "golang.org/x/net" {
		t.Errorf("net package = %v", net.Package)
	}
	if net.PackageVersion == nil || *net.PackageVersion != "0.1.0" {
		t.Errorf("net version = %v", net.PackageVersion)
	}
	lodash := m["osv.GHSA-xxxx"]
	// CVSS vector isn't a bare float → falls back to database_specific label.
	if lodash.Severity != model.SeverityCritical {
		t.Errorf("lodash severity = %q want critical (label fallback)", lodash.Severity)
	}
}

func TestParseZAPAlerts(t *testing.T) {
	data := []byte(`{"alerts":[
	  {"pluginId":"10035","name":"Strict-Transport-Security Header Not Set","risk":"High","confidence":"Medium","url":"https://x.example.com/","param":"","evidence":"","cweid":"319","solution":"add HSTS","reference":"https://owasp.org"},
	  {"pluginId":"40012","name":"Cross Site Scripting (Reflected)","risk":"High","confidence":"Medium","url":"https://x.example.com/search","param":"q","evidence":"<script>","cweid":"79"}
	]}`)
	fs, err := parseZAPAlerts(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("want 2 zap findings, got %d", len(fs))
	}
	m := byRule(fs)
	xss := m["zap.40012"]
	if xss.Severity != model.SeverityHigh {
		t.Errorf("xss severity = %q want high", xss.Severity)
	}
	if xss.CWE == nil || *xss.CWE != "79" {
		t.Errorf("xss cwe = %v want 79", xss.CWE)
	}
	if xss.FindingHash == "" {
		t.Errorf("xss hash missing")
	}
	// distinct url/param → distinct hash from the HSTS alert
	if m["zap.10035"].FindingHash == xss.FindingHash {
		t.Errorf("hashes should differ across url/param")
	}
}

func TestDedupDAST(t *testing.T) {
	header := AnalyzeHeaders(http.Header{}, "https://x.example.com")
	// ZAP reports HSTS (10035) and CSP (10038) → those header findings suppressed.
	zap := []model.ScanFinding{
		{RuleID: "zap.10035", Severity: model.SeverityHigh, Status: model.FindingOpen},
		{RuleID: "zap.10038", Severity: model.SeverityMedium, Status: model.FindingOpen},
	}
	merged := DedupDAST(header, zap)
	got := bySev(merged)
	for _, suppressed := range []string{"dast.header.missing-hsts", "dast.header.missing-csp"} {
		if _, ok := got[suppressed]; ok {
			t.Errorf("%s should be suppressed by ZAP", suppressed)
		}
	}
	// non-overlapping header finding survives
	if _, ok := got["dast.header.missing-x-frame-options"]; !ok {
		t.Errorf("non-overlapping header finding must survive")
	}
	// ZAP findings retained
	if _, ok := got["zap.10035"]; !ok {
		t.Errorf("zap finding must be retained")
	}
}

func TestAnalyzeHeadersNamespaced(t *testing.T) {
	fs := AnalyzeHeaders(http.Header{}, "https://x.example.com")
	m := bySev(fs)
	want := map[string]model.Severity{
		"dast.header.missing-hsts":                   model.SeverityHigh,
		"dast.header.missing-csp":                    model.SeverityMedium,
		"dast.header.missing-x-frame-options":        model.SeverityLow,
		"dast.header.missing-x-content-type-options": model.SeverityLow,
		"dast.header.missing-x-xss-protection":       model.SeverityInfo,
	}
	for rule, sev := range want {
		if m[rule] != sev {
			t.Errorf("rule %s: got %q want %q", rule, m[rule], sev)
		}
	}
}

func TestAnalyzeHeadersHashStable(t *testing.T) {
	h := http.Header{}
	a := AnalyzeHeaders(h, "https://a.example.com")
	b := AnalyzeHeaders(h, "https://a.example.com")
	for i := range a {
		if a[i].FindingHash != b[i].FindingHash || a[i].FindingHash == "" {
			t.Errorf("hash unstable/empty for %s", a[i].RuleID)
		}
	}
	c := AnalyzeHeaders(h, "https://b.example.com")
	if a[0].FindingHash == c[0].FindingHash {
		t.Errorf("hash should differ across locations")
	}
}
