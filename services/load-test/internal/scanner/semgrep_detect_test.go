package scanner

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestSemgrepDetectConfigs proves D-1: only the rule packs for languages actually
// present in the source tree are selected, so scans stay fast and bounded.
func TestSemgrepDetectConfigs(t *testing.T) {
	rules := t.TempDir()
	for _, l := range []string{"python", "go", "javascript", "typescript", "java"} {
		if err := os.MkdirAll(filepath.Join(rules, l), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "app.py"), "print('x')")
	mustWrite(t, filepath.Join(src, "pkg", "main.go"), "package main")
	// a node_modules tree must be skipped (not descended)
	mustWrite(t, filepath.Join(src, "node_modules", "dep", "index.js"), "x")

	got := (Semgrep{RulesDir: rules}).detectConfigs(src)
	var names []string
	for _, c := range got {
		names = append(names, filepath.Base(c))
	}
	sort.Strings(names)
	// python + go detected; javascript must NOT (only under skipped node_modules)
	want := map[string]bool{"python": true, "go": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected config pack %q (js under node_modules should be skipped)", n)
		}
		delete(want, n)
	}
	if len(want) != 0 {
		t.Errorf("missing expected packs: %v (got %v)", want, names)
	}
}

func TestSemgrepDetectNoLanguage(t *testing.T) {
	rules := t.TempDir()
	_ = os.MkdirAll(filepath.Join(rules, "python"), 0o755)
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "README.txt"), "no code here")
	if got := (Semgrep{RulesDir: rules}).detectConfigs(src); len(got) != 0 {
		t.Errorf("no supported language → want 0 configs, got %v", got)
	}
}

func TestNormalizeCheckID(t *testing.T) {
	cases := []struct{ id, rules, want string }{
		{"opt.semgrep-rules.python.lang.security.audit.x", "/opt/semgrep-rules", "python.lang.security.audit.x"},
		{"python.lang.security.x", "/opt/semgrep-rules", "python.lang.security.x"}, // already clean
		{"anything", "", "anything"}, // no rulesDir → unchanged
	}
	for _, c := range cases {
		if got := normalizeCheckID(c.id, c.rules); got != c.want {
			t.Errorf("normalizeCheckID(%q,%q) = %q want %q", c.id, c.rules, got, c.want)
		}
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
