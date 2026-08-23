package explorer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/klaro/observability/internal/tenants"
)

// capture records what the client sent upstream, which is where the tenant
// enforcement has to be visible.
type capture struct {
	path   string
	query  url.Values
	scope  string
	called int
}

// fakeBackend answers with body and records the request.
func fakeBackend(t *testing.T, body string) (*httptest.Server, *capture) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.called++
		cap.path = r.URL.Path
		cap.query = r.URL.Query()
		cap.scope = r.Header.Get(scopeHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// mapper pins orgA to a known tenant so the assertions can name it.
func mapper() tenants.Mapper {
	return tenants.NewStaticMapper(map[string]uint32{orgA: 7})
}

func window() TimeRange {
	to := time.Unix(1756000000, 0).UTC()
	return TimeRange{From: to.Add(-time.Hour), To: to}
}

const vmBody = `{"status":"success","data":{"resultType":"matrix","result":[
 {"metric":{"__name__":"cpu","service_name":"checkout","klaro_org_id":"00000000-0000-0000-0000-0000000000aa","vm_account_id":"7"},
  "values":[[1756000000,"0.5"],[1756000030,"0.75"]]}]}}`

// The VictoriaMetrics tenant is a path segment, and it comes from the mapper -
// there is no request parameter that can reach it.
func TestQueryMetricsPinsTheTenantAndOrgMatcher(t *testing.T) {
	srv, cap := fakeBackend(t, vmBody)
	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)

	got, err := c.QueryMetrics(context.Background(), orgA, MetricsQuery{
		Range: window(), Metric: "cpu",
		Filters: []Matcher{{Label: "service_name", Value: "checkout"}},
		Agg:     "avg", Step: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	if cap.path != "/select/7/prometheus/api/v1/query_range" {
		t.Errorf("upstream path = %q, want the org's tenant segment", cap.path)
	}
	wantQuery := `avg_over_time(cpu{klaro_org_id="` + orgA + `",service_name="checkout"}[60s])`
	if cap.query.Get("query") != wantQuery {
		t.Errorf("upstream query = %q\nwant %q", cap.query.Get("query"), wantQuery)
	}
	if cap.query.Get("step") != "60s" {
		t.Errorf("step = %q", cap.query.Get("step"))
	}

	if len(got.Series) != 1 || len(got.Series[0].Points) != 2 {
		t.Fatalf("result = %+v", got)
	}
	if got.Series[0].Points[0].TS != 1756000000000 || got.Series[0].Points[0].Value != 0.5 {
		t.Errorf("first point = %+v", got.Series[0].Points[0])
	}
	// Routing plumbing must not come back out.
	for _, banned := range []string{"klaro_org_id", "vm_account_id"} {
		if _, leaked := got.Series[0].Labels[banned]; leaked {
			t.Errorf("%q reached the client", banned)
		}
	}
}

// A query with neither a metric nor a filter would be "every series this tenant
// ever wrote", which is a way to fall over, not a query.
func TestQueryMetricsRefusesUnboundedQueries(t *testing.T) {
	srv, cap := fakeBackend(t, vmBody)
	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)

	_, err := c.QueryMetrics(context.Background(), orgA, MetricsQuery{Range: window()})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("err = %v, want ErrInvalidQuery", err)
	}
	if cap.called != 0 {
		t.Error("an invalid query still reached the backend")
	}
}

func TestQueryMetricsRefusesTooManyPoints(t *testing.T) {
	srv, _ := fakeBackend(t, vmBody)
	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)

	wide := TimeRange{From: time.Unix(0, 0), To: time.Unix(90*24*3600, 0)}
	_, err := c.QueryMetrics(context.Background(), orgA, MetricsQuery{
		Range: wide, Metric: "cpu", Step: time.Second,
	})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("err = %v, want ErrInvalidQuery", err)
	}
}

const tempoSearchBody = `{"traces":[
 {"traceID":"5b8efff798038103d269b633813fc60c","rootServiceName":"checkout",
  "rootTraceName":"GET /cart","startTimeUnixNano":"1756000000000000000","durationMs":4200}]}`

