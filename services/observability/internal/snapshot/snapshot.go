// Package snapshot serves the deployment-report path (OBS-10, design 4.6).
//
// A report (S4) wants "what did this service look like between 14:02 and
// 14:09". This package answers that by delegating to the Explorer - the same
// read path a dashboard uses - and returning the result. It owns nothing and
// stores nothing.
//
// That is the whole design, and it is deliberate in two directions:
//
//   - Reading through the Explorer means the org tenant enforcement, the plan
//     retention clamp and the rollup fallback all apply here too. A second read
//     path would be a second place to forget them.
//   - Storing nothing means a report cannot become the owner of telemetry.
//     Continuous collection, storage and retention carry on whether a report
//     succeeds, fails or is never run: the report consumes a window, it is not
//     the reason the data exists. Coupling them would mean a deleted report
//     taking observability data with it, or a failed report keeping data alive
//     past its plan.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/klaro/observability/internal/explorer"
)

// ErrInvalidRequest is a caller mistake in the snapshot request.
var ErrInvalidRequest = errors.New("invalid snapshot request")

// Bounds. A report window is minutes to hours, and a snapshot is served
// synchronously to a waiting job.
const (
	MaxWindow        = 24 * time.Hour
	MaxMetricQueries = 20
	defaultStep      = time.Minute
	defaultTraces    = 20
)

// MetricQuery is one series a report wants over the window.
type MetricQuery struct {
	// Key labels the result so the report can find it again without matching on
	// query text.
	Key     string             `json:"key"`
	Metric  string             `json:"metric"`
	Filters []explorer.Matcher `json:"filters,omitempty"`
	Agg     string             `json:"agg,omitempty"`
	StepSec int                `json:"step_sec,omitempty"`
}

// TraceQuery selects the slow traces a report highlights. The threshold comes
// from the caller: what counts as slow is a property of the service under test.
type TraceQuery struct {
	Service       string `json:"service,omitempty"`
	MinDurationMS int    `json:"min_duration_ms,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

// Request is what a report asks for.
type Request struct {
	// From and To bound the window - typically a load test run.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`

	Metrics []MetricQuery `json:"metrics,omitempty"`
	// Traces is optional; a report with no trace section omits it.
	Traces *TraceQuery `json:"traces,omitempty"`
}

// MetricResult is one query's answer.
type MetricResult struct {
	Key    string            `json:"key"`
	Series []explorer.Series `json:"series"`
	// Resolution says whether this came from raw samples or a rollup. A report
	// printing a chart has to say which, or a coarse line reads as a calm system.
	Resolution string `json:"resolution"`
	// Query is the MetricsQL the Explorer generated, for the report's appendix.
	Query string `json:"query"`
}

// Snapshot is the answer. It is a value, not a stored object: nothing here is
// persisted, and asking again re-reads the backends.
type Snapshot struct {
	OrgID string    `json:"org_id"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`

	Metrics []MetricResult          `json:"metrics"`
	Traces  []explorer.TraceSummary `json:"traces,omitempty"`

	// Partial reports that the requested window reached past what the org's plan
	// keeps, so the snapshot covers less than was asked for. A report must say
	// "we could only see the last N days" rather than draw a shorter line and
	// let a reader assume the rest was quiet.
	Partial bool `json:"partial"`
	// Notes carries anything a report should print alongside the numbers.
	Notes []string `json:"notes,omitempty"`
}

// Reader is the Explorer surface this package uses. An interface, so the adapter
// is testable without three storage backends - and so it stays obvious that
// snapshots have no read path of their own.
type Reader interface {
	QueryMetrics(ctx context.Context, orgID string, q explorer.MetricsQuery) (explorer.MetricsResult, error)
	SearchTraces(ctx context.Context, orgID string, q explorer.TracesQuery) (explorer.TracesResult, error)
}

// Adapter answers snapshot requests.
type Adapter struct{ reader Reader }

