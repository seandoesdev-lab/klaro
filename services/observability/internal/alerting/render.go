package alerting

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/klaro/observability/internal/explorer"
)

// Labels this package puts on every generated rule. The webhook receiver reads
// the first two to decide which org's history an alert belongs to - it never
// trusts anything else in the payload, because everything else came from a
// metric a customer produced.
const (
	LabelOrgID  = "klaro_org_id"
	LabelRuleID = "klaro_rule_id"
	// LabelVMAccount routes the series vmalert itself writes (alert state and
	// rollups) back into the org's VictoriaMetrics tenant. This is the label
	// path, not the header path: wave 3 measured the header being lost inside
	// the exporter's goroutine hop, and a lost tenant means the shared default
	// account.
	LabelVMAccount = "vm_account_id"
	// LabelMetric carries the original metric name of a rollup series.
	LabelMetric = "klaro_metric"
	// LabelResolution says which rollup a series is.
	LabelResolution = "klaro_resolution"
)

// Rollup series names and their windows (design HOW-10). Downsampling is
// metrics-only: traces and logs have no standard notion of it, and pretending
// otherwise would set an expectation the backends cannot meet.
const (
	Rollup5m = "klaro_rollup5m"
	Rollup1h = "klaro_rollup1h"
)

// Window5m and Window1h are the aggregation windows of the two rollup tiers.
const (
	Window5m = 5 * time.Minute
	Window1h = time.Hour
)

// rawSelector matches an org's raw series, excluding the rollups themselves.
//
// The exclusion is not optional: without it the 5m rule would roll up its own
// output every interval, and the series count would grow without bound.
func rawSelector(orgID string) string {
	return fmt.Sprintf("{%s=%s,__name__!~%q}", LabelOrgID, strconv.Quote(orgID), Rollup5m+"|"+Rollup1h)
}

// RollupExpr renders the recording-rule expression for the 5m tier.
//
// keep_metric_names is what makes the original name available to label_replace;
// the recording rule then overwrites __name__ with the rollup name, so the
// original has to be parked in a label first or it is lost. One rollup series
// name with the metric in a label keeps cardinality identical to the raw set.
func RollupExpr(orgID string) string {
	return fmt.Sprintf(`label_replace(avg_over_time(%s[%s]) keep_metric_names, %q, "$1", "__name__", "(.+)")`,
		rawSelector(orgID), "5m", LabelMetric)
}

// RollupExpr1h derives the hourly tier from the 5m tier rather than from raw
// data: an hour of raw samples is twelve times the work for an answer that is
// already an average of averages either way.
func RollupExpr1h(orgID string) string {
	return fmt.Sprintf("avg_over_time(%s{%s=%s}[1h])", Rollup5m, LabelOrgID, strconv.Quote(orgID))
}

// vmRule is one entry in a vmalert rule group. Only the fields this package
// sets are modelled; omitempty keeps the rendered file readable.
type vmRule struct {
	Alert       string            `json:"alert,omitempty"`
	Record      string            `json:"record,omitempty"`
	Expr        string            `json:"expr"`
	For         string            `json:"for,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type vmGroup struct {
	Name     string   `json:"name"`
	Interval string   `json:"interval,omitempty"`
	Rules    []vmRule `json:"rules"`
}

type vmRuleFile struct {
	Groups []vmGroup `json:"groups"`
}

// OrgRules is everything needed to render one org's group.
type OrgRules struct {
	OrgID       string
	VMAccountID uint32
	Rules       []Rule
	// Downsample adds the rollup recording rules for this org (HOW-10).
	Downsample bool
}

// RenderOptions tunes the generated file.
type RenderOptions struct {
	// Interval is how often vmalert evaluates each group.
	Interval time.Duration
	// DashboardBaseURL is prefixed to the dashboard link in an annotation, so a
	// notification can carry somewhere to click.
	DashboardBaseURL string
}

const defaultInterval = 30 * time.Second

// Render produces the vmalert rule file for every org.
//
// The output is JSON, and the file is named .yaml. That is not a trick: YAML is
// a superset of JSON, so vmalert's parser accepts it, and emitting JSON means
// the standard library does the escaping. Hand-rolling YAML quoting for values
// that contain a customer's label strings is exactly the kind of code that
// produces a parse error in production and nowhere else.
func Render(orgs []OrgRules, opts RenderOptions) ([]byte, error) {
	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	}
	file := vmRuleFile{Groups: []vmGroup{}}

	sorted := append([]OrgRules(nil), orgs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].OrgID < sorted[j].OrgID })

	for _, org := range sorted {
		group := vmGroup{
			Name:     "klaro-org-" + org.OrgID,
			Interval: durationString(opts.Interval),
			Rules:    []vmRule{},
		}
		tenant := strconv.FormatUint(uint64(org.VMAccountID), 10)

		if org.Downsample {
			group.Rules = append(group.Rules,
				vmRule{
					Record: Rollup5m,
					Expr:   RollupExpr(org.OrgID),
					Labels: map[string]string{
						LabelVMAccount:  tenant,
						LabelOrgID:      org.OrgID,
						LabelResolution: "5m",
					},
				},
				vmRule{
					Record: Rollup1h,
					Expr:   RollupExpr1h(org.OrgID),
					Labels: map[string]string{
						LabelVMAccount:  tenant,
						LabelOrgID:      org.OrgID,
						LabelResolution: "1h",
					},
				},
			)
		}

		for _, r := range org.Rules {
			if !r.Enabled || r.Signal != SignalMetric {
				continue
			}
			expr, err := r.Expression()
			if err != nil {
				return nil, fmt.Errorf("render rule %s: %w", r.ID, err)
			}
			rule := vmRule{
				Alert: r.Name,
				Expr:  expr,
				For:   durationString(r.ForDuration()),
				Labels: map[string]string{
					LabelVMAccount: tenant,
					LabelOrgID:     org.OrgID,
					LabelRuleID:    r.ID,
					"severity":     r.Severity,
				},
				Annotations: map[string]string{
					"klaro_threshold":  fmt.Sprintf("%v", r.Threshold),
					"klaro_comparator": r.Comparator,
					// Alertmanager alerts carry no numeric value, so the one the
					// rule tripped on has to be templated into an annotation or
					// the event history records "something crossed a threshold"
					// without saying by how much.
					"klaro_value": "{{ $value }}",
				},
			}
			if opts.DashboardBaseURL != "" {
				rule.Annotations["klaro_dashboard_url"] =
					fmt.Sprintf("%s/orgs/%s/obs/alerts/%s", opts.DashboardBaseURL, org.OrgID, r.ID)
			}
			group.Rules = append(group.Rules, rule)
		}

		// A group with no rules is not written: vmalert logs an empty group as a
		// warning on every reload, and a file full of them hides real problems.
		if len(group.Rules) > 0 {
			file.Groups = append(file.Groups, group)
		}
	}

	return json.MarshalIndent(file, "", "  ")
}

// durationString renders a Go duration in the form vmalert reads.
func durationString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d%time.Hour == 0 {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	if d%time.Minute == 0 {
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

// RollupSelector builds the Explorer's fallback selector: the rollup series for
// one metric, with the org matcher forced in exactly as the raw path does.
func RollupSelector(series, metric, orgID string, filters []explorer.Matcher) string {
	all := append([]explorer.Matcher(nil), filters...)
	if metric != "" {
		all = append(all, explorer.Matcher{Label: LabelMetric, Value: metric})
	}
	return explorer.Selector(series, orgID, all)
}
