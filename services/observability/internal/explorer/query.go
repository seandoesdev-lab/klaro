package explorer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OrgLabel is the label every stored signal carries, stamped by the Collector
// from the control-plane-issued org (klarotenant sets klaro.org_id; the
// backends normalise the dot to an underscore).
//
// It is injected into every query as a second line of defence behind the
// backend's own tenancy. Belt and braces on purpose: the tenant header is one
// forgotten map lookup away from being absent, and the failure mode of that
// mistake is another org's data in someone's dashboard.
const OrgLabel = "klaro_org_id"

// reservedLabels may never come from a caller. Each one either selects the
// tenant or names the metric, so accepting a caller-supplied copy would let a
// query argue with the matcher this package injects.
var reservedLabels = map[string]bool{
	OrgLabel:        true,
	"vm_account_id": true,
	"vm_project_id": true,
	"__name__":      true,
}

// Aggregations is the whitelist (design HOW-2). Anything outside it is a 422,
// not a passthrough.
var Aggregations = []string{"rate", "avg", "sum", "count", "p50", "p95", "p99"}

// maxLabelValue bounds a matcher value. Long values are not a security problem
// once quoted, but they are a sign of someone trying to smuggle a query.
const maxLabelValue = 512

// Matcher is one label condition.
//
// The json tags matter: matchers are not only parsed from query parameters, they
// are also stored inside an alert rule spec and a dashboard panel, where a
// capitalised key would leak Go naming into a customer-facing document.
type Matcher struct {
	Label  string `json:"label"`
	Negate bool   `json:"negate,omitempty"`
	Value  string `json:"value"`
}

// ValidLabelName reports whether s is a label a caller may name.
func ValidLabelName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		alpha := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
		if i == 0 && !alpha {
			return false
		}
		if i > 0 && !alpha && !(ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

// ValidMetricName is ValidLabelName plus the colon Prometheus allows in
// recording-rule names.
func ValidMetricName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	return ValidLabelName(strings.ReplaceAll(s, ":", "_"))
}

// validLabelValue rejects control characters. Quoting alone would make them
// safe, but a newline in a label value has no legitimate source and every
// illegitimate one.
func validLabelValue(s string) bool {
	if len(s) > maxLabelValue {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ParseFilters reads the repeated filter parameter: label=value or label!=value.
//
// Values are taken literally - no globs, no regex. A caller that needs a regex
// match today would need a way to say so that cannot also say "match
// everything", and that is a deliberate later decision.
func ParseFilters(raw []string) ([]Matcher, error) {
	var out []Matcher
	for _, item := range raw {
		for _, part := range strings.Split(item, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			m, err := parseMatcher(part)
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
	}
	return out, nil
}

func parseMatcher(part string) (Matcher, error) {
	negate := false
	sep := "="
	if i := strings.Index(part, "!="); i >= 0 {
		negate, sep = true, "!="
	}
	idx := strings.Index(part, sep)
	if idx <= 0 {
		return Matcher{}, fmt.Errorf("%w: filter %q must be label=value or label!=value", ErrInvalidQuery, part)
	}
	// Spaces around the separator are formatting, not data: these arrive in a
	// URL query parameter, where "a = b" is far more likely to be a human
	// writing a list than a label value that genuinely ends in a space.
	label := strings.TrimSpace(part[:idx])
	value := strings.TrimSpace(part[idx+len(sep):])

	if !ValidLabelName(label) {
		return Matcher{}, fmt.Errorf("%w: %q is not a valid label name", ErrInvalidQuery, label)
	}
	if reservedLabels[label] {
		// The org matcher is this package's to set. A caller that could restate
		// it could also restate it as another org.
		return Matcher{}, fmt.Errorf("%w: label %q is reserved", ErrInvalidQuery, label)
	}
	if !validLabelValue(value) {
		return Matcher{}, fmt.Errorf("%w: value for %q is too long or contains control characters", ErrInvalidQuery, label)
	}
	return Matcher{Label: label, Negate: negate, Value: value}, nil
}

// Selector renders matchers as a PromQL/LogQL series selector with the org
// matcher forced in front.
//
// strconv.Quote does the escaping. It emits a Go string literal, and PromQL,
// LogQL and TraceQL all use the same double-quoted form with backslash escapes,
// so a value containing a quote or a backslash cannot terminate the literal and
// start a new matcher.
func Selector(metric, orgID string, matchers []Matcher) string {
	parts := make([]string, 0, len(matchers)+1)
	parts = append(parts, OrgLabel+"="+strconv.Quote(orgID))

	sorted := append([]Matcher(nil), matchers...)
	// Stable output keeps the generated query deterministic, which is what
	// makes it assertable in a test and readable in a log.
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Label < sorted[j].Label })
	for _, m := range sorted {
		op := "="
		if m.Negate {
			op = "!="
		}
		parts = append(parts, m.Label+op+strconv.Quote(m.Value))
	}
	return metric + "{" + strings.Join(parts, ",") + "}"
}

// Aggregate wraps a selector in a whitelisted over-time rollup.
//
// Every entry is an *_over_time form rather than a cross-series aggregation:
// the API takes a step, so the caller is asking "summarise each series over
// this bucket", not "collapse these series together". Collapsing would need a
// grouping parameter to be meaningful, and there is not one yet.
func Aggregate(agg, selector string, step time.Duration) (string, error) {
	if agg == "" {
		return selector, nil
	}
	if step <= 0 {
		return "", fmt.Errorf("%w: step is required when agg is set", ErrInvalidQuery)
	}
	window := strconv.FormatInt(int64(step.Seconds()), 10) + "s"

	switch agg {
	case "rate":
		return fmt.Sprintf("rate(%s[%s])", selector, window), nil
	case "avg":
		return fmt.Sprintf("avg_over_time(%s[%s])", selector, window), nil
	case "sum":
		return fmt.Sprintf("sum_over_time(%s[%s])", selector, window), nil
	case "count":
		return fmt.Sprintf("count_over_time(%s[%s])", selector, window), nil
	case "p50", "p95", "p99":
		q := "0." + strings.TrimPrefix(agg, "p")
		return fmt.Sprintf("quantile_over_time(%s, %s[%s])", q, selector, window), nil
	default:
		return "", fmt.Errorf("%w: agg must be one of %v, got %q", ErrInvalidQuery, Aggregations, agg)
	}
}

// stripInternalLabels removes the routing plumbing from a result before it goes
// out. A caller already knows its own org, and the VictoriaMetrics tenant is an
// internal number it must never build anything on.
func stripInternalLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if k == OrgLabel || k == "vm_account_id" || k == "vm_project_id" {
			continue
		}
		out[k] = v
	}
	return out
}

// ReservedLabel reports whether a label is one this package sets itself.
//
// Exported because the alerting renderer builds queries too, and it must refuse
// exactly the same labels: a rule that could restate the org matcher could
// restate it as another org.
func ReservedLabel(label string) bool { return reservedLabels[label] }
