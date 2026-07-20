//go:build integration

package scanner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestSemgrepExecFindsPythonVuln proves D-1 end-to-end: with language-scoped
// configs, a 3-line vulnerable Python file yields a finding well within the
// timeout (no context-deadline failure). Requires the semgrep binary and the
// bundled rules dir; skips otherwise.
//
//	SEMGREP_RULES_DIR=/opt/semgrep-rules go test -tags integration \
//	  ./internal/scanner/ -run TestSemgrepExecFindsPythonVuln -v
func TestSemgrepExecFindsPythonVuln(t *testing.T) {
	if _, err := exec.LookPath("semgrep"); err != nil {
		t.Skip("semgrep binary not on PATH")
	}
	rulesDir := os.Getenv("SEMGREP_RULES_DIR")
	if rulesDir == "" {
		rulesDir = "/opt/semgrep-rules"
	}
	if st, err := os.Stat(filepath.Join(rulesDir, "python")); err != nil || !st.IsDir() {
		t.Skipf("rules dir %s/python not present", rulesDir)
	}

	src := t.TempDir()
	// dangerous subprocess(shell=True) — a classic semgrep python finding.
	code := "import subprocess\n" +
		"def run(cmd):\n" +
		"    return subprocess.call(cmd, shell=True)\n"
	if err := os.WriteFile(filepath.Join(src, "app.py"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	start := time.Now()
	fs, err := (Semgrep{RulesDir: rulesDir}).Scan(ctx, src)
	if err != nil {
		t.Fatalf("semgrep scan failed (D-1 regression?): %v", err)
	}
	if len(fs) == 0 {
		t.Fatalf("expected at least one finding for shell=True subprocess")
	}
	t.Logf("semgrep returned %d findings in %s", len(fs), time.Since(start))
	for _, f := range fs {
		// rule_id must stay under the sast.semgrep.* namespace and be normalized
		// (no "opt.semgrep-rules." path noise).
		if len(f.RuleID) < len("sast.semgrep.") || f.RuleID[:13] != "sast.semgrep." {
			t.Errorf("rule_id %q not namespaced", f.RuleID)
		}
		if contains(f.RuleID, "opt.semgrep-rules") {
			t.Errorf("rule_id %q not normalized (path noise present)", f.RuleID)
		}
	}
}
