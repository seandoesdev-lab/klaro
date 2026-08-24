package explorer

// Cross-signal correlation: one trace, the log lines written inside it, and the
// metrics of the services and hosts that produced it [OBS-03/04/05, APM-03].
//
// This is a join, and the join key differs by direction:
//
//   - trace -> logs is exact. A log line carries the trace_id of the span it
//     was written inside (sdk/SDK_CONTRACT.md section 12), so the lines belong
//     to the trace rather than merely overlapping it in time.
//   - trace -> metrics is approximate, and has to be. A metric series has no
//     trace id - a counter is not per-request - so the only honest join is "the
//     same service and host, over the same window". Each series is returned
//     with the scope it came from, so the approximation is visible rather than
//     implied.
//
// Everything runs through the existing per-signal readers, which is what makes
// the tenant enforcement, the plan retention clamp and the rollup fallback
// apply here too. A second read path would be a second place to forget them.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Labels the collector writes for the resource attributes that identify a
// process. The OTLP-to-Prometheus and OTLP-to-Loki conversions both normalise
// the dots to underscores, so one pair of names covers metrics and logs.
const (
	// ServiceLabel mirrors the resource attribute service.name.
	ServiceLabel = "service_name"
	// HostLabel mirrors service.instance.id, the SDK contract's host_ident.
	HostLabel = "service_instance_id"
)

// DefaultCorrelationMetrics is what a span detail view asks for when the caller
// names nothing: throughput, latency and saturation, which is the smallest set
// that can separate "this service was slow" from "this host was busy".
//
// A fixed default rather than a discovery call: listing an org's metric names
// costs a second round trip to answer a question the UI never asks, and a
// caller that wants something else passes it.
var DefaultCorrelationMetrics = []string{
	"http_server_duration_seconds",
	"http_server_requests_total",
	"process_cpu_utilization",
}

// Bounds. A correlated read fans out, so every dimension of the fan is capped:
// a 400-span trace across 30 hosts must not turn one request into 90 upstream
// queries.
const (
	// DefaultCorrelationPad widens the window around the trace. A line written
	// microseconds before the root span opened is still part of the story, and
	// a metric scrape lands on its own schedule rather than on the trace's.
	DefaultCorrelationPad = 30 * time.Second
	// MaxCorrelationPad stops the pad being used to read a whole day through an
	// endpoint whose window is meant to be one trace.
	MaxCorrelationPad = 15 * time.Minute
	// DefaultCorrelationLogs is the log page size; a single trace's own lines
	// are usually tens, not thousands.
	DefaultCorrelationLogs = 200
	// MaxCorrelationScopes caps how many service+host pairs get metric queries.
	// Scopes are sorted by time spent, so the ones dropped contributed least to
	// the latency being investigated.
	MaxCorrelationScopes = 4
	// MaxCorrelationQueries caps scopes x metrics.
	MaxCorrelationQueries = 12
	// defaultCorrelationStep resolves a minutes-long window without asking for
	// more points than a sparkline can draw.
	defaultCorrelationStep = 15 * time.Second
)

// CorrelateQuery is the structured form of GET /obs/traces/:traceId/correlated.
type CorrelateQuery struct {
	TraceID string
	// Pad widens the window on both sides of the trace.
	Pad time.Duration
	// LogLimit caps the correlated log lines.
	LogLimit int
	// Metrics are the metric names read per scope. Empty means
	// DefaultCorrelationMetrics.
	Metrics []string
	// Step is the metric resolution.
	Step time.Duration
}

// Validate checks the query and fills in the defaults.
func (q *CorrelateQuery) Validate(maxLimit int) error {
	if !ValidTraceID(q.TraceID) {
		return fmt.Errorf("%w: trace id must be 16-32 hex characters", ErrInvalidQuery)
	}
	if q.Pad < 0 {
		return fmt.Errorf("%w: pad_sec must not be negative", ErrInvalidQuery)
	}
	if q.Pad == 0 {
		q.Pad = DefaultCorrelationPad
	}
	if q.Pad > MaxCorrelationPad {
		return fmt.Errorf("%w: pad_sec must be at most %d", ErrInvalidQuery,
			int(MaxCorrelationPad.Seconds()))
	}
	if q.LogLimit <= 0 {
		q.LogLimit = DefaultCorrelationLogs
	}
	if maxLimit > 0 && q.LogLimit > maxLimit {
		q.LogLimit = maxLimit
	}
	if q.Step <= 0 {
		q.Step = defaultCorrelationStep
	}
	if len(q.Metrics) == 0 {
		q.Metrics = DefaultCorrelationMetrics
	}
	for _, m := range q.Metrics {
		// Validated here rather than left to QueryMetrics so a typo is one 422
		// naming the bad metric, not a half-filled answer with a note in it.
		if !ValidMetricName(m) {
			return fmt.Errorf("%w: %q is not a valid metric name", ErrInvalidQuery, m)
		}
	}
	return nil
}

