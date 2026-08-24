package explorer

// Infrastructure read path: the host vitals and uptime queries that back
// GET /obs/hosts and the hostmap (P1b).
//
// It stays inside the explorer package for one reason: this is the only place
// allowed to author MetricsQL, and every query below is authored here rather
// than accepted from a caller. The org matcher goes on through the same
// Selector the Explorer uses, and the VictoriaMetrics tenant is still the path
// segment the mapper resolves - so an infrastructure query is scoped by exactly
// the same two independent mechanisms as a metrics query.
//
// The series names are hostmetrics receiver output as the Prometheus remote
// write exporter renames it (dots to underscores, unit suffixes appended).
// They are constants rather than literals inside the query strings because
// they are the one part of this file that is a property of the collector
// configuration (deploy/otel-hostmetrics.yaml) rather than of this package:
// change the scraper set there and these are what has to move with it.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/klaro/observability/internal/tenants"
)

// Series written by the hostmetrics receiver (deploy/otel-hostmetrics.yaml).
const (
	// MetricCPUUtilization is a 0..1 ratio per cpu and per state.
	MetricCPUUtilization = "system_cpu_utilization_ratio"
	// MetricMemoryUtilization is a 0..1 ratio per memory state.
	MetricMemoryUtilization = "system_memory_utilization_ratio"
	// MetricFilesystemUtilization is a 0..1 ratio per mounted filesystem.
	MetricFilesystemUtilization = "system_filesystem_utilization_ratio"
	// MetricLoad1 is the 1 minute run queue length.
	MetricLoad1 = "system_cpu_load_average_1m"
	// MetricNetworkIO is a cumulative byte counter per interface and direction
	// (direction=receive|transmit).
	MetricNetworkIO = "system_network_io_bytes_total"
	// MetricDiskIO is a cumulative byte counter per block device and direction
	// (direction=read|write).
	MetricDiskIO = "system_disk_io_bytes_total"
)

// HostMetricLabel is the label a host series is grouped by.
//
// The remote write exporter sets `instance` from service.instance.id, which is
// the same normalised identity the gateway reports to the control plane as
// observability_hosts.host_ident (design HOW-4). That agreement is what lets
// the registry row and the time series be joined at all, so the agent config
// derives service.instance.id from host.name and this reads it back.
const HostMetricLabel = "instance"

// stateLabel names the sub-dimension a utilisation ratio breaks down by.
const stateLabel = "state"

// DefaultHostLookback is how stale the newest sample may be and still count as
// the host's current reading.
const DefaultHostLookback = 5 * time.Minute

// HostVitals is the latest reading for one host. Every field is a pointer
// because "this host reports no filesystem metrics" and "this host is at 0%"
// are different facts, and a zero would tell the second story for both.
type HostVitals struct {
	CPUPct  *float64 `json:"cpu_pct"`
	MemPct  *float64 `json:"mem_pct"`
	DiskPct *float64 `json:"disk_pct"`
	Load1   *float64 `json:"load1"`
}

// vitalsQuery is one instant query and where its answer lands.
type vitalsQuery struct {
	// name identifies the reading in an error. Without it a failure reads as
	// "the inventory is broken" when what actually happened is that one of four
	// queries was refused.
	name string
	// build renders the MetricsQL with the org matcher already forced in.
	build func(orgID string) string
	// into assigns the value onto the right field of a host's vitals.
	into func(v *HostVitals, value float64)
}

// pct converts a 0..1 ratio into a percentage, clamped to a sane range. A
// ratio above 1 is a scraper reporting per-cpu totals; showing 340% on a cell
// would read as a bug rather than as the sum it is.
func pct(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Max(0, math.Min(100, v*100))
}

