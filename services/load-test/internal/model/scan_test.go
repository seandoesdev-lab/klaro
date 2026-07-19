package model

import "testing"

func TestComputeScore(t *testing.T) {
	cases := []struct {
		name     string
		findings []ScanFinding
		want     int
	}{
		{"no findings", nil, 100},
		{"one high", []ScanFinding{{Severity: SeverityHigh}}, 85},
		{
			"mixed",
			[]ScanFinding{
				{Severity: SeverityHigh},   // -15
				{Severity: SeverityMedium}, // -8
				{Severity: SeverityLow},    // -3
				{Severity: SeverityInfo},   // -1
			},
			73,
		},
		{
			"floor at zero",
			[]ScanFinding{
				{Severity: SeverityCritical}, {Severity: SeverityCritical},
				{Severity: SeverityCritical}, {Severity: SeverityCritical},
				{Severity: SeverityCritical}, // 5 * -25 = -125
			},
			0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeScore(tc.findings); got != tc.want {
				t.Errorf("ComputeScore = %d want %d", got, tc.want)
			}
		})
	}
}

func TestFindingHashStable(t *testing.T) {
	a := FindingHash("dast.missing-hsts", "https://x.example.com")
	b := FindingHash("dast.missing-hsts", "https://x.example.com")
	if a != b {
		t.Errorf("hash not stable: %s vs %s", a, b)
	}
	if a == FindingHash("dast.missing-hsts", "https://y.example.com") {
		t.Errorf("hash should differ by location")
	}
	if a == FindingHash("dast.missing-csp", "https://x.example.com") {
		t.Errorf("hash should differ by rule_id")
	}
	if len(a) != 64 {
		t.Errorf("expected 64-hex sha256, got len %d", len(a))
	}
}

func TestCanScanTransition(t *testing.T) {
	cases := []struct {
		from, to ScanStatus
		ok       bool
	}{
		{ScanStatusPending, ScanStatusRunning, true},
		{ScanStatusPending, ScanStatusFailed, true},
		{ScanStatusRunning, ScanStatusCompleted, true},
		{ScanStatusRunning, ScanStatusFailed, true},
		{ScanStatusCompleted, ScanStatusRunning, false},
		{ScanStatusPending, ScanStatusCompleted, false},
		{ScanStatusFailed, ScanStatusRunning, false},
	}
	for _, c := range cases {
		if got := CanScanTransition(c.from, c.to); got != c.ok {
			t.Errorf("CanScanTransition(%s,%s)=%v want %v", c.from, c.to, got, c.ok)
		}
	}
}
