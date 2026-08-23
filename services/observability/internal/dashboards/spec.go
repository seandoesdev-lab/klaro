// Package dashboards stores saved panel layouts (OBS-09, design HOW-3).
//
// A dashboard is one JSONB document: panels, each referencing an Explorer
// query. There is no dashboard_panels table, because a panel has no identity
// outside the dashboard that contains it, and rendering happens in the klaro
// frontend against the Explorer API - this is not a Grafana embed, so nothing
// here needs to be addressable by a third party.
//
// Panel queries are validated on write with the same rules the Explorer
// enforces on read. That is the point of the package: without it a dashboard
// would be a way to persist a query the Explorer would refuse - including one
// naming a reserved label - and the refusal would only surface later, to
// whoever opened the dashboard.
package dashboards

import (
	"errors"
	"fmt"
	"time"

	"github.com/klaro/observability/internal/explorer"
)

// Signals a panel can query.
const (
	SignalMetrics = "metrics"
	SignalTraces  = "traces"
	SignalLogs    = "logs"
)

// Signals lists the accepted values, for error messages.
var Signals = []string{SignalMetrics, SignalTraces, SignalLogs}

// PanelTypes is the rendering hint the frontend switches on. A whitelist, so an
// unknown type cannot be stored and then fail to render.
var PanelTypes = []string{"timeseries", "stat", "table", "logs", "traces"}

// Bounds. Each exists because a dashboard is caller-supplied JSON stored in a
// row that is read on every page load.
const (
	MaxPanels      = 50
	MaxNameLen     = 120
	MaxDescription = 1000
	maxTitleLen    = 200
	maxPanelIDLen  = 64
	maxFilters     = 20
	// gridWidth is the column count the layout is expressed in. Bounding it
	// keeps a panel from claiming a width the frontend cannot lay out.
	gridWidth      = 12
	maxGridRow     = 200
	minStepSec     = 1
	maxStepSec     = 24 * 3600
	maxRangeSec    = 90 * 24 * 3600
	minRefreshSec  = 5
	maxPanelLimit  = 1000
	maxServiceLen  = 256
	maxContainsLen = 512
)

// ErrInvalidSpec is a caller mistake in the dashboard document.
var ErrInvalidSpec = errors.New("invalid dashboard spec")

// Layout places a panel on the grid.
type Layout struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// PanelQuery is the Explorer query a panel renders.
//
// One struct for all three signals rather than a union type: it round-trips
// through JSONB without a discriminated decoder, and the validator rejects the
// fields that do not belong to the chosen signal, so an ignored field cannot
// quietly mean something.
type PanelQuery struct {
	Signal string `json:"signal"`

	// Metrics.
	Metric  string             `json:"metric,omitempty"`
	Filters []explorer.Matcher `json:"filters,omitempty"`
	Agg     string             `json:"agg,omitempty"`
	StepSec int                `json:"step_sec,omitempty"`

	// Traces.
	Service       string `json:"service,omitempty"`
	MinDurationMS int    `json:"min_duration_ms,omitempty"`

	// Logs.
	Contains string `json:"contains,omitempty"`

	// Traces and logs.
	Limit int `json:"limit,omitempty"`
}

// Panel is one tile.
type Panel struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Type   string     `json:"type"`
	Layout Layout     `json:"layout"`
	Query  PanelQuery `json:"query"`
}

// Spec is the stored document.
type Spec struct {
	Panels []Panel `json:"panels"`
	// RangeSec is the lookback the dashboard opens with. Stored rather than
	// computed so a dashboard built for a daily view does not open on an hour.
	RangeSec int `json:"range_sec,omitempty"`
	// RefreshSec is how often the frontend re-queries. Zero means manual.
	RefreshSec int `json:"refresh_sec,omitempty"`
}

