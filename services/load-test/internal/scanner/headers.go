package scanner

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/klaro/load-test/internal/model"
)

// minStrongHSTSMaxAge is the max-age (seconds, ~180d) below which an HSTS header
// is considered weak.
const minStrongHSTSMaxAge = 15552000

// AnalyzeHeaders inspects HTTP response headers and returns security findings.
// It is a pure function: given the same headers and location it always yields the
// same findings (including stable finding hashes). [DAST-02] keeps this offline,
// deterministic baseline; rule_ids live under the dast.header.* namespace so ZAP
// (zap.*) can dedup overlapping topics (design #10 / §6.5).
func AnalyzeHeaders(h http.Header, location string) []model.ScanFinding {
	var out []model.ScanFinding
	add := func(ruleID string, sev model.Severity, title string) {
		ev, _ := json.Marshal(map[string]string{"location": location})
		out = append(out, model.ScanFinding{
			RuleID:      ruleID,
			Severity:    sev,
			Title:       title,
			FindingHash: model.FindingHash(ruleID, location),
			Status:      model.FindingOpen,
			Evidence:    ev,
		})
	}

	hsts := h.Get("Strict-Transport-Security")
	if hsts == "" {
		add("dast.header.missing-hsts", model.SeverityHigh,
			"Missing Strict-Transport-Security header")
	} else if maxAge := hstsMaxAge(hsts); maxAge < minStrongHSTSMaxAge {
		add("dast.header.weak-hsts", model.SeverityHigh,
			"Weak Strict-Transport-Security max-age (below 180 days)")
	}

	if h.Get("Content-Security-Policy") == "" {
		add("dast.header.missing-csp", model.SeverityMedium,
			"Missing Content-Security-Policy header")
	}

	if h.Get("X-Frame-Options") == "" {
		add("dast.header.missing-x-frame-options", model.SeverityLow,
			"Missing X-Frame-Options header (clickjacking risk)")
	}

	if h.Get("X-Content-Type-Options") == "" {
		add("dast.header.missing-x-content-type-options", model.SeverityLow,
			"Missing X-Content-Type-Options header (MIME sniffing risk)")
	}

	if server := h.Get("Server"); server != "" {
		if hasVersion(server) {
			add("dast.header.server-version-disclosure", model.SeverityLow,
				"Server header discloses software version: "+server)
		} else {
			add("dast.header.server-software-disclosure", model.SeverityInfo,
				"Server header discloses software: "+server)
		}
	}

	if h.Get("X-XSS-Protection") == "" {
		add("dast.header.missing-x-xss-protection", model.SeverityInfo,
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
			if strings.ContainsAny(v, "/.") {
				return true
			}
		}
	}
	return false
}
