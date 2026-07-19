package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

// ReportStatus is the lifecycle of a generated report.
type ReportStatus string

const (
	ReportGenerating ReportStatus = "generating"
	ReportReady      ReportStatus = "ready"
	ReportFailed     ReportStatus = "failed"
)

// Report aggregates a load test and/or a security scan into scored view.
type Report struct {
	ID               string       `json:"id"`
	ProjectID        string       `json:"-"`
	LoadTestID       *string      `json:"load_test_id,omitempty"`
	ScanID           *string      `json:"scan_id,omitempty"`
	PerformanceScore int          `json:"performance_score"`
	SecurityScore    int          `json:"security_score"`
	AISummary        string       `json:"ai_summary"`
	PDFURL           *string      `json:"pdf_url,omitempty"`
	Status           ReportStatus `json:"status"`
	CreatedAt        time.Time    `json:"created_at"`
}

// ReportShare is a public share link for a report.
type ReportShare struct {
	ID           string     `json:"id"`
	ReportID     string     `json:"-"`
	Slug         string     `json:"slug"`
	PasswordHash *string    `json:"-"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RevokedAt    *time.Time `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
}

// PerfInput is the load-test slice of report inputs.
type PerfInput struct {
	HasData    bool
	RPSAvg     float64
	LatencyP95 float64 // ms
	ErrorRate  float64 // fraction 0..1
	MaxVU      int
	Bottleneck string
}

// SecInput is the security-scan slice of report inputs. If ScanScore is set it
// wins; otherwise a score is derived from severity counts.
type SecInput struct {
	HasData   bool
	Critical  int
	High      int
	Medium    int
	Low       int
	Info      int
	ScanScore *int
}

// ComputePerformanceScore maps error rate + p95 latency onto a 0..100 score.
// 0 when there is no load-test data to score.
func ComputePerformanceScore(in PerfInput) int {
	if !in.HasData {
		return 0
	}
	score := 100.0
	// Error rate dominates: each 1% costs 3 points.
	score -= in.ErrorRate * 100 * 3
	// p95 latency thresholds (ms).
	switch {
	case in.LatencyP95 <= 200:
		// excellent, no penalty
	case in.LatencyP95 <= 500:
		score -= (in.LatencyP95 - 200) / 30 // up to -10
	case in.LatencyP95 <= 1000:
		score -= 10 + (in.LatencyP95-500)/25 // up to -30
	default:
		score -= 30 + (in.LatencyP95-1000)/100
	}
	return clamp(score)
}

// ComputeSecurityScore prefers an existing scan score, else derives from
// severity counts using the same weights as ComputeScore.
func ComputeSecurityScore(in SecInput) int {
	if !in.HasData {
		return 0
	}
	if in.ScanScore != nil {
		return clampInt(*in.ScanScore)
	}
	score := 100 -
		in.Critical*severityWeight[SeverityCritical] -
		in.High*severityWeight[SeverityHigh] -
		in.Medium*severityWeight[SeverityMedium] -
		in.Low*severityWeight[SeverityLow] -
		in.Info*severityWeight[SeverityInfo]
	return clampInt(score)
}

// BuildAISummary produces a MOCK Korean natural-language summary templated from
// the real numbers. No external API is called (cost gate: 04-cost-model).
func BuildAISummary(perf PerfInput, sec SecInput) string {
	var b strings.Builder
	if perf.HasData {
		rating := "안정적이며"
		switch {
		case perf.ErrorRate > 0.05 || perf.LatencyP95 > 1000:
			rating = "불안정하며"
		case perf.ErrorRate > 0.01 || perf.LatencyP95 > 500:
			rating = "주의가 필요하며"
		}
		fmt.Fprintf(&b,
			"동시접속 최대 %d VU까지 %s 평균 %.0f RPS를 처리했습니다. P95 지연은 %.0fms, 에러율은 %.2f%%입니다.",
			perf.MaxVU, rating, perf.RPSAvg, perf.LatencyP95, perf.ErrorRate*100)
		if perf.Bottleneck != "" {
			fmt.Fprintf(&b, " 주요 병목 지점은 %s 엔드포인트입니다.", perf.Bottleneck)
		}
	} else {
		b.WriteString("부하 테스트 데이터가 없어 성능 평가는 생략되었습니다.")
	}

	b.WriteString(" ")

	if sec.HasData {
		total := sec.Critical + sec.High + sec.Medium + sec.Low + sec.Info
		if total == 0 {
			b.WriteString("보안 스캔에서 발견된 취약점은 없습니다.")
		} else {
			fmt.Fprintf(&b,
				"보안 측면에서는 Critical %d건, High %d건, Medium %d건, Low %d건이 발견되었습니다.",
				sec.Critical, sec.High, sec.Medium, sec.Low)
			if sec.Critical > 0 || sec.High > 0 {
				b.WriteString(" 심각도 높은 항목은 우선 조치를 권장합니다.")
			}
		}
	} else {
		b.WriteString("보안 스캔 데이터가 없어 보안 평가는 생략되었습니다.")
	}
	return b.String()
}

func clamp(f float64) int { return clampInt(int(math.Round(f))) }

func clampInt(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

// ── share helpers ────────────────────────────────────────────────────────────

// NewSlug mints a short, URL-safe share slug (e.g. "r_ab12cd").
func NewSlug() string { return "r_" + newHex(4) }

// HashReportPassword returns "salt$sha256hex(salt|pw)". MVP-simple: sha256+salt,
// no bcrypt (keeps the dependency surface flat; note in manifest).
func HashReportPassword(pw string) string {
	salt := newHex(8)
	return salt + "$" + sha256Hex(salt+"|"+pw)
}

// VerifyReportPassword checks pw against a "salt$hash" string.
func VerifyReportPassword(stored, pw string) bool {
	i := strings.IndexByte(stored, '$')
	if i < 0 {
		return false
	}
	salt, want := stored[:i], stored[i+1:]
	return sha256Hex(salt+"|"+pw) == want
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
