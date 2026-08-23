// Package api wires the obsplane HTTP surface.
//
// It carries the org-scoped group that every handler hangs off, which fixes the
// middleware order once - authenticate, match :orgId, then run inside a tenant
// transaction - so no later handler can accidentally sit outside it. Mounted so
// far: health, the tenant probe and observability keys/quota [OBS-02]. Explorer,
// alerting, dashboards and the live WebSocket are later build-order steps.
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/alerting"
	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/ingestkey"
	"github.com/klaro/observability/internal/live"
	"github.com/klaro/observability/internal/platform/audit"
	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/platform/redisx"
	"github.com/klaro/observability/internal/tenancy"
	"github.com/klaro/observability/internal/tenants"
)

// Deps are the collaborators the router hands to its handlers.
type Deps struct {
	DB       *db.DB
	Signal   redisx.Signaler
	Tenants  tenants.Mapper
	Auth     tenancy.Authenticator
	Keys     *ingestkey.Store
	Authz    *ingestkey.Authorizer
	Audit    audit.Recorder
	Live     *live.Hub
	Explorer *explorer.Client
	Rules    *alerting.Store
	RuleSync *alerting.Syncer

	// RotationGrace is how long a rotated key keeps working alongside its
	// replacement (design HOW-4).
	RotationGrace time.Duration
	// ActiveHostWindow is how recently a host must have reported to count
	// against the host quota.
	ActiveHostWindow time.Duration
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
	// Live streaming sits outside the org group on purpose: it authorises
	// with the same tenancy.Resolve the middleware uses, but reports a
	// scope mismatch as a WebSocket close code rather than an HTTP status,
	// which is the only form a browser client can actually read
	// (design section 4.3) [OBS-01/APM-02].
	r.GET("/orgs/:orgId/obs/live", d.getLive)

	org := r.Group("/orgs/:orgId", tenancy.Middleware(d.Auth))
	{
		org.GET("/obs/tenant", d.getTenant)

		// Observability keys and the quota they are metered against [OBS-02].
		org.POST("/obs/keys", d.postKey)
		org.GET("/obs/keys", d.listKeys)
		org.POST("/obs/keys/:keyId/rotate", d.rotateKey)
		org.DELETE("/obs/keys/:keyId", d.deleteKey)
		org.GET("/obs/quota", d.getQuota)

		// Explorer: structured reads proxied to the storage backends with the
		// org forced in server side [OBS-03/04/05].
		org.GET("/obs/metrics/query", d.getMetrics)
		org.GET("/obs/traces", d.getTraces)
		org.GET("/obs/traces/:traceId", d.getTrace)
		org.GET("/obs/logs", d.getLogs)

		// Alert rules and the history of what they fired [OBS-06/07].
		org.POST("/obs/alert-rules", d.postAlertRule)
		org.GET("/obs/alert-rules", d.listAlertRules)
		org.GET("/obs/alert-rules/:ruleId", d.getAlertRule)
		org.PATCH("/obs/alert-rules/:ruleId", d.patchAlertRule)
		org.DELETE("/obs/alert-rules/:ruleId", d.deleteAlertRule)
		org.GET("/obs/alert-events", d.listAlertEvents)
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