// New builds an Adapter over an Explorer client.
func New(reader Reader) *Adapter { return &Adapter{reader: reader} }

// Validate checks a request before any backend is touched.
func (r *Request) Validate() error {
	if r.From.IsZero() || r.To.IsZero() {
		return fmt.Errorf("%w: from and to are required", ErrInvalidRequest)
	}
	if !r.To.After(r.From) {
		return fmt.Errorf("%w: to must be after from", ErrInvalidRequest)
	}
	if r.To.Sub(r.From) > MaxWindow {
		return fmt.Errorf("%w: window must be at most %s", ErrInvalidRequest, MaxWindow)
	}
	if len(r.Metrics) == 0 && r.Traces == nil {
		return fmt.Errorf("%w: ask for at least one metric query or a trace section", ErrInvalidRequest)
	}
	if len(r.Metrics) > MaxMetricQueries {
		return fmt.Errorf("%w: at most %d metric queries", ErrInvalidRequest, MaxMetricQueries)
	}
	keys := make(map[string]bool, len(r.Metrics))
	for i := range r.Metrics {
		q := &r.Metrics[i]
		if q.Key == "" {
			return fmt.Errorf("%w: metric query %d has no key", ErrInvalidRequest, i)
		}
		if keys[q.Key] {
			return fmt.Errorf("%w: duplicate metric key %q", ErrInvalidRequest, q.Key)
		}
		keys[q.Key] = true
		if q.Metric == "" && len(q.Filters) == 0 {
			return fmt.Errorf("%w: metric query %q needs a metric or a filter", ErrInvalidRequest, q.Key)
		}
	}
	return nil
}

// Take reads the window.
//
// A failing metric query does not fail the snapshot: a deployment report with
// four of five charts is useful, one with an error page is not. What went wrong
// goes into Notes so the report can say so instead of showing a gap.
func (a *Adapter) Take(ctx context.Context, orgID string, req Request) (Snapshot, error) {
	if err := req.Validate(); err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{OrgID: orgID, From: req.From, To: req.To, Metrics: []MetricResult{}}

	for _, q := range req.Metrics {
		step := time.Duration(q.StepSec) * time.Second
		if step <= 0 {
			step = defaultStep
		}
		res, err := a.reader.QueryMetrics(ctx, orgID, explorer.MetricsQuery{
			Range:   explorer.TimeRange{From: req.From, To: req.To},
			Metric:  q.Metric,
			Filters: q.Filters,
			Agg:     q.Agg,
			Step:    step,
		})
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("metric %q unavailable: %v", q.Key, err))
			continue
		}
		if res.Clamped {
			// The Explorer narrowed the window to the plan. Surfacing it is the
			// difference between a short chart and a misleading one.
			out.Partial = true
			out.Notes = append(out.Notes,
				fmt.Sprintf("metric %q was narrowed to the plan retention window", q.Key))
		}
		if res.Resolution != "" && res.Resolution != explorer.ResolutionRaw {
			out.Notes = append(out.Notes,
				fmt.Sprintf("metric %q came from the %s rollup, not raw samples", q.Key, res.Resolution))
		}
		out.Metrics = append(out.Metrics, MetricResult{
			Key: q.Key, Series: res.Series, Resolution: res.Resolution, Query: res.Query,
		})
	}

	if req.Traces != nil {
		limit := req.Traces.Limit
		if limit <= 0 {
			limit = defaultTraces
		}
		res, err := a.reader.SearchTraces(ctx, orgID, explorer.TracesQuery{
			Range:       explorer.TimeRange{From: req.From, To: req.To},
			Service:     req.Traces.Service,
			MinDuration: time.Duration(req.Traces.MinDurationMS) * time.Millisecond,
			Limit:       limit,
		})
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("traces unavailable: %v", err))
		} else {
			out.Traces = res.Data
		}
	}
	return out, nil
}
