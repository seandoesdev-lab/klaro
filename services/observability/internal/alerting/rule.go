// Package alerting owns metric alert rules: their storage, the vmalert rule
// groups rendered from them, and the events that come back when one fires
// (OBS-06/07, design HOW-1).
//
// Rules are stored in Postgres and evaluated by vmalert. Reusing vmalert means
// not reimplementing evaluation scheduling, the `for` duration, firing/resolved
// state and deduplication - all of which look simple until they are wrong at
// three in the morning.
//
// The rule's MetricsQL is always rendered here, never supplied by the caller.
// A caller-written expression is one whose org matcher the caller can remove,
// which is the same hole the Explorer closes (HOW-2).
package alerting

import (
	"errors"
	"fmt"
	"time"

	"github.com/klaro/observability/internal/explorer"
)

// Signals. The column allows log and trace so the schema does not need changing
// later, but vmalert evaluates metrics only, so those are refused at the edge
// rather than accepted into a rule that would never fire (HOW-1 trade-off).
const (
	SignalMetric = "metric"
	SignalLog    = "log"
	SignalTrace  = "trace"
)

// comparators maps the stored value onto a MetricsQL operator.
var comparators = map[string]string{
	"gt":  ">",
	"gte": ">=",
	"lt":  "<",
	"lte": "<=",
}

// Comparators lists the accepted values, for error messages.
var Comparators = []string{"gt", "gte", "lt", "lte"}

// Severities accepted by the schema.
var Severities = []string{"info", "warning", "critical"}

// Channel types the Notifier knows how to reach.
const (
	ChannelEmail = "email"
	ChannelSlack = "slack"
)

// Errors callers distinguish.
var (
	// ErrInvalidRule is a caller mistake: bad name, unknown comparator.
	ErrInvalidRule = errors.New("invalid alert rule")
	// ErrUnsupportedSignal is a signal the evaluator cannot serve. Separate
	// because it deserves its own message.
	ErrUnsupportedSignal = errors.New("unsupported alert signal")
	// ErrDuplicateName is a name collision inside the org.
	ErrDuplicateName = errors.New("an alert rule with that name already exists")
	// ErrNotFound is a rule that does not exist in the caller org.
	ErrNotFound = errors.New("alert rule not found")
)

const (
	maxNameLen       = 120
	maxForDuration   = 24 * time.Hour
	defaultForDur    = time.Minute
	defaultStep      = time.Minute
	maxChannels      = 10
	maxChannelTarget = 320 // an email address at its RFC ceiling
)

// QuerySpec is the structured query a rule evaluates, mirroring the Explorer's
// metrics parameters so a chart and an alert cannot mean different things.
type QuerySpec struct {
	Metric  string             `json:"metric"`
	Filters []explorer.Matcher `json:"filters,omitempty"`
	Agg     string             `json:"agg,omitempty"`
	// StepSec is the rollup window for Agg, in seconds: that is what the API
	// takes and what a JSONB column round-trips without a custom type.
	StepSec int `json:"step_sec,omitempty"`
}

// Step returns the aggregation window.
func (q QuerySpec) Step() time.Duration {
	if q.StepSec <= 0 {
		return defaultStep
	}
	return time.Duration(q.StepSec) * time.Second
}

// Channel is one notification destination.
type Channel struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

