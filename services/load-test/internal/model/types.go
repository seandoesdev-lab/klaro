package model

import (
	"encoding/json"
	"time"
)

type Step struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type Thresholds struct {
	HTTPReqDurationP95Ms int     `json:"http_req_duration_p95_ms,omitempty"`
	ErrorRate            float64 `json:"error_rate,omitempty"`
}

type Scenario struct {
	VU          int        `json:"vu"`
	DurationSec int        `json:"duration_sec"`
	RampUpSec   int        `json:"ramp_up_sec"`
	Steps       []Step     `json:"steps"`
	Thresholds  Thresholds `json:"thresholds"`
}

type VerifiedDomain struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"-"`
	Domain     string     `json:"domain"`
	Method     string     `json:"method"`
	Token      string     `json:"-"`
	Status     string     `json:"status"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
}

type LoadTest struct {
	ID            string     `json:"id"`
	OrgID         string     `json:"-"`
	ProjectID     string     `json:"-"`
	TargetURL     string     `json:"target_url"`
	Scenario      Scenario   `json:"scenario"`
	VU            int        `json:"vu"`
	DurationSec   int        `json:"duration_sec"`
	Status        Status     `json:"status"`
	AbortedReason *string    `json:"aborted_reason"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type LoadTestResult struct {
	RPSAvg                 float64 `json:"rps_avg"`
	LatencyP50             float64 `json:"latency_p50"`
	LatencyP95             float64 `json:"latency_p95"`
	LatencyP99             float64 `json:"latency_p99"`
	ErrorRate              float64 `json:"error_rate"`
	MaxVUBeforeDegradation int     `json:"max_vu_before_degradation"`
	BottleneckEndpoint     string  `json:"bottleneck_endpoint"`
	MetricsRef             string  `json:"-"`
}

type Job struct {
	LoadTestID string   `json:"load_test_id"`
	OrgID      string   `json:"org_id"`
	ProjectID  string   `json:"project_id"`
	TargetURL  string   `json:"target_url"`
	Scenario   Scenario `json:"scenario"`
}
