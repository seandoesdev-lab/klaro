package explorer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// hostRecorder answers each request from a body chosen by the query it carries, and
// keeps what it was asked. The host queries fan out over several expressions,
// so the single canned body fakeBackend gives cannot tell them apart.
type hostRecorder struct {
	mu      sync.Mutex
	paths   []string
	queries []string
	body    func(query string) string
}

func newRecorder(t *testing.T, body func(query string) string) (*httptest.Server, *hostRecorder) {
	t.Helper()
	rec := &hostRecorder{body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.queries = append(rec.queries, q)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rec.body(q)))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *hostRecorder) sawQueryContaining(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.queries {
		if strings.Contains(q, sub) {
			return true
		}
	}
	return false
}

// vector renders an instant-query envelope with one sample per host.
func vector(pairs ...string) string {
	rows := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		rows = append(rows, `{"metric":{"instance":"`+pairs[i]+
			`","klaro_org_id":"`+orgA+`","vm_account_id":"7"},"value":[1756000000,"`+pairs[i+1]+`"]}`)
	}
	return `{"status":"success","data":{"resultType":"vector","result":[` +
		strings.Join(rows, ",") + `]}}`
}

// matrix renders a range-query envelope: one series whose values sit on
// successive 60 second bucket boundaries.
func matrix(host string, values ...string) string {
	pts := make([]string, 0, len(values))
	for i, v := range values {
		ts := strconv.FormatInt(1756000000+int64(i)*60, 10)
		pts = append(pts, "["+ts+`,"`+v+`"]`)
	}
	return `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"instance":"` + host + `"},"values":[` + strings.Join(pts, ",") + `]}]}}`
}

// Both scoping mechanisms have to be visible on the wire: the tenant is the
// path segment the mapper resolved, and the org matcher is inside the query.
// Neither is reachable from a caller parameter, and a regression in either one
// is a cross-tenant read.
func TestVitalsPinsTenantAndOrgMatcher(t *testing.T) {
	srv, rec := newRecorder(t, func(q string) string {
		if strings.Contains(q, MetricMemoryUtilization) {
			return vector("host-a", "0.5")
		}
		return vector("host-a", "0.75")
	})
	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)

	got, err := c.Vitals(context.Background(), orgA, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range rec.paths {
		if p != "/select/7/prometheus/api/v1/query" {
			t.Errorf("upstream path = %q, want the org's tenant segment", p)
		}
	}
	if !rec.sawQueryContaining(`klaro_org_id="` + orgA + `"`) {
		t.Errorf("no query carried the org matcher: %v", rec.queries)
	}

	v, ok := got["host-a"]
	if !ok {
		t.Fatalf("host-a missing from %+v", got)
	}
	// The backend answers with the value of the expression, not the raw series:
	// the "1 - idle" arithmetic is inside the MetricsQL this package generated,
	// so 0.75 back means 75% busy. Memory answers 0.5 = 50% used.
	if v.CPUPct == nil || *v.CPUPct != 75 {
		t.Errorf("cpu = %v, want 75", v.CPUPct)
	}
	if v.MemPct == nil || *v.MemPct != 50 {
		t.Errorf("mem = %v, want 50", v.MemPct)
	}
}

// A metric a host does not report stays nil rather than becoming 0: the screen
// has to be able to say "not reported" instead of "idle".
func TestVitalsLeavesUnreportedMetricsNil(t *testing.T) {
	srv, _ := newRecorder(t, func(q string) string {
		if strings.Contains(q, MetricFilesystemUtilization) {
			return `{"status":"success","data":{"resultType":"vector","result":[]}}`
		}
		return vector("host-a", "0.1")
	})
	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)

	got, err := c.Vitals(context.Background(), orgA, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got["host-a"].DiskPct != nil {
		t.Errorf("disk = %v, want nil for a host that reports no filesystem metric",
			got["host-a"].DiskPct)
	}
	if got["host-a"].CPUPct == nil {
		t.Error("cpu must still be read when another metric is missing")
	}
}

// The instant queries must not answer for a backend that was never configured -
// dialling an empty URL is a timeout dressed up as an outage.
func TestVitalsRefusesWithoutABackend(t *testing.T) {
	c := New(Config{}, mapper(), nil)
	if _, err := c.Vitals(context.Background(), orgA, 0); !errors.Is(err, ErrBackend) {
		t.Fatalf("err = %v, want ErrBackend", err)
	}
}

