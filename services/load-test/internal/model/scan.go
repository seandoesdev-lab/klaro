package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// ScanType is the kind of security scan.
type ScanType string

const (
	ScanTypeSAST ScanType = "sast"
	ScanTypeDAST ScanType = "dast"
)

// ScanTrigger records how a scan was initiated.
type ScanTrigger string

const (
	ScanTriggerManual   ScanTrigger = "manual"
	ScanTriggerPR       ScanTrigger = "pr"
	ScanTriggerSchedule ScanTrigger = "schedule"
)

// ScanStatus is the lifecycle state of a scan.
type ScanStatus string

const (
	ScanStatusPending   ScanStatus = "pending"
	ScanStatusRunning   ScanStatus = "running"
	ScanStatusCompleted ScanStatus = "completed"
	ScanStatusFailed    ScanStatus = "failed"
)

var scanAllowed = map[ScanStatus]map[ScanStatus]bool{
	ScanStatusPending: {ScanStatusRunning: true, ScanStatusFailed: true},
	ScanStatusRunning: {ScanStatusCompleted: true, ScanStatusFailed: true},
}

// CanScanTransition reports whether moving from->to is a legal scan state change.
func CanScanTransition(from, to ScanStatus) bool {
	return scanAllowed[from][to]
}

// Severity classifies a finding by risk.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

// FindingStatus is the triage state of a single finding.
type FindingStatus string

const (
	FindingOpen    FindingStatus = "open"
	FindingIgnored FindingStatus = "ignored"
	FindingFixed   FindingStatus = "fixed"
)

// Scan is a security scan run (SAST or DAST).
type Scan struct {
	ID         string      `json:"id"`
	ProjectID  string      `json:"-"`
	Type       ScanType    `json:"type"`
	Trigger    ScanTrigger `json:"trigger"`
	TargetURL  *string     `json:"target_url,omitempty"`
	PRNumber   *int        `json:"pr_number,omitempty"`
	SourceType *string     `json:"source_type,omitempty"` // 'repo' | 'upload' (sast)
	SourceRef  *string     `json:"source_ref,omitempty"`  // repo URL or uploaded filename (pointer only)
	Mode       *string     `json:"mode,omitempty"`        // 'baseline' | 'active' (dast)
	Status     ScanStatus  `json:"status"`
	Score      *int        `json:"score,omitempty"`
	StartedAt  *time.Time  `json:"started_at,omitempty"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
}

// ScanFinding is a single security issue produced by a scan.
//
// [SC-05] evidence and the common columns (cwe/confidence/package/...) let a
// finding faithfully represent Semgrep/osv/ZAP output. [EPHEM-01] Evidence carries
// only a capped snippet / scanner metadata — never a full source copy.
type ScanFinding struct {
	ID             string          `json:"id"`
	ScanID         string          `json:"scan_id"`
	RuleID         string          `json:"rule_id"`
	Severity       Severity        `json:"severity"`
	Title          string          `json:"title"`
	FilePath       *string         `json:"file_path,omitempty"`
	Line           *int            `json:"line,omitempty"`
	FindingHash    string          `json:"finding_hash"`
	Status         FindingStatus   `json:"status"`
	IgnoreReason   *string         `json:"ignore_reason,omitempty"`
	CWE            *string         `json:"cwe,omitempty"`
	Confidence     *string         `json:"confidence,omitempty"`
	Package        *string         `json:"package,omitempty"`
	PackageVersion *string         `json:"package_version,omitempty"`
	Evidence       json.RawMessage `json:"evidence,omitempty"` // jsonb
	CreatedAt      time.Time       `json:"created_at"`
}

// ScanSource identifies the SAST source tree to acquire (repo clone or upload).
type ScanSource struct {
	Type    string `json:"type,omitempty"` // repo|upload
	RepoURL string `json:"repo_url,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Token   string `json:"token,omitempty"`
}

// ScanJob is the queue payload consumed by the scan worker.
type ScanJob struct {
	ScanID    string     `json:"scan_id"`
	OrgID     string     `json:"org_id"`
	ProjectID string     `json:"project_id"`
	Type      ScanType   `json:"type"`
	TargetURL string     `json:"target_url"`
	Mode      string     `json:"mode,omitempty"`   // dast
	Source    ScanSource `json:"source,omitempty"` // sast
}

// FindingHash is a stable hash of rule_id + location, used to match a finding
// across re-scans so triage state (ignored/fixed) is preserved.
func FindingHash(ruleID, location string) string {
	return FindingHashParts(ruleID, location)
}

// FindingHashParts hashes an ordered list of stable identity parts joined by "|".
// SAST uses (rule_id, repo-relative path, normalized snippet) so line shifts don't
// break triage; osv uses (rule_id, package, version); DAST/ZAP uses (rule_id, url,
// param). Header analysis keeps the 2-part (rule_id, location) form via FindingHash.
func FindingHashParts(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// severityWeight is the per-finding score penalty by severity.
var severityWeight = map[Severity]int{
	SeverityCritical: 25,
	SeverityHigh:     15,
	SeverityMedium:   8,
	SeverityLow:      3,
	SeverityInfo:     1,
}

// severityCap caps the total penalty a single severity bucket can contribute, so
// a flood of same-severity findings can't instantly pin the score to 0 (design #6).
var severityCap = map[Severity]int{
	SeverityCritical: 40,
	SeverityHigh:     30,
	SeverityMedium:   20,
	SeverityLow:      8,
	SeverityInfo:     2,
}

// ComputeScore = max(0, 100 - Σ_bucket min(cap, weight×openCount)), counting only
// open findings (ignored/fixed excluded so triage is reflected). Bucket caps give
// gradation instead of an immediate floor at 0 under a real-engine finding flood.
func ComputeScore(findings []ScanFinding) int {
	counts := map[Severity]int{}
	for _, f := range findings {
		if f.Status != FindingOpen {
			continue // ignored/fixed don't penalize the score
		}
		counts[f.Severity]++
	}
	penalty := 0
	for sev, n := range counts {
		p := severityWeight[sev] * n
		if cap := severityCap[sev]; p > cap {
			p = cap
		}
		penalty += p
	}
	if penalty > 100 {
		penalty = 100
	}
	return 100 - penalty
}
