package snapshot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/klaro/observability/internal/explorer"
)

const orgA = "00000000-0000-0000-0000-0000000000aa"

// fakeReader stands in for the Explorer and records what it was asked.
type fakeReader struct {
	metrics    explorer.MetricsResult
	metricsErr error
	traces     explorer.TracesResult
	tracesErr  error

	metricCalls []explorer.MetricsQuery
	traceCalls  []explorer.TracesQuery
	orgs        []string
}

func (f *fakeReader) QueryMetrics(_ context.Context, orgID string, q explorer.MetricsQuery) (explorer.MetricsResult, error) {
	f.metricCalls = append(f.metricCalls, q)
	f.orgs = append(f.orgs, orgID)
	return f.metrics, f.metricsErr
}

func (f *fakeReader) SearchTraces(_ context.Context, orgID string, q explorer.TracesQuery) (explorer.TracesResult, error) {
	f.traceCalls = append(f.traceCalls, q)
	f.orgs = append(f.orgs, orgID)
	return f.traces, f.tracesErr
}

func window() (time.Time, time.Time) {
	to := time.Date(2026, 8, 23, 14, 9, 0, 0, time.UTC)
	return to.Add(-7 * time.Minute), to
}

func request() Request {
	from, to := window()
	return Request{
		From: from, To: to,
		Metrics: []MetricQuery{{Key: "latency", Metric: "http_server_duration", Agg: "p95", StepSec: 15}},
		Traces:  &TraceQuery{Service: "checkout", MinDurationMS: 3000},
	}
}

// The window a report asks for is the window the Explorer is asked for: a
// snapshot must not quietly widen or shift it.
func TestTakeDelegatesTheExactWindow(t *testing.T) {
	from, to := window()
	reader := &fakeReader{
		metrics: explorer.MetricsResult{Resolution: explorer.ResolutionRaw, Query: "rendered"},
		traces:  explorer.TracesResult{Data: []explorer.TraceSummary{{TraceID: "abc"}}},
	}

	out, err := New(reader).Take(context.Background(), orgA, request())
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.metricCalls) != 1 || len(reader.traceCalls) != 1 {
		t.Fatalf("delegation = %d metric and %d trace calls", len(reader.metricCalls), len(reader.traceCalls))
	}
	if !reader.metricCalls[0].Range.From.Equal(from) || !reader.metricCalls[0].Range.To.Equal(to) {
		t.Errorf("metric window = %v..%v, want the requested one", reader.metricCalls[0].Range.From, reader.metricCalls[0].Range.To)
	}
	if !reader.traceCalls[0].Range.From.Equal(from) {
		t.Errorf("trace window = %v, want the requested one", reader.traceCalls[0].Range.From)
	}
	if reader.metricCalls[0].Step != 15*time.Second {
		t.Errorf("step = %v, want the requested 15s", reader.metricCalls[0].Step)
	}
	// Every read is on behalf of one org, and it is the one the caller named.
	for _, org := range reader.orgs {
		if org != orgA {
			t.Errorf("a read ran for org %q", org)
		}
	}
	if len(out.Metrics) != 1 || out.Metrics[0].Key != "latency" {
		t.Errorf("result keys = %+v", out.Metrics)
	}
	if out.Metrics[0].Query != "rendered" {
		t.Errorf("the generated query was not carried into the report: %q", out.Metrics[0].Query)
	}
	if len(out.Traces) != 1 {
		t.Errorf("traces = %+v", out.Traces)
	}
}

// A report with four of five charts is useful; one with an error page is not.
func TestOneFailingQueryDoesNotFailTheSnapshot(t *testing.T) {
	reader := &fakeReader{metricsErr: errors.New("vmselect unreachable")}
	req := request()
	req.Traces = nil

	out, err := New(reader).Take(context.Background(), orgA, req)
	if err != nil {
		t.Fatalf("the snapshot failed instead of noting the gap: %v", err)
	}
	if len(out.Metrics) != 0 {
		t.Errorf("metrics = %+v, want none", out.Metrics)
	}
	if len(out.Notes) == 0 || !strings.Contains(out.Notes[0], "latency") {
		t.Errorf("notes = %v, want the failing key named", out.Notes)
	}
}

// A window narrowed to the plan has to be reported as partial. Drawing a shorter
// line and saying nothing lets a reader assume the rest was quiet.
func TestClampedWindowIsReportedAsPartial(t *testing.T) {
	reader := &fakeReader{metrics: explorer.MetricsResult{Clamped: true, Resolution: explorer.ResolutionRaw}}
	req := request()
	req.Traces = nil

	out, err := New(reader).Take(context.Background(), orgA, req)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Partial {
		t.Error("a clamped window was not reported as partial")
	}
	if len(out.Notes) == 0 {
		t.Error("no note explained the narrowing")
	}
}

// A rollup answer must say so: a coarse line presented as raw reads as calm.
func TestRollupResolutionIsNoted(t *testing.T) {
	reader := &fakeReader{metrics: explorer.MetricsResult{Resolution: explorer.Resolution1h}}
	req := request()
	req.Traces = nil

	out, err := New(reader).Take(context.Background(), orgA, req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Metrics[0].Resolution != explorer.Resolution1h {
		t.Errorf("resolution = %q", out.Metrics[0].Resolution)
	}
	var noted bool
	for _, n := range out.Notes {
		if strings.Contains(n, "rollup") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("notes = %v, want the rollup called out", out.Notes)
	}
}

func TestValidateRejectsBadRequests(t *testing.T) {
	from, to := window()
	cases := map[string]Request{
		"no window":       {Metrics: []MetricQuery{{Key: "k", Metric: "cpu"}}},
		"reversed window": {From: to, To: from, Metrics: []MetricQuery{{Key: "k", Metric: "cpu"}}},
		"window too wide": {From: to.Add(-MaxWindow - time.Hour), To: to,
			Metrics: []MetricQuery{{Key: "k", Metric: "cpu"}}},
		"nothing asked": {From: from, To: to},
		"no key":        {From: from, To: to, Metrics: []MetricQuery{{Metric: "cpu"}}},
		"duplicate key": {From: from, To: to,
			Metrics: []MetricQuery{{Key: "k", Metric: "cpu"}, {Key: "k", Metric: "mem"}}},
		// Without a metric or a filter the only matcher left is the injected org
		// one, and the query reads everything the tenant ever wrote.
		"unbounded query": {From: from, To: to, Metrics: []MetricQuery{{Key: "k"}}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if err := req.Validate(); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// An invalid request must not reach a backend.
func TestInvalidRequestTouchesNothing(t *testing.T) {
	reader := &fakeReader{}
	if _, err := New(reader).Take(context.Background(), orgA, Request{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if len(reader.metricCalls) != 0 || len(reader.traceCalls) != 0 {
		t.Error("an invalid request reached the Explorer")
	}
}

// A traces-only snapshot is a valid report section.
func TestTracesOnlySnapshot(t *testing.T) {
	from, to := window()
	reader := &fakeReader{traces: explorer.TracesResult{Data: []explorer.TraceSummary{{TraceID: "abc"}}}}

	out, err := New(reader).Take(context.Background(), orgA,
		Request{From: from, To: to, Traces: &TraceQuery{Service: "checkout"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Traces) != 1 || len(out.Metrics) != 0 {
		t.Errorf("snapshot = %+v", out)
	}
	// A default limit is applied so a report cannot ask for every trace in the
	// window by omission.
	if reader.traceCalls[0].Limit != defaultTraces {
		t.Errorf("limit = %d, want the default %d", reader.traceCalls[0].Limit, defaultTraces)
	}
}