// Validate checks the whole document.
func (s *Spec) Validate() error {
	if s.Panels == nil {
		s.Panels = []Panel{}
	}
	if len(s.Panels) > MaxPanels {
		return fmt.Errorf("%w: at most %d panels", ErrInvalidSpec, MaxPanels)
	}
	if s.RangeSec < 0 || s.RangeSec > maxRangeSec {
		return fmt.Errorf("%w: range_sec must be between 0 and %d", ErrInvalidSpec, maxRangeSec)
	}
	if s.RefreshSec != 0 && s.RefreshSec < minRefreshSec {
		// A one-second refresh across a dozen panels is a self-inflicted load
		// test, not a dashboard.
		return fmt.Errorf("%w: refresh_sec must be 0 (manual) or at least %d", ErrInvalidSpec, minRefreshSec)
	}

	seen := make(map[string]bool, len(s.Panels))
	for i := range s.Panels {
		p := &s.Panels[i]
		if err := p.validate(); err != nil {
			return fmt.Errorf("panel %d: %w", i, err)
		}
		if seen[p.ID] {
			// Panel ids address a tile in a URL fragment and in the frontend's
			// reconciliation; duplicates make both ambiguous.
			return fmt.Errorf("%w: duplicate panel id %q", ErrInvalidSpec, p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

func (p *Panel) validate() error {
	if p.ID == "" || len(p.ID) > maxPanelIDLen || !simpleIdent(p.ID) {
		return fmt.Errorf("%w: id must be 1-%d characters of [A-Za-z0-9_-]", ErrInvalidSpec, maxPanelIDLen)
	}
	if len(p.Title) > maxTitleLen {
		return fmt.Errorf("%w: title must be at most %d characters", ErrInvalidSpec, maxTitleLen)
	}
	if !contains(PanelTypes, p.Type) {
		return fmt.Errorf("%w: type must be one of %v, got %q", ErrInvalidSpec, PanelTypes, p.Type)
	}
	if err := p.Layout.validate(); err != nil {
		return err
	}
	return p.Query.validate()
}

func (l Layout) validate() error {
	if l.X < 0 || l.W < 1 || l.X+l.W > gridWidth {
		return fmt.Errorf("%w: layout must fit the %d column grid", ErrInvalidSpec, gridWidth)
	}
	if l.Y < 0 || l.Y > maxGridRow || l.H < 1 || l.H > maxGridRow {
		return fmt.Errorf("%w: layout row and height must be between 1 and %d", ErrInvalidSpec, maxGridRow)
	}
	return nil
}

// validate applies the Explorer's own rules to a stored query.
func (q *PanelQuery) validate() error {
	if !contains(Signals, q.Signal) {
		return fmt.Errorf("%w: signal must be one of %v, got %q", ErrInvalidSpec, Signals, q.Signal)
	}
	if len(q.Filters) > maxFilters {
		return fmt.Errorf("%w: at most %d filters", ErrInvalidSpec, maxFilters)
	}
	for _, m := range q.Filters {
		if !explorer.ValidLabelName(m.Label) {
			return fmt.Errorf("%w: %q is not a valid label name", ErrInvalidSpec, m.Label)
		}
		if explorer.ReservedLabel(m.Label) {
			// The org matcher is the server's to set. A panel that could restate
			// it could restate it as another org - and it would be stored.
			return fmt.Errorf("%w: label %q is reserved", ErrInvalidSpec, m.Label)
		}
	}
	if q.Limit < 0 || q.Limit > maxPanelLimit {
		return fmt.Errorf("%w: limit must be between 0 and %d", ErrInvalidSpec, maxPanelLimit)
	}

	switch q.Signal {
	case SignalMetrics:
		return q.validateMetrics()
	case SignalTraces:
		return q.validateTraces()
	default:
		return q.validateLogs()
	}
}

func (q *PanelQuery) validateMetrics() error {
	if q.Metric == "" && len(q.Filters) == 0 {
		// Otherwise the only matcher left is the injected org one, and the panel
		// reads every series the tenant has ever written.
		return fmt.Errorf("%w: a metrics panel needs a metric or at least one filter", ErrInvalidSpec)
	}
	if q.Metric != "" && !explorer.ValidMetricName(q.Metric) {
		return fmt.Errorf("%w: %q is not a valid metric name", ErrInvalidSpec, q.Metric)
	}
	if q.StepSec != 0 && (q.StepSec < minStepSec || q.StepSec > maxStepSec) {
		return fmt.Errorf("%w: step_sec must be between %d and %d", ErrInvalidSpec, minStepSec, maxStepSec)
	}
	step := time.Duration(q.StepSec) * time.Second
	if step <= 0 {
		step = time.Minute
	}
	// Rendering the aggregation now is the cheapest way to reject one the
	// Explorer would refuse later, and it keeps the whitelist in one place.
	if _, err := explorer.Aggregate(q.Agg, "probe{}", step); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSpec, err)
	}
	if q.Service != "" || q.MinDurationMS != 0 || q.Contains != "" {
		return fmt.Errorf("%w: service, min_duration_ms and contains do not apply to a metrics panel", ErrInvalidSpec)
	}
	return nil
}

func (q *PanelQuery) validateTraces() error {
	if q.MinDurationMS < 0 {
		return fmt.Errorf("%w: min_duration_ms must not be negative", ErrInvalidSpec)
	}
	if len(q.Service) > maxServiceLen || hasControl(q.Service) {
		return fmt.Errorf("%w: service is too long or contains control characters", ErrInvalidSpec)
	}
	if q.Metric != "" || q.Agg != "" || q.Contains != "" {
		return fmt.Errorf("%w: metric, agg and contains do not apply to a traces panel", ErrInvalidSpec)
	}
	return nil
}

func (q *PanelQuery) validateLogs() error {
	if len(q.Contains) > maxContainsLen || hasControl(q.Contains) {
		return fmt.Errorf("%w: contains is too long or has control characters", ErrInvalidSpec)
	}
	if q.Metric != "" || q.Agg != "" || q.Service != "" || q.MinDurationMS != 0 {
		return fmt.Errorf("%w: metric, agg, service and min_duration_ms do not apply to a logs panel", ErrInvalidSpec)
	}
	return nil
}

func simpleIdent(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
