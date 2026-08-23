package live

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustFlatten(t *testing.T, body string) Frame {
	t.Helper()
	f, err := FlattenOTLP(json.RawMessage(body), StreamMetric, 1756000000000)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func findPoint(f Frame, name string) (Point, bool) {
	for _, p := range f.Points {
		if p.Labels["__name__"] == name {
			return p, true
		}
	}
	return Point{}, false
}

func TestFlattenGaugeAndSum(t *testing.T) {
	f := mustFlatten(t, `{"resource":{"attributes":[
	  {"key":"service.name","value":{"stringValue":"checkout"}},
	  {"key":"klaro.org_id","value":{"stringValue":"00000000-0000-0000-0000-0000000000aa"}},
	  {"key":"vm_account_id","value":{"stringValue":"7"}}]},
	 "scopeMetrics":[{"metrics":[
	  {"name":"cpu.usage","gauge":{"dataPoints":[
	    {"asDouble":0.42,"attributes":[{"key":"core","value":{"stringValue":"0"}}]}]}},
	  {"name":"http.requests","sum":{"dataPoints":[{"asInt":"137"}]}}]}]}`)

	if f.Stream != StreamMetric || f.TS != 1756000000000 {
		t.Errorf("frame envelope = %+v", f)
	}
	if len(f.Points) != 2 {
		t.Fatalf("points = %d, want 2: %+v", len(f.Points), f.Points)
	}

	cpu, ok := findPoint(f, "cpu.usage")
	if !ok || cpu.Value != 0.42 {
		t.Fatalf("cpu point = %+v", cpu)
	}
	if cpu.Labels["service.name"] != "checkout" {
		t.Errorf("resource labels were not carried: %v", cpu.Labels)
	}
	if cpu.Labels["core"] != "0" {
		t.Errorf("data point attributes were not carried: %v", cpu.Labels)
	}

	// An int-encoded sum must arrive as a number, not a string.
	req, ok := findPoint(f, "http.requests")
	if !ok || req.Value != 137 {
		t.Errorf("sum point = %+v", req)
	}
}

// Routing plumbing is not telemetry. A client already knows its own org, and
// the VictoriaMetrics tenant is an internal detail it must never depend on.
func TestFlattenDropsInternalAttributes(t *testing.T) {
	f := mustFlatten(t, `{"resource":{"attributes":[
	  {"key":"klaro.org_id","value":{"stringValue":"00000000-0000-0000-0000-0000000000aa"}},
	  {"key":"vm_account_id","value":{"stringValue":"7"}},
	  {"key":"vm_project_id","value":{"stringValue":"0"}},
	  {"key":"service.name","value":{"stringValue":"checkout"}}]},
	 "scopeMetrics":[{"metrics":[{"name":"cpu.usage","gauge":{"dataPoints":[{"asDouble":1}]}}]}]}`)

	p := f.Points[0]
	for _, banned := range []string{"klaro.org_id", "vm_account_id", "vm_project_id"} {
		if _, leaked := p.Labels[banned]; leaked {
			t.Errorf("internal attribute %q reached the client: %v", banned, p.Labels)
		}
	}
	if p.Labels["service.name"] != "checkout" {
		t.Errorf("real attributes were dropped too: %v", p.Labels)
	}
}

// A live tile plots a number, so a histogram is reduced to count and sum.
// Shipping every bucket twice a second costs more than the view is worth.
func TestFlattenReducesHistogramToCountAndSum(t *testing.T) {
	f := mustFlatten(t, `{"resource":{"attributes":[]},"scopeMetrics":[{"metrics":[
	  {"name":"http.duration","histogram":{"dataPoints":[
	    {"count":"12","sum":3.5,"bucketCounts":["1","2","9"]}]}}]}]}`)

	count, ok := findPoint(f, "http.duration_count")
	if !ok || count.Value != 12 {
		t.Errorf("count point = %+v", count)
	}
	sum, ok := findPoint(f, "http.duration_sum")
	if !ok || sum.Value != 3.5 {
		t.Errorf("sum point = %+v", sum)
	}
	if len(f.Points) != 2 {
		t.Errorf("buckets leaked into the frame: %+v", f.Points)
	}
}

// Two points from one resource must not share a label map, or the last one
// written would rewrite the labels of the first.
func TestFlattenDoesNotAliasLabelMaps(t *testing.T) {
	f := mustFlatten(t, `{"resource":{"attributes":[
	  {"key":"service.name","value":{"stringValue":"checkout"}}]},
	 "scopeMetrics":[{"metrics":[
	  {"name":"a","gauge":{"dataPoints":[{"asDouble":1,"attributes":[{"key":"k","value":{"stringValue":"one"}}]}]}},
	  {"name":"b","gauge":{"dataPoints":[{"asDouble":2,"attributes":[{"key":"k","value":{"stringValue":"two"}}]}]}}]}]}`)

	a, _ := findPoint(f, "a")
	b, _ := findPoint(f, "b")
	if a.Labels["k"] != "one" || b.Labels["k"] != "two" {
		t.Errorf("label maps alias each other: a=%v b=%v", a.Labels, b.Labels)
	}
}

func TestFlattenSkipsValuelessPoints(t *testing.T) {
	f := mustFlatten(t, `{"resource":{"attributes":[]},"scopeMetrics":[{"metrics":[
	  {"name":"empty","gauge":{"dataPoints":[{}]}}]}]}`)
	if len(f.Points) != 0 {
		t.Errorf("a data point with no value was plotted: %+v", f.Points)
	}
}

// An empty frame still serialises points as [] rather than null, so a client
// can iterate without a nil check.
func TestFlattenEmitsEmptyPointArray(t *testing.T) {
	f := mustFlatten(t, `{"resource":{"attributes":[]},"scopeMetrics":[]}`)
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); !strings.Contains(got, `"points":[]`) {
		t.Errorf("marshalled frame = %s", got)
	}
}

func TestFlattenRejectsGarbage(t *testing.T) {
	if _, err := FlattenOTLP(json.RawMessage(`not json`), StreamMetric, 0); err == nil {
		t.Error("garbage was accepted")
	}
}

func TestValidStream(t *testing.T) {
	for _, ok := range []string{StreamMetric, StreamService} {
		if !ValidStream(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	// The stream name becomes part of a Redis channel, so anything that could
	// address a different channel must be refused.
	for _, bad := range []string{"", "metrics", "metric:other", "*", "METRIC"} {
		if ValidStream(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