// Availability is observed buckets over the buckets the window contains, not
// over the buckets that came back. A host that reported for the last hour of a
// day must not score 100%.
func TestUptimeMeasuresAgainstTheWholeWindow(t *testing.T) {
	// Three buckets answered, one of them empty (a zero count).
	srv, rec := newRecorder(t, func(string) string { return matrix("host-a", "1", "0", "1") })
	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)

	to := time.Unix(1756000000, 0).UTC()
	from := to.Add(-10 * time.Minute) // 10 minutes at a 60s step = 11 grid points

	got, err := c.Uptime(context.Background(), orgA, TimeRange{From: from, To: to}, time.Minute, 0.99, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts) != 1 {
		t.Fatalf("hosts = %+v", got.Hosts)
	}
	h := got.Hosts[0]
	if h.Expected != 11 {
		t.Errorf("expected buckets = %d, want 11 (derived from the window, not from the answer)", h.Expected)
	}
	if h.Observed != 2 {
		t.Errorf("observed = %d, want 2 (an empty bucket is not up)", h.Observed)
	}
	if h.Met {
		t.Error("2/11 must not meet a 99% objective")
	}
	if !rec.sawQueryContaining("count_over_time") {
		t.Errorf("uptime must count samples per bucket, got %v", rec.queries)
	}
}

// An org with no host metrics gets a well-formed empty result, so the widget
// renders an empty state. Reporting 100% for a fleet nobody is watching would
// be the most dangerous number this endpoint could return.
func TestUptimeWithNoDataIsEmptyNotPerfect(t *testing.T) {
	srv, _ := newRecorder(t, func(string) string {
		return `{"status":"success","data":{"resultType":"matrix","result":[]}}`
	})
	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)

	to := time.Unix(1756000000, 0).UTC()
	got, err := c.Uptime(context.Background(), orgA,
		TimeRange{From: to.Add(-time.Hour), To: to}, time.Minute, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts) != 0 || got.Expected != 0 || got.Availability != 0 {
		t.Errorf("result = %+v, want an empty measurement", got)
	}
	if got.Target != DefaultUptimeTarget {
		t.Errorf("target = %v, want the default when none is given", got.Target)
	}
}

// A host identity ends up inside a quoted matcher the server wrote, so it is
// validated before it gets there.
func TestHostSeriesRejectsAnUnusableHost(t *testing.T) {
	srv, _ := newRecorder(t, func(string) string { return matrix("host-a", "1") })
	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)

	to := time.Unix(1756000000, 0).UTC()
	rng := TimeRange{From: to.Add(-time.Hour), To: to}

	for _, host := range []string{"", "host\nname"} {
		_, err := c.HostSeries(context.Background(), orgA, host, rng, time.Minute)
		if !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("host %q: err = %v, want ErrInvalidQuery", host, err)
		}
	}
}

// Every declared series is present even when the backend has nothing for it, so
// the client renders a labelled empty chart rather than silently dropping a
// panel.
func TestHostSeriesAlwaysReturnsEveryKey(t *testing.T) {
	srv, rec := newRecorder(t, func(q string) string {
		if strings.Contains(q, MetricNetworkIO) {
			return `{"status":"success","data":{"resultType":"matrix","result":[]}}`
		}
		return matrix("host-a", "1", "2")
	})
	c := New(Config{VMSelectURL: srv.URL}, mapper(), nil)

	to := time.Unix(1756000000, 0).UTC()
	got, err := c.HostSeries(context.Background(), orgA, "host-a",
		TimeRange{From: to.Add(-time.Hour), To: to}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range HostSeriesKeys {
		s, ok := got.Series[key]
		if !ok {
			t.Errorf("series %q missing from the response", key)
			continue
		}
		if s.Points == nil {
			t.Errorf("series %q has nil points; an empty slice is what the client can render", key)
		}
	}
	if !rec.sawQueryContaining(`instance="host-a"`) {
		t.Errorf("the host matcher never reached the backend: %v", rec.queries)
	}
	if !rec.sawQueryContaining(`klaro_org_id="` + orgA + `"`) {
		t.Errorf("the org matcher never reached the backend: %v", rec.queries)
	}
}

// The step lands on the wire as the whole-second literal MetricsQL takes; a
// fractional one would be rejected by the backend rather than by us.
func TestDurationLiteralIsWholeSeconds(t *testing.T) {
	cases := map[time.Duration]string{
		time.Minute:            "60s",
		90 * time.Second:       "90s",
		500 * time.Millisecond: "1s",
		0:                      "1s",
	}
	for d, want := range cases {
		if got := durationLiteral(d); got != want {
			t.Errorf("durationLiteral(%v) = %q, want %q", d, got, want)
		}
	}
}

// The generated queries have to survive URL encoding unchanged - a matcher that
// loses its quotes is a matcher that no longer scopes anything.
func TestGeneratedQueryRoundTripsThroughTheURL(t *testing.T) {
	q := vitalsQueries[0].build(orgA)
	params := url.Values{}
	params.Set("query", q)
	parsed, err := url.ParseQuery(params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Get("query") != q {
		t.Errorf("query changed across encoding:\n got %q\nwant %q", parsed.Get("query"), q)
	}
}
