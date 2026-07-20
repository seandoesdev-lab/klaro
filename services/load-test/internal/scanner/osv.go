package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/klaro/load-test/internal/model"
)

// osv-scanner exit codes (v1.x):
//
//	0   no vulnerabilities
//	1   vulnerabilities found
//	128 no package sources / lockfiles found  → [SAST-02] normal 0 findings
//
// Any other non-zero exit is a real execution error and is surfaced (M-1): we do
// NOT silently turn a scanner failure into "0 findings" (which would masquerade
// as a clean, score=100 result).
const (
	osvExitNoVuln    = 0
	osvExitVuln      = 1
	osvExitNoPackage = 128
)

// OSV runs osv-scanner over dependency manifests/lockfiles found in the source
// tree. [COST-05]/M-1: when Offline is set, osv-scanner runs against a build-time
// bundled local advisory DB (LocalDBPath) with no runtime network access.
type OSV struct {
	Bin         string // default "osv-scanner"
	Offline     bool   // run against the bundled local DB, no network
	LocalDBPath string // OSV_SCANNER_LOCAL_DB dir (build-time populated)
}

func (o OSV) Name() string { return "osv" }

func (o OSV) bin() string {
	if o.Bin != "" {
		return o.Bin
	}
	return "osv-scanner"
}

// Scan runs osv-scanner --format json --recursive over srcDir and distinguishes
// "no lockfile" (0 findings) from a genuine execution failure (error).
func (o OSV) Scan(ctx context.Context, srcDir string) ([]model.ScanFinding, error) {
	args := []string{"--format", "json", "--recursive"}
	if o.Offline {
		// --offline disables all network lookups; the local DB path is provided
		// via env so the flag name stays stable across osv-scanner versions.
		args = append(args, "--offline")
	}
	args = append(args, srcDir)

	cmd := exec.CommandContext(ctx, o.bin(), args...)
	if o.Offline && o.LocalDBPath != "" {
		cmd.Env = append(os.Environ(), "OSV_SCANNER_LOCAL_DB="+o.LocalDBPath)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	if ctx.Err() != nil {
		return nil, fmt.Errorf("osv-scanner timed out: %w", ctx.Err())
	}
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			// binary missing / could not start — a real failure.
			return nil, fmt.Errorf("osv-scanner: %w", err)
		}
		switch ee.ExitCode() {
		case osvExitVuln:
			// vulnerabilities found → parse the JSON below.
		case osvExitNoPackage:
			// no lockfile/manifest found → [SAST-02] 0 findings, not a failure.
			return nil, nil
		default:
			// M-1: surface the real error instead of swallowing it as 0 findings.
			return nil, fmt.Errorf("osv-scanner exit %d: %s", ee.ExitCode(), truncate(stderr.String(), 300))
		}
	}
	return parseOSV(out)
}

// osv-scanner JSON shapes (subset).
type osvOutput struct {
	Results []struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
		Packages []struct {
			Package struct {
				Name      string `json:"name"`
				Version   string `json:"version"`
				Ecosystem string `json:"ecosystem"`
			} `json:"package"`
			Vulnerabilities []osvVuln `json:"vulnerabilities"`
		} `json:"packages"`
	} `json:"results"`
}
type osvVuln struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
}

func parseOSV(data []byte) ([]model.ScanFinding, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var o osvOutput
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("osv parse: %w", err)
	}
	var out []model.ScanFinding
	for _, res := range o.Results {
		manifest := res.Source.Path
		for _, pkg := range res.Packages {
			name, ver := pkg.Package.Name, pkg.Package.Version
			for _, v := range pkg.Vulnerabilities {
				ruleID := "osv." + v.ID
				cvss, sev := osvSeverity(v)
				title := fmt.Sprintf("%s %s: %s", name, ver, truncate(strings.TrimSpace(v.Summary), 240))

				f := openFinding(ruleID, sev, title)
				f.FilePath = strPtr(manifest)
				f.Package = strPtr(name)
				f.PackageVersion = strPtr(ver)
				f.FindingHash = model.FindingHashParts(ruleID, name, ver)

				ev, _ := json.Marshal(map[string]any{
					"advisory": v.ID,
					"cvss":     cvss,
					"summary":  v.Summary,
				})
				f.Evidence = ev
				out = append(out, f)
			}
		}
	}
	return out, nil
}

// osvSeverity returns a human CVSS string (best effort) and the mapped severity.
func osvSeverity(v osvVuln) (string, model.Severity) {
	for _, s := range v.Severity {
		if score, err := strconv.ParseFloat(strings.TrimSpace(s.Score), 64); err == nil {
			return s.Score, mapCVSS(score)
		}
	}
	// CVSS vector or absent numeric → fall back to textual database_specific label.
	if lbl := v.DatabaseSpecific.Severity; lbl != "" {
		cvss := ""
		if len(v.Severity) > 0 {
			cvss = v.Severity[0].Score
		}
		return cvss, mapOSVSeverity(lbl)
	}
	return "", model.SeverityMedium // unknown → medium (design §6.3)
}
