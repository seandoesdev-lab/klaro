package model

import (
	"crypto/sha256"
	"encoding/hex"
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
	Status     ScanStatus  `json:"status"`
	Score      *int        `json:"score,omitempty"`
	StartedAt  *time.Time  `json:"started_at,omitempty"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
}

// ScanFinding is a single security issue produced by a scan.
type ScanFinding struct {
	ID           string        `json:"id"`
	ScanID       string        `json:"scan_id"`
	RuleID       string        `json:"rule_id"`
	Severity     Severity      `json:"severity"`
	Title        string        `json:"title"`
	FilePath     *string       `json:"file_path,omitempty"`
	Line         *int          `json:"line,omitempty"`
	FindingHash  string        `json:"finding_hash"`
	Status       FindingStatus `json:"status"`
	IgnoreReason *string       `json:"ignore_reason,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
}

// ScanJob is the queue payload consumed by the scan worker.
type ScanJob struct {
	ScanID    string   `json:"scan_id"`
	OrgID     string   `json:"org_id"`
	ProjectID string   `json:"project_id"`
	Type      ScanType `json:"type"`
	TargetURL string   `json:"target_url"`
}

// FindingHash is a stable hash of rule_id + location, used to match a finding
// across re-scans so triage state (ignored/fixed) is preserved.
func FindingHash(ruleID, location string) string {
	sum := sha256.Sum256([]byte(ruleID + "|" + location))
	return hex.EncodeToString(sum[:])
}

// severityWeight is the score penalty for a finding of a given severity.
var severityWeight = map[Severity]int{
	SeverityCritical: 25,
	SeverityHigh:     15,
	SeverityMedium:   8,
	SeverityLow:      3,
	SeverityInfo:     1,
}

// ComputeScore starts at 100 and subtracts a penalty per finding by severity,
// with a floor of 0.
func ComputeScore(findings []ScanFinding) int {
	score := 100
	for _, f := range findings {
		score -= severityWeight[f.Severity]
	}
	if score < 0 {
		score = 0
	}
	return score
}