// vitalsQueries are the four readings a host row and a hostmap cell show.
//
// Each aggregates by HostMetricLabel so one host is one result whatever its cpu
// count or mount table looks like: avg across cpus, sum across the memory
// states that count as used, max across filesystems (the fullest disk is the
// one that will page someone).
var vitalsQueries = []vitalsQuery{
	{
		name: "cpu",
		build: func(orgID string) string {
			idle := Selector(MetricCPUUtilization, orgID, []Matcher{{Label: stateLabel, Value: "idle"}})
			return "(1 - avg by (" + HostMetricLabel + ") (" + idle + "))"
		},
		into: func(v *HostVitals, value float64) { p := pct(value); v.CPUPct = &p },
	},
	{
		name: "mem",
		build: func(orgID string) string {
			used := Selector(MetricMemoryUtilization, orgID, []Matcher{{Label: stateLabel, Value: "used"}})
			return "sum by (" + HostMetricLabel + ") (" + used + ")"
		},
		into: func(v *HostVitals, value float64) { p := pct(value); v.MemPct = &p },
	},
	{
		name: "disk",
		build: func(orgID string) string {
			used := Selector(MetricFilesystemUtilization, orgID, []Matcher{{Label: stateLabel, Value: "used"}})
			return "max by (" + HostMetricLabel + ") (" + used + ")"
		},
		into: func(v *HostVitals, value float64) { p := pct(value); v.DiskPct = &p },
	},
	{
		name: "load1",
		build: func(orgID string) string {
			return "max by (" + HostMetricLabel + ") (" + Selector(MetricLoad1, orgID, nil) + ")"
		},
		into: func(v *HostVitals, value float64) { load := value; v.Load1 = &load },
	},
}

// Vitals reads the latest cpu / memory / disk / load for every host that has
// reported inside the lookback window.
//
// It answers with what it could read rather than failing the whole inventory
// when one series is missing: a fleet where the filesystem scraper is off must
// still show cpu, and a backend that has never seen a host metric produces an
// empty map rather than an error, because "no data yet" is the normal state of
// a freshly installed agent.
func (c *Client) Vitals(ctx context.Context, orgID string, lookback time.Duration) (map[string]HostVitals, error) {
	if !c.cfg.Configured(c.cfg.VMSelectURL) {
		return nil, fmt.Errorf("%w: no metrics backend configured", ErrBackend)
	}
	account, err := c.tenants.VMAccountID(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve tenant: %v", ErrBackend, err)
	}
	if lookback <= 0 {
		lookback = DefaultHostLookback
	}

	out := map[string]HostVitals{}
	for _, q := range vitalsQueries {
		// last_over_time carries the most recent sample inside the window
		// forward. Without it an instant query answers only if a sample landed
		// within the backend's staleness interval, so a slow flush makes a live
		// host blink out of the inventory.
		expr := "last_over_time((" + q.build(orgID) + ")[" + durationLiteral(lookback) + "])"
		points, err := c.instant(ctx, account, expr, time.Now())
		if err != nil {
			return nil, fmt.Errorf("read %s vitals: %w", q.name, err)
		}
		for _, p := range points {
			host := p.labels[HostMetricLabel]
			if host == "" {
				continue
			}
			v := out[host]
			q.into(&v, p.value)
			out[host] = v
		}
	}
	return out, nil
}

// HostSeriesKeys are the series HostSeries returns, in display order.
var HostSeriesKeys = []string{
	"cpu_pct", "mem_pct", "disk_pct", "load1",
	"net_rx_bps", "net_tx_bps", "disk_read_bps", "disk_write_bps",
}

// HostSeriesResult is the detail chart payload for one host.
type HostSeriesResult struct {
	HostIdent string            `json:"host_ident"`
	Series    map[string]Series `json:"series"`
	Clamped   bool              `json:"clamped"`
	From      time.Time         `json:"from"`
	To        time.Time         `json:"to"`
	Queries   map[string]string `json:"queries"`
}