// CorrelationScope is one service+host pair the trace touched, and how much of
// the trace it accounts for.
//
// Returned rather than kept internal because it is the explanation for the
// metric list: a caller can see these series were chosen because this host held
// the span that took three of the four seconds.
type CorrelationScope struct {
	Service string `json:"service"`
	Host    string `json:"host,omitempty"`
	Spans   int    `json:"spans"`
	// DurationMS is summed span duration - where the time went, not wall clock.
	// Concurrent siblings both count, which is the point: two hosts each busy
	// for the whole trace are two scopes worth looking at.
	DurationMS float64 `json:"duration_ms"`
	Errors     int     `json:"errors"`
}

// CorrelatedMetric is one metric read for one scope.
type CorrelatedMetric struct {
	// Key is unique inside a response so a client can address a chart without
	// matching on query text.
	Key     string   `json:"key"`
	Metric  string   `json:"metric"`
	Service string   `json:"service"`
	Host    string   `json:"host,omitempty"`
	Series  []Series `json:"series"`
	// Resolution says whether this came from raw samples or a rollup. A coarse
	// line must not be allowed to read as a calm system.
	Resolution string `json:"resolution"`
	// Query is the MetricsQL that ran, which is what makes the server-side org
	// matcher inspectable.
	Query string `json:"query"`
}

// Correlated is the joined answer.
type Correlated struct {
	TraceID string `json:"trace_id"`
	Spans   []Span `json:"spans"`
	// From and To are the padded window the logs and metrics were read over.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`

	Scopes []CorrelationScope `json:"scopes"`

	Logs []LogEntry `json:"logs"`
	// LogsQuery is the generated LogQL, empty when no log query ran.
	LogsQuery string `json:"logs_query,omitempty"`

	Metrics []CorrelatedMetric `json:"metrics"`

	// Notes explain anything missing. A correlated view with spans and metrics
	// but no logs is useful; the same view silently missing its logs is a lie,
	// so a signal that could not be read says so here rather than coming back
	// as an empty list.
	Notes []string `json:"notes,omitempty"`
}

// Correlate joins one trace to its logs and to its services' metrics.
//
// The trace is read first and a failure there is fatal: with no spans there is
// no window, no scope and nothing to correlate, so a missing trace is a 404
// rather than an empty envelope. Logs and metrics are best effort - one
// unavailable backend must not blank the two that are working.
func (c *Client) Correlate(ctx context.Context, orgID string, q CorrelateQuery) (Correlated, error) {
	if err := q.Validate(c.cfg.MaxLimit); err != nil {
		return Correlated{}, err
	}

	// GetTrace carries the org enforcement for this call: its Tempo tenant
	// header comes from the mapper, so another org's trace id is a miss here
	// and this returns before any log or metric query is built.
	trace, err := c.GetTrace(ctx, orgID, q.TraceID)
	if err != nil {
		return Correlated{}, err
	}

	from, to := traceWindow(trace.Spans, q.Pad)
	out := Correlated{
		TraceID: trace.TraceID,
		Spans:   trace.Spans,
		From:    from,
		To:      to,
		Scopes:  scopesOf(trace.Spans),
		Logs:    []LogEntry{},
		Metrics: []CorrelatedMetric{},
	}
	rng := TimeRange{From: from, To: to}

	c.correlateLogs(ctx, orgID, q, rng, &out)
	c.correlateMetrics(ctx, orgID, q, rng, &out)
	return out, nil
}

// correlateLogs fills in the exact half of the join.
func (c *Client) correlateLogs(ctx context.Context, orgID string, q CorrelateQuery,
	rng TimeRange, out *Correlated) {

	if !c.cfg.Configured(c.cfg.LokiURL) {
		out.Notes = append(out.Notes, "logs backend is not configured in this deployment")
		return
	}
	page, err := c.QueryLogs(ctx, orgID, LogsQuery{
		Range: rng, TraceID: q.TraceID, Limit: q.LogLimit,
	})
	if err != nil {
		out.Notes = append(out.Notes, noteFor("correlated logs", err))
		return
	}
	out.Logs = page.Data
	out.LogsQuery = page.Query
	if len(page.Data) == 0 {
		// By far the most common cause is an application whose logger does not
		// attach the trace context, and that looks exactly like "nothing was
		// logged" unless it is named.
		out.Notes = append(out.Notes,
			"no log line carries this trace id: check that the service attaches "+
				"trace_id to its logs (SDK_CONTRACT section 12)")
	}
}

