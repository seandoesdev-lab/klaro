// Package api wires the obsplane HTTP surface.
//
// Only the foundation slice exists here: liveness, readiness, and the
// org-scoped group that every future handler (ingestkey, explorer, alerting,
// dashboards, live) hangs off. Mounting that group now fixes the middleware
// order - authenticate, match :orgId, then run inside a tenant transaction -
// so no later handler can accidentally sit outside it.
package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/platform/redisx"
	"github.com/klaro/observability/internal/tenancy"
	"github.com/klaro/observability/internal/tenants"
)

// Deps are the collaborators the router hands to its handlers.
type Deps struct {
	DB      *db.DB
	Signal  redisx.Signaler
	Tenants tenants.Mapper
	Auth    tenancy.Authenticator
}

// NewRouter builds the public engine.
func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// Liveness: process is up. No dependencies, so a database blip does not
	// get the container killed.
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Readiness: can actually serve. Postgres is the hard dependency.
	r.GET("/readyz", func(c *gin.Context) {
		if d.DB == nil {
			httpx.Internal(c, "database not configured")
			return
		}
		if err := d.DB.Ping(c.Request.Context()); err != nil {
			httpx.Write(c, http.StatusServiceUnavailable, httpx.CodeInternal, "database unavailable", nil)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	// Every org-scoped route lives under this group so the tenancy guard cannot
	// be skipped by forgetting a middleware on an individual route.
	org := r.Group("/orgs/:orgId", tenancy.Middleware(d.Auth))
	{
		org.GET("/obs/tenant", d.getTenant)
	}
	return r
}

// getTenant reports how the caller org maps onto the telemetry backends. It is
// the first consumer of the tenancy + tenants pair and gives the foundation an
// end-to-end path to test: credential -> org -> RLS transaction -> backend
// tenant identity.
func (d Deps) getTenant(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}

	account, err := d.Tenants.VMAccountID(c.Request.Context(), orgID)
	if err != nil {
		httpx.Internal(c, "resolve vm tenant")
		return
	}
	scope, err := d.Tenants.ScopeOrgID(c.Request.Context(), orgID)
	if err != nil {
		httpx.Internal(c, "resolve scope tenant")
		return
	}

	// Round-trip the RLS session so readiness of the tenant path is real rather
	// than assumed.
	var current string
	if err := tenancy.InTx(c, d.DB, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		current, err = db.CurrentOrg(ctx, tx)
		return err
	}); err != nil {
		httpx.Internal(c, "open tenant transaction")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"org_id":         orgID,
		"db_current_org": current,
		"vm_account_id":  account,
		"vm_tenant_path": tenants.VMTenantPath(account),
		"x_scope_org_id": scope,
	})
}
