package model

import (
	"strings"
	"testing"
	"time"
)

func TestComputePerformanceScore(t *testing.T) {
	cases := []struct {
		name     string
		in       PerfInput
		min, max int
	}{
		{"no data", PerfInput{HasData: false}, 0, 0},
		{"healthy", PerfInput{HasData: true, LatencyP95: 120, ErrorRate: 0}, 100, 100},
		{"moderate p95", PerfInput{HasData: true, LatencyP95: 500, ErrorRate: 0}, 88, 92},
		{"slow + errors", PerfInput{HasData: true, LatencyP95: 1500, ErrorRate: 0.05}, 30, 60},
		{"terrible floors at 0", PerfInput{HasData: true, LatencyP95: 8000, ErrorRate: 0.5}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputePerformanceScore(tc.in)
			if got < tc.min || got > tc.max {
				t.Errorf("score=%d not in [%d,%d]", got, tc.min, tc.max)
			}
		})
	}
}

func TestComputeSecurityScore(t *testing.T) {
	seven := 42
	cases := []struct {
		name string
		in   SecInput
		want int
	}{
		{"no data", SecInput{HasData: false}, 0},
		{"clean", SecInput{HasData: true}, 100},
		{"prefers scan score", SecInput{HasData: true, Critical: 4, ScanScore: &seven}, 42},
		{"from counts", SecInput{HasData: true, Critical: 1, High: 1}, 60}, // 100-25-15
		{"floors at 0", SecInput{HasData: true, Critical: 10}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeSecurityScore(tc.in); got != tc.want {
				t.Errorf("score=%d want %d", got, tc.want)
			}
		})
	}
}

func TestBuildAISummaryContainsNumbers(t *testing.T) {
	perf := PerfInput{HasData: true, RPSAvg: 850, LatencyP95: 320, ErrorRate: 0.02, MaxVU: 500, Bottleneck: "/api/orders"}
	sec := SecInput{HasData: true, Critical: 1, High: 2}
	s := BuildAISummary(perf, sec)
	for _, want := range []string{"500 VU", "320ms", "/api/orders", "Critical 1", "High 2"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q\ngot: %s", want, s)
		}
	}
}

func TestBuildAISummaryNoData(t *testing.T) {
	s := BuildAISummary(PerfInput{}, SecInput{})
	if !strings.Contains(s, "성능 평가는 생략") || !strings.Contains(s, "보안 평가는 생략") {
		t.Errorf("expected skip notes, got: %s", s)
	}
}

func TestReportShareRoundTrip(t *testing.T) {
	h := HashReportPassword("s3cret")
	if !strings.Contains(h, "$") {
		t.Fatalf("expected salt$hash format, got %s", h)
	}
	if !VerifyReportPassword(h, "s3cret") {
		t.Error("correct password should verify")
	}
	if VerifyReportPassword(h, "wrong") {
		t.Error("wrong password must not verify")
	}
	if VerifyReportPassword("nodollar", "x") {
		t.Error("malformed hash must not verify")
	}
}

func TestNewSlugUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		s := NewSlug()
		if !strings.HasPrefix(s, "r_") {
			t.Fatalf("slug missing prefix: %s", s)
		}
		if seen[s] {
			t.Fatalf("duplicate slug: %s", s)
		}
		seen[s] = true
	}
}

func TestReportStatusValues(t *testing.T) {
	// guard against accidental enum drift
	if ReportGenerating != "generating" || ReportReady != "ready" || ReportFailed != "failed" {
		t.Error("report status enum values changed")
	}
	_ = time.Now
}
