package dashboards

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/klaro/observability/internal/explorer"
)

func metricsPanel() Panel {
	return Panel{
		ID: "p1", Title: "Checkout p95", Type: "timeseries",
		Layout: Layout{X: 0, Y: 0, W: 6, H: 4},
		Query: PanelQuery{
			Signal: SignalMetrics, Metric: "http_server_duration",
			Agg: "p95", StepSec: 60,
		},
	}
}

func TestValidSpecRoundTrips(t *testing.T) {
	spec := Spec{Panels: []Panel{metricsPanel()}, RangeSec: 3600, RefreshSec: 30}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var back Spec
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Panels) != 1 || back.Panels[0].Query.Agg != "p95" {
		t.Errorf("round trip lost data: %+v", back)
	}
	// Lowercase keys: the spec is a customer-facing document, not a Go dump.
	if !strings.Contains(string(raw), `"step_sec"`) {
		t.Errorf("marshalled spec = %s", raw)
	}
}

// An empty document must serialise panels as [] so the frontend can iterate
// without a nil check.
func TestEmptySpecNormalisesPanels(t *testing.T) {
	var spec Spec
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"panels":[]`) {
		t.Errorf("marshalled spec = %s", raw)
	}
}

// The point of validating on write: a panel that could name the org label would
// be a stored cross-tenant query attempt.
func TestPanelRefusesReservedLabels(t *testing.T) {
	for _, label := range []string{explorer.OrgLabel, "vm_account_id", "vm_project_id", "__name__"} {
		p := metricsPanel()
		p.Query.Filters = []explorer.Matcher{{Label: label, Value: "x"}}
		spec := Spec{Panels: []Panel{p}}
		if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("label %q was accepted (err %v)", label, err)
		}
	}
}

// A stored aggregation the Explorer would refuse is a query that only fails
// when someone opens the dashboard.
func TestPanelRefusesUnknownAggregation(t *testing.T) {
	for _, agg := range []string{"topk", "AVG", "rate(x) or up"} {
		p := metricsPanel()
		p.Query.Agg = agg
		if err := (&Spec{Panels: []Panel{p}}).Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("agg %q was accepted (err %v)", agg, err)
		}
	}
}

func TestPanelRejectsBadShape(t *testing.T) {
	cases := map[string]func(*Panel){
		"no id":             func(p *Panel) { p.ID = "" },
		"id with spaces":    func(p *Panel) { p.ID = "panel one" },
		"unknown type":      func(p *Panel) { p.Type = "sankey" },
		"zero width":        func(p *Panel) { p.Layout.W = 0 },
		"overflows grid":    func(p *Panel) { p.Layout.X, p.Layout.W = 10, 6 },
		"negative row":      func(p *Panel) { p.Layout.Y = -1 },
		"bad metric name":   func(p *Panel) { p.Query.Metric = "cpu{evil}" },
		"unbounded metrics": func(p *Panel) { p.Query.Metric = ""; p.Query.Filters = nil },
		"unknown signal":    func(p *Panel) { p.Query.Signal = "profiles" },
		"step too long":     func(p *Panel) { p.Query.StepSec = maxStepSec + 1 },
		"limit too high":    func(p *Panel) { p.Query.Limit = maxPanelLimit + 1 },
		"long title":        func(p *Panel) { p.Title = strings.Repeat("x", maxTitleLen+1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := metricsPanel()
			mutate(&p)
			if err := (&Spec{Panels: []Panel{p}}).Validate(); err == nil {
				t.Error("panel was accepted")
			}
		})
	}
}

// Fields belonging to another signal are refused rather than ignored: an ignored
// field looks like it did something.
func TestPanelRefusesFieldsFromAnotherSignal(t *testing.T) {
	metrics := metricsPanel()
	metrics.Query.Service = "checkout"
	if err := (&Spec{Panels: []Panel{metrics}}).Validate(); err == nil {
		t.Error("a metrics panel accepted a traces field")
	}

	traces := Panel{ID: "t1", Type: "traces", Layout: Layout{W: 6, H: 4},
		Query: PanelQuery{Signal: SignalTraces, Agg: "p95"}}
	if err := (&Spec{Panels: []Panel{traces}}).Validate(); err == nil {
		t.Error("a traces panel accepted an aggregation")
	}

	logs := Panel{ID: "l1", Type: "logs", Layout: Layout{W: 6, H: 4},
		Query: PanelQuery{Signal: SignalLogs, Metric: "cpu"}}
	if err := (&Spec{Panels: []Panel{logs}}).Validate(); err == nil {
		t.Error("a logs panel accepted a metric")
	}
}

func TestValidTracesAndLogsPanels(t *testing.T) {
	spec := Spec{Panels: []Panel{
		{ID: "t1", Title: "Slow", Type: "traces", Layout: Layout{W: 6, H: 4},
			Query: PanelQuery{Signal: SignalTraces, Service: "checkout", MinDurationMS: 3000, Limit: 50}},
		{ID: "l1", Title: "Errors", Type: "logs", Layout: Layout{X: 6, W: 6, H: 4},
			Query: PanelQuery{Signal: SignalLogs, Contains: "boom", Limit: 100,
				Filters: []explorer.Matcher{{Label: "service_name", Value: "checkout"}}}},
	}}
	if err := spec.Validate(); err != nil {
		t.Fatalf("a valid two-panel spec was rejected: %v", err)
	}
}

// Panel ids address a tile in a URL fragment; duplicates make that ambiguous.
func TestDuplicatePanelIDsAreRejected(t *testing.T) {
	a, b := metricsPanel(), metricsPanel()
	b.Layout.X = 6
	if err := (&Spec{Panels: []Panel{a, b}}).Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("err = %v, want ErrInvalidSpec", err)
	}
}

func TestSpecBounds(t *testing.T) {
	many := make([]Panel, MaxPanels+1)
	for i := range many {
		p := metricsPanel()
		p.ID = "p" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		many[i] = p
	}
	if err := (&Spec{Panels: many}).Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Error("panel count was unbounded")
	}
	if err := (&Spec{RangeSec: maxRangeSec + 1}).Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Error("range_sec was unbounded")
	}
	// A one-second refresh over a dozen panels is a self-inflicted load test.
	if err := (&Spec{RefreshSec: 1}).Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Error("refresh_sec had no floor")
	}
}
