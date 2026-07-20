package model

import "testing"

func TestComputeScore(t *testing.T) {
	open := func(s Severity) ScanFinding { return ScanFinding{Severity: s, Status: FindingOpen} }
	cases := []struct {
		name     string
		findings []ScanFinding
		want     int
	}{
		{"no findings", nil, 100},
		{"one open high", []ScanFinding{open(SeverityHigh)}, 85}, // 100 - 15
		{
			"mixed under caps",
			[]ScanFinding{open(SeverityHigh), open(SeverityMedium), open(SeverityLow), open(SeverityInfo)},
			73, // 100 - (15+8+3+1)
		},
		{
			// 5 open criticals: bucket weight 5*25=125 capped at 40 → 100-40=60.
			// (old unbounded formula pinned this to 0; bucket caps restore gradation)
			"critical bucket capped",
			[]ScanFinding{
				open(SeverityCritical), open(SeverityCritical), open(SeverityCritical),
				open(SeverityCritical), open(SeverityCritical),
			},
			60,
		},
		{
			// All buckets maxed out reaches the floor: 40+30+20+8+2 = 100.
			"floor at zero when every bucket saturates",
			[]ScanFinding{
				open(SeverityCritical), open(SeverityCritical), // cap 40 (2*25=50→40)
				open(SeverityHigh), open(SeverityHigh), open(SeverityHigh), // cap 30 (3*15=45→30)
				open(SeverityMedium), open(SeverityMedium), open(SeverityMedium), // 24→cap 20
				open(SeverityLow), open(SeverityLow), open(SeverityLow), // 9→cap 8
				open(SeverityInfo), open(SeverityInfo), open(SeverityInfo), // 3→cap 2
			},
			0,
		},
		{
			// ignored/fixed findings don't penalize; only the open high counts.
			"ignored and fixed excluded",
			[]ScanFinding{
				open(SeverityHigh),
				{Severity: SeverityCritical, Status: FindingIgnored},
				{Severity: SeverityCritical, Status: FindingFixed},
			},
			85,
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

func TestFindingHashParts(t *testing.T) {
	a := FindingHashParts("sast.semgrep.rule", "internal/db.go", "x := md5.New()")
	b := FindingHashParts("sast.semgrep.rule", "internal/db.go", "x := md5.New()")
	if a != b {
		t.Errorf("hash not stable")
	}
	if len(a) != 64 {
		t.Errorf("expected 64-hex sha256, got len %d", len(a))
	}
	// FindingHash is FindingHashParts with two parts.
	if FindingHash("r", "loc") != FindingHashParts("r", "loc") {
		t.Errorf("FindingHash must equal 2-part FindingHashParts")
	}
	if a == FindingHashParts("sast.semgrep.rule", "internal/db.go", "y := sha1.New()") {
		t.Errorf("hash should differ by snippet")
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
