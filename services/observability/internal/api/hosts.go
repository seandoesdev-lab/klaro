package api

// Infrastructure surface (P1b): the host inventory, one host's detail charts,
// and the uptime SLO.
//
// All three hang off the same org group as everything else, so the tenancy
// middleware has already matched :orgId against the credential before any
// handler here runs. Nothing below reads an org from a parameter.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/tenancy"
)

// listHosts returns the org's registered hosts joined with their latest
// readings [OBS-01].
//
// The registry half is authoritative for which hosts exist, so a metrics
// backend that is down degrades the response rather than failing it - see
// inventory.Result.MetricsAvailable.
func (d Deps) listHosts(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	if d.Inventory == nil {
		httpx.Internal(c, "host inventory not configured")
		return
	}
	result, err := d.Inventory.List(c.Request.Context(), orgID)
	if err != nil {
		httpx.Internal(c, "host inventory query failed")
		return
	}
	c.JSON(http.StatusOK, result)
}

// getHostSeries returns the cpu/memory/disk/load/network charts for one host.
//
// The host identity travels as a path parameter and is validated against the
// label-value rules before it reaches a query, because it ends up inside a
// quoted matcher the server wrote.
func (d Deps) getHostSeries(c *gin.Context) {
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
	stepSec, ok := intQuery(c, "step")
	if !ok {
		httpx.Validation(c, "step must be a non-negative number of seconds", nil)
		return
	}

	result, err := d.Explorer.HostSeries(c.Request.Context(), orgID, c.Param("hostIdent"),
		rng, time.Duration(stepSec)*time.Second)
	if err != nil {
		writeExplorerError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// getUptime returns the availability achieved over a window [OBS-01].
//
// Availability here is reporting coverage, not reachability (explorer.Uptime
// says why). An org with no host metrics gets a well-formed result with an
// empty host list, which is what lets the widget render an empty state rather
// than an error.
func (d Deps) getUptime(c *gin.Context) {
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
	stepSec, ok := intQuery(c, "step")
	if !ok {
		httpx.Validation(c, "step must be a non-negative number of seconds", nil)
		return
	}
	target, ok := ratioQuery(c, "target")
	if !ok {
		httpx.Validation(c, "target must be a ratio greater than 0 and at most 1", nil)
		return
	}

	result, err := d.Explorer.Uptime(c.Request.Context(), orgID, rng,
		time.Duration(stepSec)*time.Second, target, c.Query("host"))
	if err != nil {
		writeExplorerError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// ratioQuery reads an optional 0..1 objective. Absent means "use the default",
// which explorer.Uptime supplies; out of range is a caller error rather than a
// silent clamp, because an SLO written as 99 (meaning percent) must not quietly
// become an SLO of 1.
func ratioQuery(c *gin.Context, key string) (float64, bool) {
	raw := c.Query(key)
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || v > 1 {
		return 0, false
	}
	return v, true
}
