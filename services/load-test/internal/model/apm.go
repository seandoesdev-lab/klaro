package model

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// AgentLanguage is the runtime an APM agent instruments.
type AgentLanguage string

const (
	AgentNodeJS     AgentLanguage = "nodejs"
	AgentSpringBoot AgentLanguage = "springboot"
	AgentFastAPI    AgentLanguage = "fastapi"
)

// ValidAgentLanguage reports whether l is a supported agent runtime.
func ValidAgentLanguage(l AgentLanguage) bool {
	return l == AgentNodeJS || l == AgentSpringBoot || l == AgentFastAPI
}

// ApmAgent is a registered instrumentation agent that ingests spans/logs.
type ApmAgent struct {
	ID          string        `json:"id"`
	ProjectID   string        `json:"-"`
	Language    AgentLanguage `json:"language"`
	IngestToken string        `json:"ingest_token,omitempty"`
	LastSeenAt  *time.Time    `json:"last_seen_at,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
}

// ApmSpan is a single trace span (MVP storage in Postgres, not Tempo).
type ApmSpan struct {
	ID           string    `json:"id,omitempty"`
	ProjectID    string    `json:"-"`
	Service      string    `json:"service"`
	TraceID      string    `json:"trace_id"`
	SpanID       string    `json:"span_id"`
	ParentSpanID *string   `json:"parent_span_id,omitempty"`
	Name         string    `json:"name"`
	DurationMs   float64   `json:"duration_ms"`
	Status       string    `json:"status"`
	Ts           time.Time `json:"ts"`
}

// ApmLog is a single structured log line (MVP storage in Postgres, not Loki).
type ApmLog struct {
	ID        string    `json:"id,omitempty"`
	ProjectID string    `json:"-"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Ts        time.Time `json:"ts"`
}

// FilterSlowSpans returns spans whose duration is >= minMs (pure, for testing
// the slow-trace boundary independently of SQL).
func FilterSlowSpans(spans []ApmSpan, minMs float64) []ApmSpan {
	out := make([]ApmSpan, 0, len(spans))
	for _, s := range spans {
		if s.DurationMs >= minMs {
			out = append(out, s)
		}
	}
	return out
}

// newHex returns n random bytes hex-encoded (2n chars).
func newHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewIngestToken mints an opaque agent ingest token.
func NewIngestToken() string { return "ingest_" + newHex(20) }