// Rule is an alert rule as the API exposes it.
type Rule struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Signal    string    `json:"signal"`
	QuerySpec QuerySpec `json:"query_spec"`
	// Query is the rendered MetricsQL. Returned so the org matcher this package
	// injects is inspectable rather than merely promised.
	Query          string    `json:"query"`
	Comparator     string    `json:"comparator"`
	Threshold      float64   `json:"threshold"`
	ForDurationSec int       `json:"for_duration_sec"`
	Severity       string    `json:"severity"`
	Channels       []Channel `json:"channels"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ForDuration returns the sustain window.
func (r Rule) ForDuration() time.Duration {
	return time.Duration(r.ForDurationSec) * time.Second
}

// Input is the caller-supplied form. Pointer fields distinguish "absent" from
// "set to zero", so PATCH can be a partial update.
type Input struct {
	Name           *string    `json:"name"`
	Signal         *string    `json:"signal"`
	QuerySpec      *QuerySpec `json:"query_spec"`
	Comparator     *string    `json:"comparator"`
	Threshold      *float64   `json:"threshold"`
	ForDurationSec *int       `json:"for_duration_sec"`
	Severity       *string    `json:"severity"`
	Channels       *[]Channel `json:"channels"`
	Enabled        *bool      `json:"enabled"`
}

// Apply overlays the input onto a rule, then validates and re-renders it.
//
// Validation and rendering are one step on purpose: a rule whose stored
// MetricsQL disagreed with its stored spec would evaluate something nobody
// asked for.
func (r *Rule) Apply(in Input, orgID string) error {
	if in.Name != nil {
		r.Name = trimSpace(*in.Name)
	}
	if in.Signal != nil {
		r.Signal = *in.Signal
	}
	if in.QuerySpec != nil {
		r.QuerySpec = *in.QuerySpec
	}
	if in.Comparator != nil {
		r.Comparator = *in.Comparator
	}
	if in.Threshold != nil {
		r.Threshold = *in.Threshold
	}
	if in.ForDurationSec != nil {
		r.ForDurationSec = *in.ForDurationSec
	}
	if in.Severity != nil {
		r.Severity = *in.Severity
	}
	if in.Channels != nil {
		r.Channels = *in.Channels
	}
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	return r.validateAndRender(orgID)
}

func (r *Rule) validateAndRender(orgID string) error {
	if r.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidRule)
	}
	if len(r.Name) > maxNameLen {
		return fmt.Errorf("%w: name must be at most %d characters", ErrInvalidRule, maxNameLen)
	}

	if r.Signal == "" {
		r.Signal = SignalMetric
	}
	if r.Signal != SignalMetric {
		// vmalert is metric-only. Storing a log rule would store something that
		// silently never fires, which is worse than refusing it.
		return fmt.Errorf("%w: %q; only %q is supported (the evaluator is metric-only)",
			ErrUnsupportedSignal, r.Signal, SignalMetric)
	}

	if _, ok := comparators[r.Comparator]; !ok {
		return fmt.Errorf("%w: comparator must be one of %v, got %q", ErrInvalidRule, Comparators, r.Comparator)
	}
	if r.Severity == "" {
		r.Severity = "warning"
	} else if !validSeverity(r.Severity) {
		return fmt.Errorf("%w: severity must be one of %v, got %q", ErrInvalidRule, Severities, r.Severity)
	}

	// No default here: Store.Create seeds it, so an explicit zero survives and
	// means "fire on the first breach" rather than silently becoming a minute.
	if d := r.ForDuration(); d < 0 || d > maxForDuration {
		return fmt.Errorf("%w: for_duration_sec must be between 0 and %d",
			ErrInvalidRule, int(maxForDuration.Seconds()))
	}

	if err := validateChannels(r.Channels); err != nil {
		return err
	}

	query, err := RenderQuery(r.QuerySpec, orgID)
	if err != nil {
		return err
	}
	r.Query = query
	return nil
}

// RenderQuery builds the MetricsQL a rule evaluates, with the org matcher
// forced in.
//
// It reuses the Explorer's builder rather than assembling a string here, so a
// rule and a chart of the same thing produce the same query - and so the
// reserved-label and quoting rules cannot drift between the two surfaces.
func RenderQuery(spec QuerySpec, orgID string) (string, error) {
	if spec.Metric == "" && len(spec.Filters) == 0 {
		return "", fmt.Errorf("%w: query_spec needs a metric or at least one filter", ErrInvalidRule)
	}
	if spec.Metric != "" && !explorer.ValidMetricName(spec.Metric) {
		return "", fmt.Errorf("%w: %q is not a valid metric name", ErrInvalidRule, spec.Metric)
	}
	for _, m := range spec.Filters {
		if !explorer.ValidLabelName(m.Label) {
			return "", fmt.Errorf("%w: %q is not a valid label name", ErrInvalidRule, m.Label)
		}
		if explorer.ReservedLabel(m.Label) {
			return "", fmt.Errorf("%w: label %q is reserved", ErrInvalidRule, m.Label)
		}
	}
	selector := explorer.Selector(spec.Metric, orgID, spec.Filters)
	rendered, err := explorer.Aggregate(spec.Agg, selector, spec.Step())
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRule, err)
	}
	return rendered, nil
}

// Expression renders the condition vmalert evaluates: the query compared
// against the threshold.
func (r Rule) Expression() (string, error) {
	op, ok := comparators[r.Comparator]
	if !ok {
		return "", fmt.Errorf("%w: comparator %q", ErrInvalidRule, r.Comparator)
	}
	return fmt.Sprintf("%s %s %v", r.Query, op, r.Threshold), nil
}

func validSeverity(s string) bool {
	for _, known := range Severities {
		if s == known {
			return true
		}
	}
	return false
}

func validateChannels(channels []Channel) error {
	if len(channels) > maxChannels {
		return fmt.Errorf("%w: at most %d channels", ErrInvalidRule, maxChannels)
	}
	for _, ch := range channels {
		if ch.Type != ChannelEmail && ch.Type != ChannelSlack {
			return fmt.Errorf("%w: channel type must be %q or %q, got %q",
				ErrInvalidRule, ChannelEmail, ChannelSlack, ch.Type)
		}
		if ch.Target == "" || len(ch.Target) > maxChannelTarget {
			return fmt.Errorf("%w: channel target is required and must be at most %d characters",
				ErrInvalidRule, maxChannelTarget)
		}
	}
	return nil
}

func trimSpace(s string) string {
	isSpace := func(b byte) bool {
		return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
	}
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}
