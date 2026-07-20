package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/klaro/load-test/internal/model"
)

// Semgrep runs the Semgrep CLI with a bundled, offline ruleset ([COST-05]: no
// --config auto / registry / account). RulesDir is $SEMGREP_RULES_DIR.
//
// [D-1] Running the full semgrep-rules repo (~2151 rules) never finishes within a
// reasonable timeout. Instead we detect the languages present in the source tree
// and scope --config to just those language rule subdirectories (e.g.
// /opt/semgrep-rules/python), which completes in seconds and yields real findings.
type Semgrep struct {
	Bin      string // default "semgrep"
	RulesDir string // bundled rules dir, e.g. /opt/semgrep-rules
}

func (s Semgrep) Name() string { return "semgrep" }

func (s Semgrep) bin() string {
	if s.Bin != "" {
		return s.Bin
	}
	return "semgrep"
}

// langRuleDirs maps a file extension (or basename) to semgrep-rules subdirectory
// names. Detection walks the source tree and enables only the matching packs.
var langRuleDirs = map[string][]string{
	".py":    {"python"},
	".js":    {"javascript", "typescript"},
	".jsx":   {"javascript", "typescript"},
	".mjs":   {"javascript"},
	".cjs":   {"javascript"},
	".ts":    {"typescript"},
	".tsx":   {"typescript"},
	".go":    {"go"},
	".java":  {"java"},
	".rb":    {"ruby"},
	".php":   {"php"},
	".cs":    {"csharp"},
	".c":     {"c"},
	".h":     {"c"},
	".cpp":   {"cpp"},
	".cc":    {"cpp"},
	".cxx":   {"cpp"},
	".hpp":   {"cpp"},
	".kt":    {"kotlin"},
	".kts":   {"kotlin"},
	".scala": {"scala"},
	".rs":    {"rust"},
	".tf":    {"terraform"},
	".yaml":  {"yaml"},
	".yml":   {"yaml"},
	".sh":    {"bash"},
	".bash":  {"bash"},
	".swift": {"swift"},
	".ex":    {"elixir"},
	".exs":   {"elixir"},
}

// skipDirs are large/irrelevant trees we don't descend for language detection.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, ".venv": true, "venv": true,
}

// detectConfigs walks srcDir, collects present languages, and returns the existing
// rule subdirectories under RulesDir to pass as --config. Bounded by a file cap.
func (s Semgrep) detectConfigs(srcDir string) []string {
	langs := map[string]bool{}
	const fileCap = 50000
	seen := 0
	_ = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		seen++
		if seen > fileCap {
			return filepath.SkipAll
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if base := strings.ToLower(d.Name()); base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") {
			langs["dockerfile"] = true
		}
		for _, l := range langRuleDirs[ext] {
			langs[l] = true
		}
		return nil
	})
	var configs []string
	for l := range langs {
		dir := filepath.Join(s.RulesDir, l)
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			configs = append(configs, dir)
		}
	}
	return configs
}

// Scan runs Semgrep over srcDir with language-scoped configs and normalizes the
// JSON output. Semgrep exits non-zero when findings are present, so we parse
// stdout regardless of exit code and only treat unparseable/empty output as error.
func (s Semgrep) Scan(ctx context.Context, srcDir string) ([]model.ScanFinding, error) {
	if s.RulesDir == "" {
		return nil, fmt.Errorf("semgrep: SEMGREP_RULES_DIR not configured")
	}
	configs := s.detectConfigs(srcDir)
	if len(configs) == 0 {
		// No supported language detected → nothing to scan (not an error).
		return nil, nil
	}
	args := make([]string, 0, len(configs)*2+5)
	for _, cfg := range configs {
		args = append(args, "--config", cfg)
	}
	args = append(args, "--json", "--metrics=off", "--quiet", "--no-git-ignore", srcDir)
	cmd := exec.CommandContext(ctx, s.bin(), args...)
	out, err := cmd.Output()
	if len(out) == 0 {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("semgrep timed out: %w", ctx.Err())
		}
		if err != nil {
			return nil, fmt.Errorf("semgrep: %w", err)
		}
	}
	return s.parse(out, srcDir)
}

// semgrep JSON shapes (subset).
type semgrepOutput struct {
	Results []semgrepResult `json:"results"`
}
type semgrepResult struct {
	CheckID string `json:"check_id"`
	Path    string `json:"path"`
	Start   struct {
		Line int `json:"line"`
	} `json:"start"`
	Extra struct {
		Message  string          `json:"message"`
		Severity string          `json:"severity"`
		Lines    string          `json:"lines"`
		Metadata semgrepMetadata `json:"metadata"`
	} `json:"extra"`
}
type semgrepMetadata struct {
	CWE              json.RawMessage `json:"cwe"`               // string | []string
	Confidence       string          `json:"confidence"`        // HIGH|MEDIUM|LOW
	SecuritySeverity string          `json:"security-severity"` // CVSS base score
	References       []string        `json:"references"`
}

// parse is the method form used by Scan; it normalizes check_ids relative to the
// rules dir so directory-based --config doesn't produce noisy path-prefixed ids
// (e.g. sast.semgrep.opt.semgrep-rules.python… → sast.semgrep.python…).
func (s Semgrep) parse(data []byte, srcDir string) ([]model.ScanFinding, error) {
	return parseSemgrepWith(data, srcDir, s.RulesDir)
}

func parseSemgrep(data []byte, srcDir string) ([]model.ScanFinding, error) {
	return parseSemgrepWith(data, srcDir, "")
}

// normalizeCheckID strips a leading dotted form of the rules dir path from a
// semgrep check_id (e.g. "opt.semgrep-rules.python.lang…" → "python.lang…").
func normalizeCheckID(id, rulesDir string) string {
	if rulesDir == "" {
		return id
	}
	dotted := strings.Trim(filepath.ToSlash(rulesDir), "/")
	dotted = strings.ReplaceAll(dotted, "/", ".")
	return strings.TrimPrefix(id, dotted+".")
}

func parseSemgrepWith(data []byte, srcDir, rulesDir string) ([]model.ScanFinding, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var o semgrepOutput
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("semgrep parse: %w", err)
	}
	out := make([]model.ScanFinding, 0, len(o.Results))
	for _, r := range o.Results {
		ruleID := "sast.semgrep." + normalizeCheckID(r.CheckID, rulesDir)
		rel := relPath(srcDir, r.Path)
		snippet := normalizeSnippet(r.Extra.Lines)
		sev := mapSemgrepSeverity(r.Extra.Severity, r.Extra.Metadata.SecuritySeverity)

		f := openFinding(ruleID, sev, semgrepTitle(r))
		f.FilePath = strPtr(rel)
		f.Line = intPtr(r.Start.Line)
		f.CWE = strPtr(firstCWE(r.Extra.Metadata.CWE))
		f.Confidence = strPtr(r.Extra.Metadata.Confidence)
		f.FindingHash = model.FindingHashParts(ruleID, rel, snippet)

		ev, _ := json.Marshal(map[string]any{
			"lines":      capSnippet(r.Extra.Lines),
			"message":    r.Extra.Message,
			"references": r.Extra.Metadata.References,
		})
		f.Evidence = ev
		out = append(out, f)
	}
	return out, nil
}

func semgrepTitle(r semgrepResult) string {
	if msg := r.Extra.Message; msg != "" {
		return truncate(msg, 300)
	}
	return r.CheckID
}

// firstCWE handles Semgrep's cwe metadata being either a string or a []string.
func firstCWE(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr[0]
	}
	return ""
}
