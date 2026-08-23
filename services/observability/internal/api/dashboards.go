package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/dashboards"
	"github.com/klaro/observability/internal/platform/audit"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/tenancy"
)

// writeDashboardError maps the dashboards sentinels onto the shared envelope.
func writeDashboardError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, dashboards.ErrInvalidSpec), errors.Is(err, dashboards.ErrInvalidName):
		httpx.Validation(c, err.Error(), nil)
	case errors.Is(err, dashboards.ErrDuplicateName):
		httpx.Conflict(c, err.Error(), nil)
	case errors.Is(err, dashboards.ErrNotFound):
		httpx.NotFound(c, "dashboard not found")
	default:
		httpx.Internal(c, "dashboard operation failed")
	}
}

// postDashboard creates a dashboard [OBS-09].
func (d Deps) postDashboard(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	var in dashboards.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Validation(c, "request body must be a JSON object", gin.H{"parse": err.Error()})
		return
	}
	out, err := d.Dashboards.Create(c.Request.Context(), orgID, in,
		dashboards.AuditHook(d.Audit, audit.ActionDashboardCreate, nil))
	if err != nil {
		writeDashboardError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// listDashboards returns the org's dashboards [OBS-09].
func (d Deps) listDashboards(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	out, err := d.Dashboards.List(c.Request.Context(), orgID)
	if err != nil {
		writeDashboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// getDashboard reads one dashboard [OBS-09].
func (d Deps) getDashboard(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	out, err := d.Dashboards.Get(c.Request.Context(), orgID, c.Param("dashId"))
	if err != nil {
		writeDashboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// patchDashboard applies a partial update [OBS-09].
func (d Deps) patchDashboard(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	var in dashboards.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Validation(c, "request body must be a JSON object", gin.H{"parse": err.Error()})
		return
	}
	out, err := d.Dashboards.Update(c.Request.Context(), orgID, c.Param("dashId"), in,
		dashboards.AuditHook(d.Audit, audit.ActionDashboardUpdate, nil))
	if err != nil {
		writeDashboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// deleteDashboard removes a dashboard [OBS-09].
func (d Deps) deleteDashboard(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	if err := d.Dashboards.Remove(c.Request.Context(), orgID, c.Param("dashId"),
		dashboards.AuditHook(d.Audit, audit.ActionDashboardDelete, nil)); err != nil {
		writeDashboardError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