// HostSeries reads the per-host detail charts behind a hostmap cell.
//
// Unlike QueryMetrics this does not fall back to the rollup series: the
// downsampling rules cover named metrics, and a window reaching past raw
// retention is clamped instead. Showing a shorter window is honest; showing a
// rollup that was never recorded for these series would be an empty chart with
// no explanation.
func (c *Client) HostSeries(ctx context.Context, orgID, hostIdent string, rng TimeRange, step time.Duration) (HostSeriesResult, error) {
	if !c.cfg.Configured(c.cfg.VMSelectURL) {
		return HostSeriesResult{}, fmt.Errorf("%w: no metrics backend configured", ErrBackend)
	}
	if !validHostIdent(hostIdent) {
		return HostSeriesResult{}, fmt.Errorf("%w: host is required and must be a plain identifier", ErrInvalidQuery)
	}
	if err := rng.Validate(); err != nil {
		return HostSeriesResult{}, err
	}
	if step <= 0 {
		step = defaultStep
	}
	if n := rng.To.Sub(rng.From) / step; n > maxPoints {
		return HostSeriesResult{}, fmt.Errorf("%w: %d points per series exceeds the %d limit; widen step or narrow the range",
			ErrInvalidQuery, n, maxPoints)
	}

	account, err := c.tenants.VMAccountID(ctx, orgID)
	if err != nil {
		return HostSeriesResult{}, fmt.Errorf("%w: resolve tenant: %v", ErrBackend, err)
	}
	keep := c.retentionFor(ctx, orgID)
	rng, clamped := clamp(rng, keep.Metrics, time.Now())

	host := Matcher{Label: HostMetricLabel, Value: hostIdent}
	win := durationLiteral(step)
	exprs := map[string]string{
		"cpu_pct": "100 * (1 - avg (" +
			Selector(MetricCPUUtilization, orgID, []Matcher{host, {Label: stateLabel, Value: "idle"}}) + "))",
		"mem_pct": "100 * sum (" +
			Selector(MetricMemoryUtilization, orgID, []Matcher{host, {Label: stateLabel, Value: "used"}}) + ")",
		"disk_pct": "100 * max (" +
			Selector(MetricFilesystemUtilization, orgID, []Matcher{host, {Label: stateLabel, Value: "used"}}) + ")",
		"load1": "max (" + Selector(MetricLoad1, orgID, []Matcher{host}) + ")",
		"net_rx_bps": "sum (rate(" +
			Selector(MetricNetworkIO, orgID, []Matcher{host, {Label: "direction", Value: "receive"}}) + "[" + win + "]))",
		"net_tx_bps": "sum (rate(" +
			Selector(MetricNetworkIO, orgID, []Matcher{host, {Label: "direction", Value: "transmit"}}) + "[" + win + "]))",
		// Block device throughput. It is summed across devices rather than
		// broken out per device: a host detail view answers "is this machine
		// busy", and which of its twelve loop devices is busy is a question for
		// the metrics explorer, where the label is still there to group by.
		"disk_read_bps": "sum (rate(" +
			Selector(MetricDiskIO, orgID, []Matcher{host, {Label: "direction", Value: "read"}}) + "[" + win + "]))",
		"disk_write_bps": "sum (rate(" +
			Selector(MetricDiskIO, orgID, []Matcher{host, {Label: "direction", Value: "write"}}) + "[" + win + "]))",
	}

	out := HostSeriesResult{
		HostIdent: hostIdent,
		Series:    make(map[string]Series, len(exprs)),
		Clamped:   clamped,
		From:      rng.From,
		To:        rng.To,
		Queries:   exprs,
	}
	for _, key := range HostSeriesKeys {
		series, err := c.rangeQuery(ctx, account, exprs[key], rng, step)
		if err != nil {
			return HostSeriesResult{}, err
		}
		// Every expression collapses to a single series, so the first result is
		// the answer and an absent one is "this host reports no such metric".
		merged := Series{Labels: map[string]string{"series": key}, Points: []Sample{}}
		if len(series) > 0 {
			merged.Points = series[0].Points
		}
		out.Series[key] = merged
	}
	return out, nil
}

// UptimeHost is one host's achievement inside the SLO window.
type UptimeHost struct {
	HostIdent string `json:"host_ident"`
	// Observed is the number of buckets in which the host reported at least one
	// sample; Expected is how many buckets the window contains.
	Observed int `json:"observed_buckets"`
	Expected int `json:"expected_buckets"`
	// Availability is Observed/Expected as a 0..1 ratio.
	Availability float64 `json:"availability"`
	// Met reports whether Availability reached the target.
	Met bool `json:"met"`
}

