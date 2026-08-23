package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/tenancy"
)

// writeExplorerError maps the explorer sentinels onto the shared envelope.
//
// A backend failure is 502, not 500: the control plane is fine, the storage it
// proxies to is not, and a caller retrying against a different replica of us
// would not help. The upstream message never travels - it can name other
// tenants' labels or internal hostnames.
func writeExplorerError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, explorer.ErrInvalidQuery):
		httpx.Validation(c, err.Error(), nil)
	case errors.Is(err, explorer.ErrNotFound):
		httpx.NotFound(c, "not found in this org")
	case errors.Is(err, explorer.ErrBackend):
		httpx.Write(c, http.StatusBadGateway, httpx.CodeInternal, "telemetry backend unavailable", nil)
	default:
		httpx.Internal(c, "explorer query failed")
	}
}

// parseTime accepts RFC3339 or a unix timestamp in seconds or milliseconds.
//
// Three forms because three kinds of caller exist: a dashboard sends what
// Date.toISOString() produced, a script sends `date +%s`, and JavaScript's
// Date.now() is milliseconds. Guessing between seconds and milliseconds by
// magnitude is safe for any time this product will ever be asked about - the
// boundary is the year 33658 in seconds and 2001 in milliseconds.
func parseTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	if n > 1e12 {
		return time.UnixMilli(n), true
	}
	return time.Unix(n, 0), true
}

// timeRange reads from/to, defaulting to the last hour when both are absent.
func timeRange(c *gin.Context) (explorer.TimeRange, bool) {
	to, okTo := parseTime(c.Query("to"))
	if !okTo {
		if c.Query("to") != "" {
			return explorer.TimeRange{}, false
		}
		to = time.Now()
	}
	from, okFrom := parseTime(c.Query("from"))
	if !okFrom {
		if c.Query("from") != "" {
			return explorer.TimeRange{}, false
		}
		from = to.Add(-time.Hour)
	}
	return explorer.TimeRange{From: from, To: to}, true
}

// filters reads the repeated (or comma separated) filter parameter.
func filters(c *gin.Context) ([]explorer.Matcher, error) {
	return explorer.ParseFilters(c.QueryArray("filter"))
}

func intQuery(c *gin.Context, key string) (int, bool) {
	raw := c.Query(key)
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// getMetrics proxies a range query to the org's VictoriaMetrics tenant [OBS-03].
func (d Deps) getMetrics(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	rng, ok := timeRange(c)
	if !ok {
		httpx.Validation(c, "from and to must be RFC3339 or a unix timestamp", nil)
		return
	}
	match, err := filters(c)
	if err != nil {
		writeExplorerError(c, err)
		return
	}
	stepSec, ok := intQuery(c, "step")
	if !ok {
		httpx.Validation(c, "step must be a non-negative number of seconds", nil)
		return
	}

	result, err := d.Explorer.QueryMetrics(c.Request.Context(), orgID, explorer.MetricsQuery{
		Range:   rng,
		Metric:  c.Query("metric"),
		Filters: match,
		Agg:     c.Query("agg"),
		Step:    time.Duration(stepSec) * time.Second,
	})
	if err != nil {
		writeExplorerError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// getTraces searches the org's Tempo tenant [OBS-04].
func (d Deps) getTraces(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	rng, ok := timeRange(c)
	if !ok {
		httpx.Validation(c, "from and to must be RFC3339 or a unix timestamp", nil)
		return
	}
	minMS, ok := intQuery(c, "min_duration_ms")
	if !ok {
		httpx.Validation(c, "min_duration_ms must be a non-negative integer", nil)
		return
	}
	limit, ok := intQuery(c, "limit")
	if !ok {
		httpx.Validation(c, "limit must be a non-negative integer", nil)
		return
	}

	result, err := d.Explorer.SearchTraces(c.Request.Context(), orgID, explorer.TracesQuery{
		Range:       rng,
		Service:     c.Query("service"),
		MinDuration: time.Duration(minMS) * time.Millisecond,
		Limit:       limit,
	})
	if err != nil {
		writeExplorerError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// getTrace returns one trace as a waterfall [OBS-04].
func (d Deps) getTrace(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	trace, err := d.Explorer.GetTrace(c.Request.Context(), orgID, c.Param("traceId"))
	if err != nil {
		writeExplorerError(c, err)
		return
	}
	c.JSON(http.StatusOK, trace)
}

// getLogs queries the org's Loki tenant [OBS-05].
func (d Deps) getLogs(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	rng, ok := timeRange(c)
	if !ok {
		httpx.Validation(c, "from and to must be RFC3339 or a unix timestamp", nil)
		return
	}
	match, err := filters(c)
	if err != nil {
		writeExplorerError(c, err)
		return
	}
	limit, ok := intQuery(c, "limit")
	if !ok {
		httpx.Validation(c, "limit must be a non-negative integer", nil)
		return
	}

	page, err := d.Explorer.QueryLogs(c.Request.Context(), orgID, explorer.LogsQuery{
		Range:    rng,
		Filters:  match,
		Contains: c.Query("query"),
		Limit:    limit,
	})
	if err != nil {
		writeExplorerError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}
