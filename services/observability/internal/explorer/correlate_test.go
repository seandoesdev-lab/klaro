package explorer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/klaro/observability/internal/tenants"
)

const correlatedTraceID = "5b8efff798038103d269b633813fc60c"

// A trace across two services on two hosts, with the erroring span on the
// second and most of the wall clock on the first.
const correlateTempoBody = `{"batches":[
 {"resource":{"attributes":[
   {"key":"service.name","value":{"stringValue":"web-bff"}},
   {"key":"service.instance.id","value":{"stringValue":"pod:aaaa1111"}}]},
  "scopeSpans":[{"spans":[
    {"spanId":"aaaa","name":"GET /cart",
     "startTimeUnixNano":"1756000000000000000","endTimeUnixNano":"1756000004000000000",
     "status":{}}]}]},
 {"resource":{"attributes":[
   {"key":"service.name","value":{"stringValue":"payments"}},
   {"key":"service.instance.id","value":{"stringValue":"pod:bbbb2222"}}]},
  "scopeSpans":[{"spans":[
    {"spanId":"bbbb","parentSpanId":"aaaa","name":"POST psp.authorize",
     "startTimeUnixNano":"1756000000500000000","endTimeUnixNano":"1756000003900000000",
     "status":{"code":"STATUS_CODE_ERROR"}}]}]}]}`

// A Loki page whose values carry the third element - structured metadata - which
// is where the correlation keys live.
const correlateLokiBody = `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"service_name":"payments","klaro_org_id":"00000000-0000-0000-0000-0000000000aa"},
  "values":[
   ["1756000003000000000","psp timeout after 3000ms",
    {"trace_id":"5b8efff798038103d269b633813fc60c","span_id":"bbbb","severity_text":"ERROR","deployment_env":"prod"}],
   ["1756000000600000000","authorizing",
    {"trace_id":"5b8efff798038103d269b633813fc60c","span_id":"bbbb"}]]}]}}`

const correlateVMBody = `{"status":"success","data":{"resultType":"matrix","result":[
 {"metric":{"__name__":"process_cpu_utilization","service_name":"payments","vm_account_id":"7"},
  "values":[[1756000000,"0.81"]]}]}}`

// recorder keeps every upstream request, which is where the tenant enforcement
// has to be visible.
type recorder struct {
	tempo, loki, vm []url.Values
	scopes          []string
	paths           []string
}

