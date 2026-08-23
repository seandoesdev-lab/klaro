package alerting

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/klaro/observability/internal/explorer"
)

const orgA = "00000000-0000-0000-0000-0000000000aa"

func ptr[T any](v T) *T { return &v }

func validInput() Input {
	return Input{
		Name:       ptr("checkout latency"),
		QuerySpec:  &QuerySpec{Metric: "http_server_duration", Agg: "p95", StepSec: 60},
		Comparator: ptr("gt"),
		Threshold:  ptr(0.5),
		// Store.Create seeds this; a bare Apply does not, so the fixture says so
		// explicitly rather than depending on a zero value.
		Enabled: ptr(true),
	}
}

// The rendered query is the whole point: the caller supplies a spec, the server
// supplies the org matcher, and the two are stored together.
func TestApplyRendersTheQueryWithTheOrgMatcher(t *testing.T) {
	var r Rule
	if err := r.Apply(validInput(), orgA); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Query, `klaro_org_id="`+orgA+`"`) {
		t.Errorf("rendered query has no org matcher: %s", r.Query)
	}
	if !strings.HasPrefix(r.Query, "quantile_over_time(0.95,") {
		t.Errorf("p95 did not render as a quantile: %s", r.Query)
	}
	expr, err := r.Expression()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(expr, "> 0.5") {
		t.Errorf("expression = %s, want the comparator and threshold appended", expr)
	}
}

// vmalert evaluates metrics only. Storing a log rule would store something that
// silently never fires, which is worse than refusing it.
func TestApplyRefusesNonMetricSignals(t *testing.T) {
	for _, signal := range []string{SignalLog, SignalTrace, "anything"} {
		in := validInput()
		in.Signal = ptr(signal)
		var r Rule
		if err := r.Apply(in, orgA); !errors.Is(err, ErrUnsupportedSignal) {
			t.Errorf("signal %q = %v, want ErrUnsupportedSignal", signal, err)
		}
	}
}

func TestApplyRejectsBadFields(t *testing.T) {
	cases := map[string]func(*Input){
		"no name":            func(in *Input) { in.Name = ptr("  ") },
		"long name":          func(in *Input) { in.Name = ptr(strings.Repeat("x", maxNameLen+1)) },
		"unknown comparator": func(in *Input) { in.Comparator = ptr("approx") },
		"unknown severity":   func(in *Input) { in.Severity = ptr("apocalyptic") },
		"unknown agg":        func(in *Input) { in.QuerySpec = &QuerySpec{Metric: "cpu", Agg: "topk", StepSec: 60} },
		"empty spec":         func(in *Input) { in.QuerySpec = &QuerySpec{} },
		"bad metric name":    func(in *Input) { in.QuerySpec = &QuerySpec{Metric: "cpu{evil}"} },
		"long for":           func(in *Input) { in.ForDurationSec = ptr(int(maxForDuration.Seconds()) + 1) },
		"bad channel type":   func(in *Input) { in.Channels = &[]Channel{{Type: "carrier-pigeon", Target: "x"}} },
		"empty channel":      func(in *Input) { in.Channels = &[]Channel{{Type: ChannelEmail}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mutate(&in)
			var r Rule
			if err := r.Apply(in, orgA); err == nil {
				t.Error("input was accepted")
			}
		})
	}
}

// A rule that could name the org label could name another org.
func TestApplyRefusesReservedFilterLabels(t *testing.T) {
	in := validInput()
	in.QuerySpec = &QuerySpec{
		Metric:  "cpu",
		Filters: []explorer.Matcher{{Label: explorer.OrgLabel, Value: "00000000-0000-0000-0000-0000000000bb"}},
	}
	var r Rule
	if err := r.Apply(in, orgA); !errors.Is(err, ErrInvalidRule) {
		t.Errorf("err = %v, want ErrInvalidRule", err)
	}
}

// A filter value must not be able to close its quote and open a new matcher.
func TestApplyEscapesFilterValues(t *testing.T) {
	in := validInput()
	in.QuerySpec = &QuerySpec{
		Metric:  "cpu",
		Filters: []explorer.Matcher{{Label: "service", Value: `a"} or up{klaro_org_id="other`}},
	}
	var r Rule
	if err := r.Apply(in, orgA); err != nil {
		t.Fatal(err)
	}
	// The value must appear as exactly one quoted literal. Counting braces would
	// be the wrong check: an escaped value legitimately contains them inside its
	// literal, and what matters is that it cannot terminate that literal.
	want := `service=` + strconv.Quote(`a"} or up{klaro_org_id="other`)
	if !strings.Contains(r.Query, want) {
		t.Errorf("rendered %s, want it to contain %s", r.Query, want)
	}
	if !strings.Contains(r.Query, `klaro_org_id="`+orgA+`"`) {
		t.Errorf("the org matcher was displaced: %s", r.Query)
	}
}

// PATCH is a partial update: fields left absent keep their value.
func TestApplyIsAPartialUpdate(t *testing.T) {
	var r Rule
	if err := r.Apply(validInput(), orgA); err != nil {
		t.Fatal(err)
	}
	before := r.Query

	if err := r.Apply(Input{Enabled: ptr(false)}, orgA); err != nil {
		t.Fatal(err)
	}
	if r.Enabled {
		t.Error("enabled was not applied")
	}
	if r.Name != "checkout latency" || r.Query != before {
		t.Errorf("absent fields changed: name=%q query=%q", r.Name, r.Query)
	}
}

func TestApplyDefaults(t *testing.T) {
	in := validInput()
	in.Severity = nil
	in.ForDurationSec = nil
	var r Rule
	if err := r.Apply(in, orgA); err != nil {
		t.Fatal(err)
	}
	if r.Signal != SignalMetric || r.Severity != "warning" {
		t.Errorf("defaults = %+v", r)
	}
	// The sustain window is seeded by Store.Create, so an explicit zero from a
	// caller survives and means "fire on the first breach".
	zero := validInput()
	zero.ForDurationSec = ptr(0)
	var immediate Rule
	if err := immediate.Apply(zero, orgA); err != nil {
		t.Fatal(err)
	}
	if immediate.ForDurationSec != 0 {
		t.Errorf("an explicit zero became %d", immediate.ForDurationSec)
	}
}