// Tempo tenancy is the header, and the header comes from the mapper.
func TestSearchTracesSendsTheScopeHeader(t *testing.T) {
	srv, cap := fakeBackend(t, tempoSearchBody)
	c := New(Config{TempoURL: srv.URL}, mapper(), nil)

	got, err := c.SearchTraces(context.Background(), orgA, TracesQuery{
		Range: window(), Service: "checkout", MinDuration: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.scope != orgA {
		t.Errorf("%s = %q, want the org", scopeHeader, cap.scope)
	}
	wantQ := `{resource.service.name = "checkout" && duration >= 3000ms}`
	if cap.query.Get("q") != wantQ {
		t.Errorf("TraceQL = %q\nwant %q", cap.query.Get("q"), wantQ)
	}
	if len(got.Data) != 1 || got.Data[0].RootService != "checkout" || got.Data[0].DurationMS != 4200 {
		t.Fatalf("result = %+v", got.Data)
	}
	if got.Data[0].Start != 1756000000000 {
		t.Errorf("start = %d, want unix milliseconds", got.Data[0].Start)
	}
}

// A service name is caller input and must not be able to write TraceQL.
func TestSearchTracesEscapesTheServiceName(t *testing.T) {
	srv, cap := fakeBackend(t, `{"traces":[]}`)
	c := New(Config{TempoURL: srv.URL}, mapper(), nil)

	attack := `checkout" || resource.service.name = "billing`
	if _, err := c.SearchTraces(context.Background(), orgA, TracesQuery{Range: window(), Service: attack}); err != nil {
		t.Fatal(err)
	}
	want := `{resource.service.name = ` + `"checkout\" || resource.service.name = \"billing"` + `}`
	if cap.query.Get("q") != want {
		t.Errorf("TraceQL = %q\nwant %q", cap.query.Get("q"), want)
	}
}

const tempoTraceBody = `{"batches":[
 {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}}]},
  "scopeSpans":[{"spans":[
    {"spanId":"bbbb","parentSpanId":"aaaa","name":"SELECT carts",
     "startTimeUnixNano":"1756000000500000000","endTimeUnixNano":"1756000000900000000",
     "status":{"code":"STATUS_CODE_ERROR"}},
    {"spanId":"aaaa","name":"GET /cart",
     "startTimeUnixNano":"1756000000000000000","endTimeUnixNano":"1756000001000000000",
     "status":{}}]}]}]}`

// The waterfall needs start offsets, and it needs them in order.
func TestGetTraceBuildsAnOrderedWaterfall(t *testing.T) {
	srv, cap := fakeBackend(t, tempoTraceBody)
	c := New(Config{TempoURL: srv.URL}, mapper(), nil)

	got, err := c.GetTrace(context.Background(), orgA, "5b8efff798038103d269b633813fc60c")
	if err != nil {
		t.Fatal(err)
	}
	if cap.scope != orgA {
		t.Errorf("%s = %q, want the org", scopeHeader, cap.scope)
	}
	if len(got.Spans) != 2 {
		t.Fatalf("spans = %+v", got.Spans)
	}
	// The root starts first, so it must come first even though the backend
	// listed the child ahead of it.
	if got.Spans[0].SpanID != "aaaa" || got.Spans[1].SpanID != "bbbb" {
		t.Errorf("span order = %s, %s", got.Spans[0].SpanID, got.Spans[1].SpanID)
	}
	if got.Spans[0].DurationMS != 1000 || got.Spans[1].DurationMS != 400 {
		t.Errorf("durations = %v, %v", got.Spans[0].DurationMS, got.Spans[1].DurationMS)
	}
	if got.Spans[1].ParentSpanID != "aaaa" {
		t.Errorf("parent = %q", got.Spans[1].ParentSpanID)
	}
	if got.Spans[0].Service != "checkout" {
		t.Errorf("service was not carried from the resource: %q", got.Spans[0].Service)
	}
	if got.Spans[1].Status != "error" || got.Spans[0].Status != "unset" {
		t.Errorf("status = %q, %q", got.Spans[0].Status, got.Spans[1].Status)
	}
}

// A trace id is interpolated into an upstream URL path, so it is validated
// before it gets there.
func TestGetTraceRejectsNonHexIDs(t *testing.T) {
	srv, cap := fakeBackend(t, tempoTraceBody)
	c := New(Config{TempoURL: srv.URL}, mapper(), nil)

	bad := []string{"", "short", "../../admin", "zzzzzzzzzzzzzzzz", "5b8efff798038103d269b633813fc60caa"}
	for _, id := range bad {
		if _, err := c.GetTrace(context.Background(), orgA, id); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("GetTrace(%q) = %v, want ErrInvalidQuery", id, err)
		}
	}
	if cap.called != 0 {
		t.Error("an invalid trace id still reached the backend")
	}
}

// Another tenant's trace is simply not in this tenant's blocks, so the header
// turns a cross-tenant read into a miss.
func TestGetTraceReportsAnEmptyTraceAsNotFound(t *testing.T) {
	srv, _ := fakeBackend(t, `{"batches":[]}`)
	c := New(Config{TempoURL: srv.URL}, mapper(), nil)

	if _, err := c.GetTrace(context.Background(), orgA, "5b8efff798038103d269b633813fc60c"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

const lokiBody = `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"service_name":"checkout","severity_text":"ERROR","klaro_org_id":"00000000-0000-0000-0000-0000000000aa"},
  "values":[["1756000001000000000","boom"],["1756000000000000000","warming up"]]}]}}`

func TestQueryLogsForcesTheOrgIntoBothLayers(t *testing.T) {
	srv, cap := fakeBackend(t, lokiBody)
	c := New(Config{LokiURL: srv.URL}, mapper(), nil)

	got, err := c.QueryLogs(context.Background(), orgA, LogsQuery{
		Range:   window(),
		Filters: []Matcher{{Label: "service_name", Value: "checkout"}},
		// A literal substring, not LogQL: a caller-supplied pipeline could
		// carry a label matcher stage.
		Contains: `boom" } | json | line_format "`,
		Limit:    50,
	})
	if err != nil {
		t.Fatal(err)
	}

	if cap.scope != orgA {
		t.Errorf("%s = %q, want the org", scopeHeader, cap.scope)
	}
	wantQuery := `{klaro_org_id="` + orgA + `",service_name="checkout"} |= ` +
		strconv.Quote(`boom" } | json | line_format "`)
	if cap.query.Get("query") != wantQuery {
		t.Errorf("LogQL = %q\nwant %q", cap.query.Get("query"), wantQuery)
	}
	if cap.query.Get("direction") != "backward" {
		t.Errorf("direction = %q, want backward", cap.query.Get("direction"))
	}

	if len(got.Data) != 2 {
		t.Fatalf("entries = %+v", got.Data)
	}
	// Newest first: a log view opens on what just happened.
	if got.Data[0].Message != "boom" || got.Data[0].TS != 1756000001000 {
		t.Errorf("first entry = %+v", got.Data[0])
	}
	if got.Data[0].Level != "ERROR" {
		t.Errorf("level = %q", got.Data[0].Level)
	}
	if _, leaked := got.Data[0].Labels["klaro_org_id"]; leaked {
		t.Error("the org label reached the client")
	}
	// Two entries against a limit of fifty means the window is exhausted.
	if got.Next != "" {
		t.Errorf("next = %q, want empty", got.Next)
	}
}

// A full page has to be resumable, or a busy service's logs are untraversable.
func TestQueryLogsPagesWhenTheLimitIsHit(t *testing.T) {
	srv, _ := fakeBackend(t, lokiBody)
	c := New(Config{LokiURL: srv.URL}, mapper(), nil)

	got, err := c.QueryLogs(context.Background(), orgA, LogsQuery{Range: window(), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.Next != "1756000000000000000" {
		t.Errorf("next = %q, want the oldest nanosecond seen", got.Next)
	}
}

// A deployment without a backend must say so, not dial an empty URL.
func TestUnconfiguredBackendsReportThemselves(t *testing.T) {
	c := New(Config{}, mapper(), nil)
	ctx := context.Background()

	if _, err := c.QueryMetrics(ctx, orgA, MetricsQuery{Range: window(), Metric: "cpu"}); !errors.Is(err, ErrBackend) {
		t.Errorf("metrics err = %v, want ErrBackend", err)
	}
	if _, err := c.SearchTraces(ctx, orgA, TracesQuery{Range: window()}); !errors.Is(err, ErrBackend) {
		t.Errorf("traces err = %v, want ErrBackend", err)
	}
	if _, err := c.QueryLogs(ctx, orgA, LogsQuery{Range: window()}); !errors.Is(err, ErrBackend) {
		t.Errorf("logs err = %v, want ErrBackend", err)
	}
}

// An org with no tenant assignment must not fall back to an unscoped query.
func TestUnmappedOrgIsRefused(t *testing.T) {
	srv, cap := fakeBackend(t, vmBody)
	c := New(Config{VMSelectURL: srv.URL, TempoURL: srv.URL, LokiURL: srv.URL}, mapper(), nil)
	ctx := context.Background()
	const stranger = "00000000-0000-0000-0000-0000000000bb"

	if _, err := c.QueryMetrics(ctx, stranger, MetricsQuery{Range: window(), Metric: "cpu"}); err == nil {
		t.Error("metrics query ran for an unmapped org")
	}
	if _, err := c.SearchTraces(ctx, stranger, TracesQuery{Range: window()}); err == nil {
		t.Error("trace search ran for an unmapped org")
	}
	if _, err := c.QueryLogs(ctx, stranger, LogsQuery{Range: window()}); err == nil {
		t.Error("log query ran for an unmapped org")
	}
	if cap.called != 0 {
		t.Error("an unmapped org still reached a backend")
	}
}

// An upstream error body can name other tenants' labels or echo internal
// topology, so the caller gets a classified error, not the body.
func TestBackendFailureIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"vmselect-3.internal: unknown label acme_secret_tenant"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)
	_, err := c.QueryMetrics(context.Background(), orgA, MetricsQuery{Range: window(), Metric: "cpu"})
	if !errors.Is(err, ErrBackend) {
		t.Fatalf("err = %v, want ErrBackend", err)
	}
}

func TestTimeRangeValidation(t *testing.T) {
	now := time.Unix(1756000000, 0)
	bad := []TimeRange{
		{},
		{From: now},
		{To: now},
		{From: now, To: now},
		{From: now.Add(time.Hour), To: now},
	}
	for _, r := range bad {
		if err := r.Validate(); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("range %+v was accepted", r)
		}
	}
	if err := (TimeRange{From: now.Add(-time.Hour), To: now}).Validate(); err != nil {
		t.Errorf("a valid range was rejected: %v", err)
	}
}

// Tempo returns hex from its search API and base64 from /api/traces. A client
// that got a hex trace id from search must be able to match the span ids it
// gets back, so everything leaving the package is hex.
func TestGetTraceNormalisesBase64SpanIDs(t *testing.T) {
	// GM5k188Fg80= is the base64 form of 18ce64d7cf05 83cd.
	body := `{"batches":[{"resource":{"attributes":[]},"scopeSpans":[{"spans":[
	  {"spanId":"GM5k188Fg80=","parentSpanId":"GM5k188Fg84=","name":"GET /cart",
	   "startTimeUnixNano":"1756000000000000000","endTimeUnixNano":"1756000001000000000"}]}]}]}`
	srv, _ := fakeBackend(t, body)
	c := New(Config{TempoURL: srv.URL}, mapper(), nil)

	got, err := c.GetTrace(context.Background(), orgA, "5b8efff798038103d269b633813fc60c")
	if err != nil {
		t.Fatal(err)
	}
	s := got.Spans[0]
	if s.SpanID != "18ce64d7cf0583cd" {
		t.Errorf("span_id = %q, want hex", s.SpanID)
	}
	if s.ParentSpanID != "18ce64d7cf0583ce" {
		t.Errorf("parent_span_id = %q, want hex", s.ParentSpanID)
	}
}

// An id that already arrived as hex must survive untouched.
func TestNormalizeIDLeavesHexAlone(t *testing.T) {
	if got := normalizeID("5B8EFFF798038103"); got != "5b8efff798038103" {
		t.Errorf("normalizeID = %q", got)
	}
	if got := normalizeID(""); got != "" {
		t.Errorf("normalizeID(empty) = %q", got)
	}
}