// UptimeResult is the SLO widget payload.
type UptimeResult struct {
	// Target is the objective the achievement is measured against, 0..1.
	Target  float64      `json:"target"`
	StepSec int          `json:"step_sec"`
	From    time.Time    `json:"from"`
	To      time.Time    `json:"to"`
	Clamped bool         `json:"clamped"`
	Hosts   []UptimeHost `json:"hosts"`
	// Availability is the fleet-wide ratio: observed buckets over expected
	// buckets across every host. Deliberately not the mean of the per-host
	// ratios - that would weigh a host that reported for one hour the same as
	// one that reported all month.
	Availability float64 `json:"availability"`
	Observed     int     `json:"observed_buckets"`
	Expected     int     `json:"expected_buckets"`
	Query        string  `json:"query"`
}

// DefaultUptimeTarget is the objective used when a caller names none.
const DefaultUptimeTarget = 0.99

// maxUptimeBuckets bounds the bucket grid. A 31 day window at a 1 minute bucket
// is already 44640 points per host; finer than that is a timeout with extra
// steps.
const maxUptimeBuckets = 44640

// Uptime measures availability as reporting coverage [OBS-01].
//
// The definition is deliberate and narrow: a host is "up" in a bucket when at
// least one host metric sample landed inside it. That is what this platform can
// actually observe - there is no synthetic check and no agent heartbeat
// separate from the data, so anything else would infer liveness from a signal
// we do not have. count_over_time is used rather than a bare selector because
// an instant selector would let the backend's staleness interval carry a dead
// host forward and report it as up for another five minutes.
func (c *Client) Uptime(ctx context.Context, orgID string, rng TimeRange, step time.Duration, target float64, hostIdent string) (UptimeResult, error) {
	if !c.cfg.Configured(c.cfg.VMSelectURL) {
		return UptimeResult{}, fmt.Errorf("%w: no metrics backend configured", ErrBackend)
	}
	if err := rng.Validate(); err != nil {
		return UptimeResult{}, err
	}
	if step <= 0 {
		step = time.Minute
	}
	if target <= 0 || target > 1 {
		target = DefaultUptimeTarget
	}
	if hostIdent != "" && !validHostIdent(hostIdent) {
		return UptimeResult{}, fmt.Errorf("%w: host must be a plain identifier", ErrInvalidQuery)
	}
	if n := rng.To.Sub(rng.From) / step; n > maxUptimeBuckets {
		return UptimeResult{}, fmt.Errorf("%w: %d buckets exceeds the %d limit; widen step or narrow the range",
			ErrInvalidQuery, n, maxUptimeBuckets)
	}

	account, err := c.tenants.VMAccountID(ctx, orgID)
	if err != nil {
		return UptimeResult{}, fmt.Errorf("%w: resolve tenant: %v", ErrBackend, err)
	}
	keep := c.retentionFor(ctx, orgID)
	rng, clamped := clamp(rng, keep.Metrics, time.Now())

	matchers := []Matcher{{Label: stateLabel, Value: "idle"}}
	if hostIdent != "" {
		matchers = append(matchers, Matcher{Label: HostMetricLabel, Value: hostIdent})
	}
	expr := "count by (" + HostMetricLabel + ") (count_over_time(" +
		Selector(MetricCPUUtilization, orgID, matchers) + "[" + durationLiteral(step) + "]))"

	series, err := c.rangeQuery(ctx, account, expr, rng, step)
	if err != nil {
		return UptimeResult{}, err
	}

	// The bucket grid is derived, not counted: a host that reported for the
	// last hour of a 24 hour window must score 1/24, and counting only the
	// buckets that came back would score it 1.
	expected := int(rng.To.Sub(rng.From)/step) + 1
	if expected < 1 {
		expected = 1
	}

	out := UptimeResult{
		Target: target, StepSec: int(step.Seconds()),
		From: rng.From, To: rng.To, Clamped: clamped,
		Hosts: make([]UptimeHost, 0, len(series)), Query: expr,
	}
	for _, s := range series {
		host := s.Labels[HostMetricLabel]
		if host == "" {
			continue
		}
		observed := 0
		for _, p := range s.Points {
			if p.Value > 0 {
				observed++
			}
		}
		if observed > expected {
			observed = expected
		}
		ratio := float64(observed) / float64(expected)
		out.Hosts = append(out.Hosts, UptimeHost{
			HostIdent: host, Observed: observed, Expected: expected,
			Availability: ratio, Met: ratio >= target,
		})
		out.Observed += observed
		out.Expected += expected
	}
	sort.Slice(out.Hosts, func(i, j int) bool { return out.Hosts[i].HostIdent < out.Hosts[j].HostIdent })
	if out.Expected > 0 {
		out.Availability = float64(out.Observed) / float64(out.Expected)
	}
	return out, nil
}

