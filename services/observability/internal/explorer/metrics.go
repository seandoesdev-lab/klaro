package explorer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/klaro/observability/internal/tenants"
)

// MetricsQuery is the structured form of GET /obs/metrics/query [OBS-03].
type MetricsQuery struct {
	Range   TimeRange
	Metric  string
	Filters []Matcher
	Agg     string
	Step    time.Duration
}

// Sample is one point, serialised as the [ts, value] pair the design specifies.
// ts is unix milliseconds so a browser can use it directly.
type Sample struct {
	TS    int64
	Value float64
}

// MarshalJSON renders the pair form.
func (s Sample) MarshalJSON() ([]byte, error) {
	return []byte("[" + strconv.FormatInt(s.TS, 10) + "," +
		strconv.FormatFloat(s.Value, 'f', -1, 64) + "]"), nil
}

// Series is one labelled time series.
type Series struct {
	Labels map[string]string `json:"labels"`
	Points []Sample          `json:"points"`
}

// MetricsResult is the response body.
type MetricsResult struct {
	Series []Series `json:"series"`
	// Query is the MetricsQL this package generated. Returning it makes the
	// server-side enforcement inspectable - you can see the org matcher is
	// there - and it is safe because the caller supplied every other part.
	Query string `json:"query"`
}

// defaultStep keeps a range query from asking for more points than a chart can
// draw when the caller does not choose a resolution.
const defaultStep = 30 * time.Second

// maxPoints bounds points-per-series. A 90 day window at a 1 second step is
// nearly 8 million points per series; refusing is kinder than timing out.
const maxPoints = 11000

// Validate checks the query and fills in the step default.
func (q *MetricsQuery) Validate() error {
	if err := q.Range.Validate(); err != nil {
		return err
	}
	if q.Metric != "" && !ValidMetricName(q.Metric) {
		return fmt.Errorf("%w: %q is not a valid metric name", ErrInvalidQuery, q.Metric)
	}
	if q.Metric == "" && len(q.Filters) == 0 {
		// Without either, the only matcher left is the injected org one, and the
		// query becomes "every series this tenant has ever written".
		return fmt.Errorf("%w: metric or at least one filter is required", ErrInvalidQuery)
	}
	if q.Step <= 0 {
		q.Step = defaultStep
	}
	if n := q.Range.To.Sub(q.Range.From) / q.Step; n > maxPoints {
		return fmt.Errorf("%w: %d points per series exceeds the %d limit; widen step or narrow the range",
			ErrInvalidQuery, n, maxPoints)
	}
	return nil
}

// vmRangeResponse is the Prometheus-compatible query_range envelope.
type vmRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string    `json:"metric"`
			Values [][2]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// QueryMetrics runs a range query against the org's VictoriaMetrics tenant.
//
// Two independent things scope it to the org: the AccountID path segment, which
// vmselect resolves to a physically separate tenant, and the injected
// klaro_org_id matcher. Neither is reachable from a caller parameter.
func (c *Client) QueryMetrics(ctx context.Context, orgID string, q MetricsQuery) (MetricsResult, error) {
	if !c.cfg.Configured(c.cfg.VMSelectURL) {
		return MetricsResult{}, fmt.Errorf("%w: no metrics backend configured", ErrBackend)
	}
	if err := q.Validate(); err != nil {
		return MetricsResult{}, err
	}

	account, err := c.tenants.VMAccountID(ctx, orgID)
	if err != nil {
		return MetricsResult{}, fmt.Errorf("%w: resolve tenant: %v", ErrBackend, err)
	}

	promQL, err := Aggregate(q.Agg, Selector(q.Metric, orgID, q.Filters), q.Step)
	if err != nil {
		return MetricsResult{}, err
	}

	params := url.Values{}
	params.Set("query", promQL)
	params.Set("start", strconv.FormatInt(q.Range.From.Unix(), 10))
	params.Set("end", strconv.FormatInt(q.Range.To.Unix(), 10))
	params.Set("step", strconv.FormatInt(int64(q.Step.Seconds()), 10)+"s")

	endpoint := join(c.cfg.VMSelectURL,
		"/select/"+tenants.VMTenantPath(account)+"/prometheus/api/v1/query_range", params)

	var raw vmRangeResponse
	if err := c.getJSON(ctx, endpoint, nil, &raw); err != nil {
		return MetricsResult{}, err
	}
	if raw.Status != "" && raw.Status != "success" {
		return MetricsResult{}, fmt.Errorf("%w: metrics query returned status %q", ErrBackend, raw.Status)
	}

	out := MetricsResult{Series: make([]Series, 0, len(raw.Data.Result)), Query: promQL}
	for _, r := range raw.Data.Result {
		s := Series{Labels: stripInternalLabels(r.Metric), Points: make([]Sample, 0, len(r.Values))}
		for _, v := range r.Values {
			sample, ok := decodeSample(v)
			if !ok {
				continue // a point we cannot read is dropped, not fatal
			}
			s.Points = append(s.Points, sample)
		}
		out.Series = append(out.Series, s)
	}
	return out, nil
}

// decodeSample reads Prometheus' [<unix seconds float>, "<value>"] pair.
func decodeSample(v [2]json.RawMessage) (Sample, bool) {
	var secs float64
	if err := json.Unmarshal(v[0], &secs); err != nil {
		return Sample{}, false
	}
	var text string
	if err := json.Unmarshal(v[1], &text); err != nil {
		return Sample{}, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		// NaN and +Inf arrive as text and are real answers, but there is
		// nothing a chart can do with them.
		return Sample{}, false
	}
	return Sample{TS: int64(secs * 1000), Value: value}, true
}