// correlateBackends stands in for Tempo, Loki and vmselect on one server, so a
// single URL can be handed to all three Config fields.
func correlateBackends(t *testing.T, r *recorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.paths = append(r.paths, req.URL.Path)
		if s := req.Header.Get(scopeHeader); s != "" {
			r.scopes = append(r.scopes, s)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(req.URL.Path, "/api/traces/"):
			r.tempo = append(r.tempo, req.URL.Query())
			_, _ = w.Write([]byte(correlateTempoBody))
		case strings.HasPrefix(req.URL.Path, "/loki/"):
			r.loki = append(r.loki, req.URL.Query())
			_, _ = w.Write([]byte(correlateLokiBody))
		default:
			r.vm = append(r.vm, req.URL.Query())
			_, _ = w.Write([]byte(correlateVMBody))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func correlateClient(t *testing.T) (*Client, *recorder) {
	t.Helper()
	r := &recorder{}
	srv := correlateBackends(t, r)
	return New(Config{VMSelectURL: srv.URL, TempoURL: srv.URL, LokiURL: srv.URL}, mapper(), nil), r
}

// The whole point of the endpoint: one call, three signals, joined on the trace.
func TestCorrelateJoinsSpansLogsAndMetrics(t *testing.T) {
	c, rec := correlateClient(t)

	got, err := c.Correlate(context.Background(), orgA, CorrelateQuery{
		TraceID: correlatedTraceID,
		Metrics: []string{"process_cpu_utilization"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Spans) != 2 {
		t.Fatalf("spans = %+v", got.Spans)
	}
	// The host is the join key for metrics, so it has to survive the trace read.
	if got.Spans[0].Host != "pod:aaaa1111" {
		t.Errorf("root host = %q, want the resource service.instance.id", got.Spans[0].Host)
	}

	// Logs come back keyed to the trace, with the correlation keys promoted out
	// of structured metadata.
	if len(got.Logs) != 2 {
		t.Fatalf("logs = %+v", got.Logs)
	}
	if got.Logs[0].TraceID != correlatedTraceID {
		t.Errorf("log trace_id = %q, want the trace", got.Logs[0].TraceID)
	}
	if got.Logs[0].SpanID != "bbbb" {
		t.Errorf("log span_id = %q, want the span that wrote it", got.Logs[0].SpanID)
	}
	// The level lives in this line's own metadata, not in the stream labels.
	if got.Logs[0].Level != "ERROR" {
		t.Errorf("log level = %q, want ERROR from the structured metadata", got.Logs[0].Level)
	}
	// Non-correlation metadata stays visible as a label; the promoted keys must
	// not be duplicated into it.
	if got.Logs[0].Labels["deployment_env"] != "prod" {
		t.Errorf("labels = %v, want the remaining metadata merged in", got.Logs[0].Labels)
	}
	if _, dup := got.Logs[0].Labels[traceIDKey]; dup {
		t.Error("trace_id was returned both as a field and as a label")
	}

	// Scopes are ordered by where the time went, so the root service is first.
	if len(got.Scopes) != 2 || got.Scopes[0].Service != "web-bff" {
		t.Fatalf("scopes = %+v", got.Scopes)
	}
	if got.Scopes[1].Service != "payments" || got.Scopes[1].Errors != 1 {
		t.Errorf("second scope = %+v, want payments with one error", got.Scopes[1])
	}

	// One metric per scope, each addressable by a stable key.
	if len(got.Metrics) != 2 {
		t.Fatalf("metrics = %+v", got.Metrics)
	}
	wantKey := "process_cpu_utilization@web-bff/pod:aaaa1111"
	if got.Metrics[0].Key != wantKey {
		t.Errorf("metric key = %q, want %q", got.Metrics[0].Key, wantKey)
	}

	// The window covers the trace plus the default pad on both sides.
	if !got.From.Equal(time.UnixMilli(1756000000000).Add(-DefaultCorrelationPad)) {
		t.Errorf("from = %v, want the trace start minus the pad", got.From)
	}
	if !got.To.Equal(time.UnixMilli(1756000004000).Add(DefaultCorrelationPad)) {
		t.Errorf("to = %v, want the trace end plus the pad", got.To)
	}

	if len(rec.tempo) != 1 || len(rec.loki) != 1 || len(rec.vm) != 2 {
		t.Errorf("upstream calls: tempo %d, loki %d, vm %d",
			len(rec.tempo), len(rec.loki), len(rec.vm))
	}
}

// Tenant isolation: every hop of the join carries the org, and no part of it
// comes from a caller parameter.
func TestCorrelateForcesTheOrgIntoEveryBackend(t *testing.T) {
	c, rec := correlateClient(t)

	got, err := c.Correlate(context.Background(), orgA, CorrelateQuery{
		TraceID: correlatedTraceID,
		Metrics: []string{"process_cpu_utilization"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Tempo and Loki: the scope header, resolved from the mapper.
	if len(rec.scopes) == 0 {
		t.Fatal("no request carried the scope header")
	}
	for _, scope := range rec.scopes {
		if scope != orgA {
			t.Errorf("%s = %q, want the caller org", scopeHeader, scope)
		}
	}

	// Loki: the org matcher in the selector, with the trace id as a structured
	// metadata label filter rather than as part of the stream selector.
	wantLogQL := `{klaro_org_id="` + orgA + `"} | trace_id = "` + correlatedTraceID + `"`
	if rec.loki[0].Get("query") != wantLogQL {
		t.Errorf("LogQL = %q\nwant %q", rec.loki[0].Get("query"), wantLogQL)
	}
	if got.LogsQuery != wantLogQL {
		t.Errorf("returned logs_query = %q, want the query that ran", got.LogsQuery)
	}

	// VictoriaMetrics: the tenant path segment from the mapper, plus the org
	// matcher and the scope labels in the expression.
	tenantPath := false
	for _, p := range rec.paths {
		if strings.HasPrefix(p, "/select/7/prometheus/") {
			tenantPath = true
		}
	}
	if !tenantPath {
		t.Errorf("no metrics request used the org tenant segment: %v", rec.paths)
	}
	wantPromQL := `process_cpu_utilization{klaro_org_id="` + orgA +
		`",service_instance_id="pod:aaaa1111",service_name="web-bff"}`
	if rec.vm[0].Get("query") != wantPromQL {
		t.Errorf("MetricsQL = %q\nwant %q", rec.vm[0].Get("query"), wantPromQL)
	}

	// And nothing internal comes back out.
	for _, m := range got.Metrics {
		for _, s := range m.Series {
			for _, banned := range []string{OrgLabel, "vm_account_id"} {
				if _, leaked := s.Labels[banned]; leaked {
					t.Errorf("%q reached the client", banned)
				}
			}
		}
	}
}

// An org with no tenant assignment must not fall back to an unscoped join.
func TestCorrelateRefusesAnUnmappedOrg(t *testing.T) {
	c, rec := correlateClient(t)
	const stranger = "00000000-0000-0000-0000-0000000000bb"

	_, err := c.Correlate(context.Background(), stranger, CorrelateQuery{TraceID: correlatedTraceID})
	if err == nil {
		t.Error("the join ran for an unmapped org")
	}
	if len(rec.paths) != 0 {
		t.Errorf("an unmapped org still reached a backend: %v", rec.paths)
	}
}

// A trace id from another org is not in this tenant's blocks, so the join stops
// at the trace read rather than going on to query logs and metrics over a
// window it invented.
func TestCorrelateStopsWhenTheTraceIsNotInThisOrg(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.paths = append(rec.paths, req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"batches":[]}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{VMSelectURL: srv.URL, TempoURL: srv.URL, LokiURL: srv.URL}, mapper(), nil)

	_, err := c.Correlate(context.Background(), orgA, CorrelateQuery{TraceID: correlatedTraceID})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	for _, p := range rec.paths {
		if !strings.HasPrefix(p, "/api/traces/") {
			t.Errorf("a log or metric query ran for a trace this org cannot see: %s", p)
		}
	}
}

// The trace id reaches an upstream URL path and a LogQL filter, so it is
// validated before either.
func TestCorrelateRejectsABadTraceID(t *testing.T) {
	c, rec := correlateClient(t)

	for _, bad := range []string{"", "short", "../../admin", "zzzzzzzzzzzzzzzz"} {
		_, err := c.Correlate(context.Background(), orgA, CorrelateQuery{TraceID: bad})
		if !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("Correlate(%q) = %v, want ErrInvalidQuery", bad, err)
		}
	}
	if len(rec.paths) != 0 {
		t.Error("an invalid trace id still reached a backend")
	}
}

// A caller-named metric is interpolated into MetricsQL, so a name that is not a
// metric name is a rejection rather than a note buried in a partial answer.
func TestCorrelateRejectsABadMetricName(t *testing.T) {
	c, rec := correlateClient(t)

	_, err := c.Correlate(context.Background(), orgA, CorrelateQuery{
		TraceID: correlatedTraceID,
		Metrics: []string{`cpu"} or up{klaro_org_id="other`},
	})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("err = %v, want ErrInvalidQuery", err)
	}
	if len(rec.paths) != 0 {
		t.Error("an invalid metric name still reached a backend")
	}
}

// The pad is a caller parameter on an endpoint whose window is meant to be one
// trace, so it has a ceiling.
func TestCorrelateBoundsThePad(t *testing.T) {
	c, _ := correlateClient(t)

	_, err := c.Correlate(context.Background(), orgA, CorrelateQuery{
		TraceID: correlatedTraceID, Pad: MaxCorrelationPad + time.Second,
	})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("err = %v, want ErrInvalidQuery", err)
	}
}

// A trace with no correlated lines is the normal shape of an app whose logger
// does not attach the trace context. Saying so is the difference between a
// useful empty state and a screen that looks broken.
func TestCorrelateNotesLogsThatCarryNoTraceID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(req.URL.Path, "/api/traces/"):
			_, _ = w.Write([]byte(correlateTempoBody))
		case strings.HasPrefix(req.URL.Path, "/loki/"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
		default:
			_, _ = w.Write([]byte(correlateVMBody))
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{VMSelectURL: srv.URL, TempoURL: srv.URL, LokiURL: srv.URL}, mapper(), nil)

	got, err := c.Correlate(context.Background(), orgA, CorrelateQuery{
		TraceID: correlatedTraceID, Metrics: []string{"process_cpu_utilization"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got.Notes, " "), "trace id") {
		t.Errorf("notes = %v, want one naming the missing trace id", got.Notes)
	}
	// And the rest of the join still happened.
	if len(got.Metrics) == 0 {
		t.Error("an empty log page suppressed the metrics half of the join")
	}
}

// One dead backend must not blank the signals that are working.
func TestCorrelateSurvivesAFailingLogsBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/api/traces/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(correlateTempoBody))
		case strings.HasPrefix(req.URL.Path, "/loki/"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"loki-2.internal: unknown label acme_secret_tenant"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(correlateVMBody))
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{VMSelectURL: srv.URL, TempoURL: srv.URL, LokiURL: srv.URL}, mapper(), nil)

	got, err := c.Correlate(context.Background(), orgA, CorrelateQuery{
		TraceID: correlatedTraceID, Metrics: []string{"process_cpu_utilization"},
	})
	if err != nil {
		t.Fatalf("a failing logs backend failed the whole join: %v", err)
	}
	if len(got.Spans) == 0 || len(got.Metrics) == 0 {
		t.Error("spans or metrics were lost along with the logs")
	}
	joined := strings.Join(got.Notes, " ")
	if !strings.Contains(joined, "correlated logs") {
		t.Errorf("notes = %v, want one naming the unavailable logs", got.Notes)
	}
	// The upstream body can name other tenants; it must never be echoed.
	if strings.Contains(joined, "acme_secret_tenant") || strings.Contains(joined, "loki-2.internal") {
		t.Errorf("an upstream error body reached the caller: %v", got.Notes)
	}
}

