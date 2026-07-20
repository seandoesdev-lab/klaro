// Package scanner is the S2 security-scan real engine. It runs Semgrep + osv
// (SAST) over an Ephemeral source tree and OWASP ZAP + header analysis (DAST),
// returning normalized []model.ScanFinding. It is deliberately DB-agnostic: no
// store/tenancy imports, only model. Orchestration (status transitions, source
// lifecycle, re-scan triage) lives in the scan worker.
package scanner

import (
	"context"

	"github.com/klaro/load-test/internal/model"
)

// Mode selects the DAST scan aggressiveness. baseline is passive/safe (default);
// active fires attack payloads and is only reachable behind the [SC-01] verified-
// domain gate (enforced by the API before enqueue).
type Mode string

const (
	ModeBaseline Mode = "baseline"
	ModeActive   Mode = "active"
)

// ParseMode normalizes a request mode string, defaulting to baseline.
func ParseMode(s string) Mode {
	if Mode(s) == ModeActive {
		return ModeActive
	}
	return ModeBaseline
}

// SASTScanner analyzes a checked-out source tree (Semgrep, osv-scanner).
type SASTScanner interface {
	Name() string
	Scan(ctx context.Context, srcDir string) ([]model.ScanFinding, error)
}

// DASTScanner probes a running target URL (OWASP ZAP).
type DASTScanner interface {
	Name() string
	Scan(ctx context.Context, targetURL string, mode Mode) ([]model.ScanFinding, error)
}

// Source identifies the SAST source tree to acquire. Mirrors model.ScanSource but
// keeps the scanner package free of the queue payload's JSON concerns.
type Source struct {
	Type    string // "repo" | "upload"
	RepoURL string
	Ref     string // optional branch/tag; empty → default HEAD
	Token   string // upload staging token
}

// SnippetMaxLines caps how many code lines evidence may retain. [EPHEM-01] keeps
// findings from reconstructing a full source copy.
const SnippetMaxLines = 10

// Default per-scanner timeouts (design #4). The worker derives child contexts.
// [D-1] SemgrepTimeout raised to 900s as headroom; the primary fix is scoping
// --config to detected-language rule packs (see Semgrep.detectConfigs) so scans
// finish in seconds rather than hitting the ceiling on the full ruleset.
const (
	CloneTimeoutSec       = 120
	SemgrepTimeoutSec     = 900
	OSVTimeoutSec         = 120
	ZAPBaselineTimeoutSec = 600
	ZAPActiveTimeoutSec   = 900
)
