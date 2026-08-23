package alerting

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/klaro/observability/internal/explorer"
)

func renderOne(t *testing.T, org OrgRules) map[string]any {
	t.Helper()
	raw, err := Render([]OrgRules{org}, RenderOptions{Interval: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// The file is JSON on purpose - YAML is a superset, so vmalert parses it and
	// the standard library does the escaping - which also means a test can
	// decode it instead of matching text.
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("rendered file is not valid JSON: %v", err)
	}
	return out
}

func groups(t *testing.T, file map[string]any) []map[string]any {
	t.Helper()
	raw, ok := file["groups"].([]any)
	if !ok {
		t.Fatalf("no groups in %v", file)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, g := range raw {
		out = append(out, g.(map[string]any))
	}
	return out
}

func rules(t *testing.T, group map[string]any) []map[string]any {
	t.Helper()
	raw := group["rules"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestRenderAlertingRuleCarriesTenantAndIdentity(t *testing.T) {
	var rule Rule
	if err := rule.Apply(validInput(), orgA); err != nil {
		t.Fatal(err)
	}
	rule.ID = "00000000-0000-0000-0000-0000000000c1"
	// Store.Create seeds the sustain window; a bare Apply leaves it at zero, so
	// the fixture sets what the rendered rule should carry.
	rule.ForDurationSec = 60

	file := renderOne(t, OrgRules{OrgID: orgA, VMAccountID: 7, Rules: []Rule{rule}})
	gs := groups(t, file)
	if len(gs) != 1 || gs[0]["name"] != "klaro-org-"+orgA {
		t.Fatalf("groups = %v", gs)
	}
	rs := rules(t, gs[0])
	if len(rs) != 1 {
		t.Fatalf("rules = %v", rs)
	}

	labels := rs[0]["labels"].(map[string]any)
	// The account label is how the series vmalert writes gets back to the org's
	// tenant. Wave 3 measured the header path being lost inside an exporter's
	// goroutine hop, so the tenant travels as data here.
	if labels[LabelVMAccount] != "7" {
		t.Errorf("%s = %v, want the org's account", LabelVMAccount, labels[LabelVMAccount])
	}
	if labels[LabelOrgID] != orgA || labels[LabelRuleID] != rule.ID {
		t.Errorf("identity labels = %v", labels)
	}
	if rs[0]["for"] != "1m" {
		t.Errorf("for = %v, want 1m", rs[0]["for"])
	}
	expr := rs[0]["expr"].(string)
	if !strings.Contains(expr, `klaro_org_id="`+orgA+`"`) {
		t.Errorf("expr has no org matcher: %s", expr)
	}
	// The value has to be templated in, or the event history records that
	// something crossed a threshold without saying by how much.
	if ann := rs[0]["annotations"].(map[string]any); ann["klaro_value"] != "{{ $value }}" {
		t.Errorf("annotations = %v", ann)
	}
}

// Downsampling is the rollup pair, and the 5m rule must exclude the rollups or
// it would roll up its own output every interval.
func TestRenderRollupRulesExcludeThemselves(t *testing.T) {
	file := renderOne(t, OrgRules{OrgID: orgA, VMAccountID: 7, Downsample: true})
	rs := rules(t, groups(t, file)[0])
	if len(rs) != 2 {
		t.Fatalf("rollup rules = %d, want 2", len(rs))
	}

	byName := map[string]map[string]any{}
	for _, r := range rs {
		byName[r["record"].(string)] = r
	}
	five, ok := byName[explorer.Rollup5m]
	if !ok {
		t.Fatalf("no 5m rollup in %v", byName)
	}
	expr := five["expr"].(string)
	if !strings.Contains(expr, "__name__!~") {
		t.Errorf("5m rollup does not exclude the rollup series: %s", expr)
	}
	if !strings.Contains(expr, "keep_metric_names") || !strings.Contains(expr, explorer.MetricLabel) {
		t.Errorf("5m rollup does not preserve the original metric name: %s", expr)
	}

	hour, ok := byName[explorer.Rollup1h]
	if !ok {
		t.Fatalf("no 1h rollup in %v", byName)
	}
	// The hourly tier derives from the 5m tier: an hour of raw samples is twelve
	// times the work for an answer that is an average of averages either way.
	if !strings.Contains(hour["expr"].(string), explorer.Rollup5m) {
		t.Errorf("1h rollup does not derive from the 5m tier: %s", hour["expr"])
	}
	for _, r := range rs {
		if r["labels"].(map[string]any)[LabelVMAccount] != "7" {
			t.Errorf("rollup would land in the wrong tenant: %v", r["labels"])
		}
	}
}

// A disabled rule must not reach the evaluator, and an org with nothing to
// evaluate must not produce an empty group: vmalert warns about those on every
// reload, and a file full of warnings hides the real one.
func TestRenderSkipsDisabledRulesAndEmptyGroups(t *testing.T) {
	var rule Rule
	if err := rule.Apply(validInput(), orgA); err != nil {
		t.Fatal(err)
	}
	rule.ID = "00000000-0000-0000-0000-0000000000c1"
	rule.Enabled = false

	file := renderOne(t, OrgRules{OrgID: orgA, VMAccountID: 7, Rules: []Rule{rule}})
	if gs := groups(t, file); len(gs) != 0 {
		t.Errorf("groups = %v, want none", gs)
	}
}

// Two orgs are two groups, each pinned to its own tenant.
func TestRenderKeepsOrgsApart(t *testing.T) {
	const orgB = "00000000-0000-0000-0000-0000000000bb"
	raw, err := Render([]OrgRules{
		{OrgID: orgB, VMAccountID: 2, Downsample: true},
		{OrgID: orgA, VMAccountID: 1, Downsample: true},
	}, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	gs := groups(t, file)
	if len(gs) != 2 {
		t.Fatalf("groups = %d, want 2", len(gs))
	}
	// Sorted output keeps the file diffable, which is how an operator notices an
	// unexpected change.
	if gs[0]["name"] != "klaro-org-"+orgA {
		t.Errorf("groups are not in a stable order: %v", gs[0]["name"])
	}
	for _, g := range gs {
		name := g["name"].(string)
		org := strings.TrimPrefix(name, "klaro-org-")
		for _, r := range rules(t, g) {
			if !strings.Contains(r["expr"].(string), org) {
				t.Errorf("group %s evaluates an expression for another org: %s", name, r["expr"])
			}
		}
	}
}

func TestDurationString(t *testing.T) {
	cases := map[time.Duration]string{
		0:                "",
		30 * time.Second: "30s",
		time.Minute:      "1m",
		90 * time.Second: "90s",
		2 * time.Hour:    "2h",
	}
	for d, want := range cases {
		if got := durationString(d); got != want {
			t.Errorf("durationString(%v) = %q, want %q", d, got, want)
		}
	}
}

// The fingerprint is what makes a re-sent alert free. It must not depend on map
// iteration order.
func TestFingerprintIsStableAndLabelSensitive(t *testing.T) {
	a := Fingerprint("rule", map[string]string{"x": "1", "y": "2"})
	b := Fingerprint("rule", map[string]string{"y": "2", "x": "1"})
	if a != b {
		t.Error("fingerprint depends on map order")
	}
	if a == Fingerprint("rule", map[string]string{"x": "1"}) {
		t.Error("a different label set produced the same fingerprint")
	}
	if a == Fingerprint("other", map[string]string{"x": "1", "y": "2"}) {
		t.Error("a different rule produced the same fingerprint")
	}
	// Two labels whose concatenation collides must not: the separators exist for
	// exactly this.
	if Fingerprint("r", map[string]string{"ab": "c"}) == Fingerprint("r", map[string]string{"a": "bc"}) {
		t.Error("label boundaries are not encoded")
	}
}