// validHostIdent reuses the label-value rules: a host identity ends up inside a
// quoted matcher, so it has to survive the same escaping as any other value.
func validHostIdent(s string) bool {
	return s != "" && validLabelValue(s)
}

/* ------------------------------------------------------------- backend -- */

// instantPoint is one row of an instant query result.
type instantPoint struct {
	labels map[string]string
	value  float64
}

// vmInstantResponse is the Prometheus-compatible /query envelope.
type vmInstantResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string  `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// instant runs a single-timestamp query against the org's VM tenant.
func (c *Client) instant(ctx context.Context, account uint32, promQL string, at time.Time) ([]instantPoint, error) {
	params := url.Values{}
	params.Set("query", promQL)
	params.Set("time", strconv.FormatInt(at.Unix(), 10))

	endpoint := join(c.cfg.VMSelectURL,
		"/select/"+tenants.VMTenantPath(account)+"/prometheus/api/v1/query", params)

	var raw vmInstantResponse
	if err := c.getJSON(ctx, endpoint, nil, &raw); err != nil {
		return nil, err
	}
	if raw.Status != "" && raw.Status != "success" {
		return nil, fmt.Errorf("%w: instant query returned status %q", ErrBackend, raw.Status)
	}
	out := make([]instantPoint, 0, len(raw.Data.Result))
	for _, r := range raw.Data.Result {
		sample, ok := decodeSample(r.Value)
		if !ok {
			continue
		}
		out = append(out, instantPoint{labels: r.Metric, value: sample.Value})
	}
	return out, nil
}

// rangeQuery runs a server-authored range query and decodes it into Series.
//
// It is the shared half of QueryMetrics: same envelope, same tenant path, same
// label stripping. QueryMetrics keeps the surrounding retention/rollup logic
// because that logic is about the caller's query, and none of it applies to a
// query this package wrote itself.
func (c *Client) rangeQuery(ctx context.Context, account uint32, promQL string, rng TimeRange, step time.Duration) ([]Series, error) {
	params := url.Values{}
	params.Set("query", promQL)
	params.Set("start", strconv.FormatInt(rng.From.Unix(), 10))
	params.Set("end", strconv.FormatInt(rng.To.Unix(), 10))
	params.Set("step", durationLiteral(step))

	endpoint := join(c.cfg.VMSelectURL,
		"/select/"+tenants.VMTenantPath(account)+"/prometheus/api/v1/query_range", params)

	var raw vmRangeResponse
	if err := c.getJSON(ctx, endpoint, nil, &raw); err != nil {
		return nil, err
	}
	if raw.Status != "" && raw.Status != "success" {
		return nil, fmt.Errorf("%w: range query returned status %q", ErrBackend, raw.Status)
	}
	out := make([]Series, 0, len(raw.Data.Result))
	for _, r := range raw.Data.Result {
		s := Series{Labels: stripInternalLabels(r.Metric), Points: make([]Sample, 0, len(r.Values))}
		for _, v := range r.Values {
			sample, ok := decodeSample(v)
			if !ok {
				continue
			}
			s.Points = append(s.Points, sample)
		}
		out = append(out, s)
	}
	return out, nil
}

// durationLiteral renders a duration as the whole-second form MetricsQL takes.
func durationLiteral(d time.Duration) string {
	sec := int64(d.Seconds())
	if sec < 1 {
		sec = 1
	}
	return strconv.FormatInt(sec, 10) + "s"
}