// A deployment with no Loki or vmselect must say so, not dial an empty URL.
func TestCorrelateReportsUnconfiguredBackends(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(correlateTempoBody))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{TempoURL: srv.URL}, mapper(), nil)

	got, err := c.Correlate(context.Background(), orgA, CorrelateQuery{TraceID: correlatedTraceID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Spans) != 2 {
		t.Errorf("the trace half of the join was lost: %+v", got.Spans)
	}
	if len(got.Notes) != 2 {
		t.Errorf("notes = %v, want one per unconfigured backend", got.Notes)
	}
}

// The fan-out is capped, and the cap is reported: a silent truncation reads as
// "this is every service in the trace".
func TestCorrelateCapsAndReportsTheFanOut(t *testing.T) {
	c, _ := correlateClient(t)

	got, err := c.Correlate(context.Background(), orgA, CorrelateQuery{
		TraceID: correlatedTraceID,
		// Two scopes in the fixture trace x seven metrics = 14, past the cap.
		Metrics: []string{"m_one", "m_two", "m_three", "m_four", "m_five", "m_six", "m_seven"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Metrics) != MaxCorrelationQueries {
		t.Errorf("metrics = %d, want the %d query cap", len(got.Metrics), MaxCorrelationQueries)
	}
	if !strings.Contains(strings.Join(got.Notes, " "), "query limit") {
		t.Errorf("notes = %v, want the truncation reported", got.Notes)
	}
}

// Scopes are how a span reaches its metrics, so the grouping and the ordering
// are worth pinning down on their own.
func TestScopesOfGroupsByProcessAndOrdersByTimeSpent(t *testing.T) {
	got := scopesOf([]Span{
		{Service: "web", Host: "pod:1", DurationMS: 10},
		{Service: "web", Host: "pod:1", DurationMS: 20, Status: "error"},
		// Same service, different instance: a separate scope, because a metric
		// series belongs to one process.
		{Service: "web", Host: "pod:2", DurationMS: 200},
		// Neither label: nothing to join on.
		{DurationMS: 999},
	})

	if len(got) != 2 {
		t.Fatalf("scopes = %+v", got)
	}
	if got[0].Host != "pod:2" || got[0].DurationMS != 200 {
		t.Errorf("first scope = %+v, want the slowest instance", got[0])
	}
	if got[1].Spans != 2 || got[1].DurationMS != 30 || got[1].Errors != 1 {
		t.Errorf("second scope = %+v, want two spans summed with the error counted", got[1])
	}
}

// A child that outlives its parent is a real shape, and its logs are inside the
// window only if the window is built from the spans rather than from the root.
func TestTraceWindowCoversASpanThatOutlivesTheRoot(t *testing.T) {
	from, to := traceWindow([]Span{
		{Start: 1000, DurationMS: 100},
		{Start: 1050, DurationMS: 5000},
	}, time.Second)

	if !from.Equal(time.UnixMilli(1000).Add(-time.Second)) {
		t.Errorf("from = %v", from)
	}
	if !to.Equal(time.UnixMilli(6050).Add(time.Second)) {
		t.Errorf("to = %v, want the latest span end plus the pad", to)
	}
}

// Loki omits the metadata element entirely when a line has none, and older
// versions never send it. Neither may break the page.
func TestQueryLogsToleratesValuesWithoutStructuredMetadata(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"streams","result":[
	 {"stream":{"service_name":"web","severity_text":"WARN"},
	  "values":[["1756000000000000000","no metadata here"]]}]}}`
	srv, _ := fakeBackend(t, body)
	c := New(Config{LokiURL: srv.URL}, mapper(), nil)

	got, err := c.QueryLogs(context.Background(), orgA, LogsQuery{Range: window(), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 1 || got.Data[0].Message != "no metadata here" {
		t.Fatalf("entries = %+v", got.Data)
	}
	if got.Data[0].TraceID != "" || got.Data[0].SpanID != "" {
		t.Errorf("correlation keys were invented: %+v", got.Data[0])
	}
	// The stream label is still the fallback for the level.
	if got.Data[0].Level != "WARN" {
		t.Errorf("level = %q", got.Data[0].Level)
	}
}

// A trace id in a log query is interpolated into LogQL, so it is validated the
// same way the path parameter is.
func TestQueryLogsRejectsABadTraceID(t *testing.T) {
	srv, cap := fakeBackend(t, correlateLokiBody)
	c := New(Config{LokiURL: srv.URL}, mapper(), nil)

	_, err := c.QueryLogs(context.Background(), orgA, LogsQuery{
		Range: window(), TraceID: `x" | line_format "`,
	})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("err = %v, want ErrInvalidQuery", err)
	}
	if cap.called != 0 {
		t.Error("an invalid trace id still reached Loki")
	}
}

// Correlate reads three backends, so a wrong mapper would be three chances to
// query unscoped. Both header-tenanted backends must be scoped exactly once.
func TestCorrelateUsesTheSameTenantMapperForAllThreeReads(t *testing.T) {
	r := &recorder{}
	srv := correlateBackends(t, r)
	c := New(Config{VMSelectURL: srv.URL, TempoURL: srv.URL, LokiURL: srv.URL},
		tenants.NewStaticMapper(map[string]uint32{orgA: 7}), nil)

	_, err := c.Correlate(context.Background(), orgA, CorrelateQuery{
		TraceID: correlatedTraceID, Metrics: []string{"process_cpu_utilization"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Tempo and Loki take the header; the metrics reads take the tenant path.
	if len(r.scopes) != 2 {
		t.Errorf("scope headers = %d, want one for Tempo and one for Loki", len(r.scopes))
	}
}
