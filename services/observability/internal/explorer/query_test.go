package explorer

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const orgA = "00000000-0000-0000-0000-0000000000aa"

func TestSelectorAlwaysCarriesTheOrgMatcher(t *testing.T) {
	got := Selector("http_requests_total", orgA, nil)
	want := `http_requests_total{klaro_org_id="00000000-0000-0000-0000-0000000000aa"}`
	if got != want {
		t.Errorf("Selector = %q, want %q", got, want)
	}

	// With user filters the org matcher stays first and stays present.
	got = Selector("cpu", orgA, []Matcher{{Label: "service", Value: "checkout"}, {Label: "env", Negate: true, Value: "dev"}})
	want = `cpu{klaro_org_id="00000000-0000-0000-0000-0000000000aa",env!="dev",service="checkout"}`
	if got != want {
		t.Errorf("Selector = %q, want %q", got, want)
	}
}

// The whole point of the package: a value cannot close its quote and start a
// new matcher, or the caller would be writing the query.
func TestSelectorEscapesInjectionAttempts(t *testing.T) {
	attacks := []string{
		`checkout"} or up{klaro_org_id="other`,
		`a\"} or foo{x="`,
		`x", klaro_org_id="00000000-0000-0000-0000-0000000000bb`,
		"line\nbreak",
	}
	for _, attack := range attacks {
		got := Selector("cpu", orgA, []Matcher{{Label: "service", Value: attack}})
		// The value must appear as exactly one quoted literal, with nothing
		// emitted around it: that is what makes a breakout impossible, and
		// comparing against the whole rendered string is the only way to be
		// sure nothing else slipped in.
		want := `cpu{klaro_org_id="` + orgA + `",service=` + strconv.Quote(attack) + `}`
		if got != want {
			t.Errorf("value %q rendered as\n  %s\nwant\n  %s", attack, got, want)
		}
		if !strings.Contains(got, `klaro_org_id="`+orgA+`"`) {
			t.Errorf("value %q displaced the org matcher: %q", attack, got)
		}
	}
}

// A caller must not be able to name the labels that decide the tenant.
func TestParseFiltersRejectsReservedLabels(t *testing.T) {
	for _, reserved := range []string{"klaro_org_id", "vm_account_id", "vm_project_id", "__name__"} {
		_, err := ParseFilters([]string{reserved + "=whatever"})
		if !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("filter on %q was accepted (err %v)", reserved, err)
		}
	}
}

func TestParseFilters(t *testing.T) {
	got, err := ParseFilters([]string{"service=checkout,env!=dev", " region = eu "})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("matchers = %+v", got)
	}
	if got[0] != (Matcher{Label: "service", Value: "checkout"}) {
		t.Errorf("first = %+v", got[0])
	}
	if got[1] != (Matcher{Label: "env", Negate: true, Value: "dev"}) {
		t.Errorf("second = %+v", got[1])
	}
	// A spaced pair keeps its spaces in the value but not in the label; the
	// label is validated, so a stray space would be rejected rather than
	// silently matched.
	if got[2].Label != "region" {
		t.Errorf("third = %+v", got[2])
	}
}

func TestParseFiltersRejectsJunk(t *testing.T) {
	bad := []string{
		"=novalue",
		"no-separator",
		"bad label=x",
		"9leading=x",
		"service=" + strings.Repeat("x", maxLabelValue+1),
		"service=line\nbreak",
	}
	for _, item := range bad {
		if _, err := ParseFilters([]string{item}); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("ParseFilters(%q) = %v, want ErrInvalidQuery", item, err)
		}
	}
}

func TestAggregateWhitelist(t *testing.T) {
	sel := `cpu{klaro_org_id="x"}`
	cases := map[string]string{
		"":      sel,
		"rate":  "rate(" + sel + "[60s])",
		"avg":   "avg_over_time(" + sel + "[60s])",
		"sum":   "sum_over_time(" + sel + "[60s])",
		"count": "count_over_time(" + sel + "[60s])",
		"p50":   "quantile_over_time(0.50, " + sel + "[60s])",
		"p95":   "quantile_over_time(0.95, " + sel + "[60s])",
		"p99":   "quantile_over_time(0.99, " + sel + "[60s])",
	}
	for agg, want := range cases {
		got, err := Aggregate(agg, sel, time.Minute)
		if err != nil {
			t.Fatalf("agg %q: %v", agg, err)
		}
		if got != want {
			t.Errorf("agg %q = %q, want %q", agg, got, want)
		}
	}
}

// Anything outside the whitelist is a rejection, never a passthrough - an
// unrecognised aggregation that reached the backend would be caller-written
// MetricsQL.
func TestAggregateRejectsAnythingElse(t *testing.T) {
	for _, agg := range []string{"topk", "rate(x) or up", "AVG", "p42", "1"} {
		if _, err := Aggregate(agg, "cpu{}", time.Minute); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("Aggregate(%q) = %v, want ErrInvalidQuery", agg, err)
		}
	}
}

func TestStripInternalLabels(t *testing.T) {
	got := stripInternalLabels(map[string]string{
		"klaro_org_id": orgA, "vm_account_id": "7", "vm_project_id": "0",
		"service_name": "checkout", "__name__": "cpu",
	})
	for _, banned := range []string{"klaro_org_id", "vm_account_id", "vm_project_id"} {
		if _, leaked := got[banned]; leaked {
			t.Errorf("%q reached the client", banned)
		}
	}
	if got["service_name"] != "checkout" || got["__name__"] != "cpu" {
		t.Errorf("real labels were dropped: %v", got)
	}
}
