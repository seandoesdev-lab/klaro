package scanner

import (
	"path/filepath"
	"strings"

	"github.com/klaro/load-test/internal/model"
)

// relPath converts an absolute scanner-reported path to a repo-relative path by
// stripping the work-dir prefix. Falls back to the input if it isn't under root.
func relPath(root, p string) string {
	if p == "" {
		return ""
	}
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

// normalizeSnippet trims each line and collapses internal whitespace runs to a
// single space, then joins with "\n". Line numbers are intentionally excluded so
// the SAST finding_hash is stable across harmless line shifts (design #7).
func normalizeSnippet(lines string) string {
	raw := strings.Split(strings.ReplaceAll(lines, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimSpace(l)
		l = strings.Join(strings.Fields(l), " ")
		out = append(out, l)
	}
	// drop leading/trailing blank lines
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// capSnippet limits a raw snippet to SnippetMaxLines lines ([EPHEM-01]).
func capSnippet(lines string) string {
	raw := strings.Split(strings.ReplaceAll(lines, "\r\n", "\n"), "\n")
	if len(raw) > SnippetMaxLines {
		raw = raw[:SnippetMaxLines]
	}
	return strings.Join(raw, "\n")
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// openFinding is the common constructor for a fresh open finding.
func openFinding(ruleID string, sev model.Severity, title string) model.ScanFinding {
	return model.ScanFinding{
		RuleID:   ruleID,
		Severity: sev,
		Title:    title,
		Status:   model.FindingOpen,
	}
}
