package scanner

import (
	"strconv"
	"strings"

	"github.com/klaro/load-test/internal/model"
)

// mapSemgrepSeverity maps Semgrep severity, preferring metadata.security-severity
// (a CVSS base score) when present (design §6.3).
func mapSemgrepSeverity(sev, securitySeverity string) model.Severity {
	if securitySeverity != "" {
		if score, err := strconv.ParseFloat(strings.TrimSpace(securitySeverity), 64); err == nil {
			return mapCVSS(score)
		}
	}
	switch strings.ToUpper(strings.TrimSpace(sev)) {
	case "ERROR":
		return model.SeverityHigh
	case "WARNING":
		return model.SeverityMedium
	case "INFO":
		return model.SeverityLow
	default:
		return model.SeverityMedium
	}
}

// mapCVSS maps a CVSS base score to a klaro severity (design §6.3).
func mapCVSS(score float64) model.Severity {
	switch {
	case score >= 9.0:
		return model.SeverityCritical
	case score >= 7.0:
		return model.SeverityHigh
	case score >= 4.0:
		return model.SeverityMedium
	case score > 0:
		return model.SeverityLow
	default:
		return model.SeverityMedium // unknown → medium
	}
}

// mapOSVSeverity maps an osv advisory's textual severity label when no numeric
// CVSS is available. Unknown → medium.
func mapOSVSeverity(label string) model.Severity {
	switch strings.ToUpper(strings.TrimSpace(label)) {
	case "CRITICAL":
		return model.SeverityCritical
	case "HIGH":
		return model.SeverityHigh
	case "MEDIUM", "MODERATE":
		return model.SeverityMedium
	case "LOW":
		return model.SeverityLow
	default:
		return model.SeverityMedium
	}
}

// mapZAPRisk maps a ZAP alert risk to a klaro severity (design §6.3).
func mapZAPRisk(risk string) model.Severity {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "high":
		return model.SeverityHigh
	case "medium":
		return model.SeverityMedium
	case "low":
		return model.SeverityLow
	case "informational", "info":
		return model.SeverityInfo
	default:
		return model.SeverityInfo
	}
}