// correlateMetrics fills in the approximate half, one query per scope+metric.
func (c *Client) correlateMetrics(ctx context.Context, orgID string, q CorrelateQuery,
	rng TimeRange, out *Correlated) {

	if !c.cfg.Configured(c.cfg.VMSelectURL) {
		out.Notes = append(out.Notes, "metrics backend is not configured in this deployment")
		return
	}

	queries := 0
	truncated := false
	for _, scope := range out.Scopes {
		for _, metric := range q.Metrics {
			if queries >= MaxCorrelationQueries {
				truncated = true
				break
			}
			queries++

			filters := []Matcher{{Label: ServiceLabel, Value: scope.Service}}
			if scope.Host != "" {
				filters = append(filters, Matcher{Label: HostLabel, Value: scope.Host})
			}
			res, err := c.QueryMetrics(ctx, orgID, MetricsQuery{
				Range: rng, Metric: metric, Filters: filters, Step: q.Step,
			})
			if err != nil {
				out.Notes = append(out.Notes,
					noteFor("metric "+metric+" for "+scope.Service, err))
				continue
			}
			out.Metrics = append(out.Metrics, CorrelatedMetric{
				Key:        metricKey(metric, scope),
				Metric:     metric,
				Service:    scope.Service,
				Host:       scope.Host,
				Series:     res.Series,
				Resolution: res.Resolution,
				Query:      res.Query,
			})
		}
		if truncated {
			break
		}
	}
	if truncated {
		// A cap that is not reported reads as "this is everything there was".
		out.Notes = append(out.Notes,
			fmt.Sprintf("metric correlation stopped at the %d query limit; "+
				"narrow the metric list to cover more services",
				MaxCorrelationQueries))
	}
}

// metricKey names one chart. Service and host are part of it because the same
// metric is read once per scope, and two charts sharing a key would be
// indistinguishable to a client that keys off it.
func metricKey(metric string, scope CorrelationScope) string {
	key := metric + "@" + scope.Service
	if scope.Host != "" {
		key += "/" + scope.Host
	}
	return key
}

// traceWindow is the extent of the trace, padded.
//
// The end comes from start+duration per span rather than from the root: a child
// outliving its parent is a real shape (a fire-and-forget publish), and cutting
// the window at the root would hide that child's logs.
func traceWindow(spans []Span, pad time.Duration) (time.Time, time.Time) {
	if len(spans) == 0 {
		now := time.Now()
		return now.Add(-pad), now.Add(pad)
	}
	first, last := spans[0].Start, int64(0)
	for _, s := range spans {
		if s.Start < first {
			first = s.Start
		}
		if end := s.Start + int64(s.DurationMS); end > last {
			last = end
		}
	}
	return time.UnixMilli(first).Add(-pad), time.UnixMilli(last).Add(pad)
}

// scopesOf groups the spans by the process that produced them.
//
// Sorted by time spent descending, so truncating at MaxCorrelationScopes drops
// the processes that contributed least to the trace rather than whichever ones
// the backend happened to list last.
func scopesOf(spans []Span) []CorrelationScope {
	index := map[string]*CorrelationScope{}
	var order []string
	for _, s := range spans {
		if s.Service == "" && s.Host == "" {
			// Nothing to join on. A metric query with neither label would be
			// "every series this org has", which QueryMetrics refuses anyway.
			continue
		}
		key := s.Service + "\x00" + s.Host
		scope, ok := index[key]
		if !ok {
			scope = &CorrelationScope{Service: s.Service, Host: s.Host}
			index[key] = scope
			order = append(order, key)
		}
		scope.Spans++
		scope.DurationMS += s.DurationMS
		if s.Status == "error" {
			scope.Errors++
		}
	}

	out := make([]CorrelationScope, 0, len(order))
	for _, key := range order {
		out = append(out, *index[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DurationMS != out[j].DurationMS {
			return out[i].DurationMS > out[j].DurationMS
		}
		return out[i].Service < out[j].Service
	})
	if len(out) > MaxCorrelationScopes {
		out = out[:MaxCorrelationScopes]
	}
	return out
}

// noteFor classifies a failure for a caller-facing note.
//
// The error text never travels. getJSON folds a snippet of the upstream body
// into ErrBackend so the server log can say what happened, and that snippet can
// name another tenant's labels or an internal hostname.
func noteFor(what string, err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return what + " was not found in this org"
	case errors.Is(err, ErrInvalidQuery):
		return what + " could not be queried: the generated query was rejected"
	default:
		return what + " is unavailable: the telemetry backend did not answer"
	}
}
