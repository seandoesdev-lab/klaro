package ingestkey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/tenants"
)

// ErrUnauthorized is the single answer the ingest path gives for every way a
// key can fail: unknown, revoked, or past its rotation grace.
//
// One error on purpose. Telling a caller "that key existed but was revoked"
// confirms a guess about our key space; telling it nothing costs the legitimate
// operator nothing, because they read the reason from the keys endpoint.
var ErrUnauthorized = errors.New("ingest key is not authorized")

// TenantCache is the part of tenants.PGMapper this package needs: a place to
// leave a tenant assignment it already looked up, so the mapper does not repeat
// the query on the next request for the same org.
type TenantCache interface {
	Remember(orgID string, accountID uint32)
}

// Grant is what the Collector learns from one successful authorization: who the
// telemetry belongs to, where each signal must be routed, and how the org is
// tracking against its plan.
type Grant struct {
	OrgID string `json:"org_id"`
	KeyID string `json:"key_id"`

	// VMAccountID / VMTenantPath address the VictoriaMetrics tenant, ScopeOrgID
	// is the X-Scope-OrgID for Tempo and Loki (design HOW-8). The Collector must
	// take these from here and never from anything the SDK sent, which is the
	// whole reason this round trip exists.
	VMAccountID  uint32 `json:"vm_account_id"`
	VMTenantPath string `json:"vm_tenant_path"`
	ScopeOrgID   string `json:"x_scope_org_id"`

	Quota Quota `json:"quota"`

	// CacheTTLSec is how long the Collector may reuse this grant. It bounds how
	// stale a revocation can be: a revoked key keeps working for at most this
	// long, so it trades ingest latency against revocation latency.
	CacheTTLSec int `json:"cache_ttl_sec"`
}

// AuthorizerOptions configures Authorizer.
type AuthorizerOptions struct {
	// ActiveHostWindow is how recently a host must have reported to count
	// against the host meter.
	ActiveHostWindow time.Duration
	// TouchWindow rate-limits last_used_at writes.
	TouchWindow time.Duration
	// CacheTTL is advertised to the Collector as CacheTTLSec.
	CacheTTL time.Duration
}

// Authorizer implements the Collector authz fast path (design section 4.1/4.4).
type Authorizer struct {
	db    *db.DB
	store *Store
	cache TenantCache
	opts  AuthorizerOptions
	now   func() time.Time
}

// Defaults for anything the caller left at zero.
const (
	defaultActiveHostWindow = 15 * time.Minute
	defaultTouchWindow      = time.Minute
	defaultCacheTTL         = 30 * time.Second
)

// NewAuthorizer wires the fast path. cache may be nil.
func NewAuthorizer(d *db.DB, store *Store, cache TenantCache, opts AuthorizerOptions) *Authorizer {
	if opts.ActiveHostWindow <= 0 {
		opts.ActiveHostWindow = defaultActiveHostWindow
	}
	if opts.TouchWindow <= 0 {
		opts.TouchWindow = defaultTouchWindow
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = defaultCacheTTL
	}
	return &Authorizer{db: d, store: store, cache: cache, opts: opts, now: time.Now}
}

// Authorize verifies a presented secret and returns the routing identity for
// its org.
//
// The quota snapshot rides along even when the org is over its limits: per the
// confirmed policy (design section 7-1) an over-quota org keeps sending and is
// billed for the overage, so the Collector needs the numbers for metering and
// headers, not for a decision to drop data.
func (a *Authorizer) Authorize(ctx context.Context, secret string) (Grant, error) {
	res, err := a.store.Resolve(ctx, secret)
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrMalformedSecret):
		return Grant{}, ErrUnauthorized
	case err != nil:
		return Grant{}, err
	}

	now := a.now()
	if res.GraceElapsed(now) {
		// The rotation window closed. Retire the row now so the keys endpoint
		// agrees with what ingest just decided.
		if err := a.db.WithOrg(ctx, res.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			return ExpireGracedTx(ctx, tx, res.KeyID)
		}); err != nil {
			return Grant{}, err
		}
		return Grant{}, ErrUnauthorized
	}
	if !res.Live(now) {
		return Grant{}, ErrUnauthorized
	}

	grant := Grant{
		OrgID:       res.OrgID,
		KeyID:       res.KeyID,
		ScopeOrgID:  res.OrgID,
		CacheTTLSec: int(a.opts.CacheTTL / time.Second),
	}
	err = a.db.WithOrg(ctx, res.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := TouchLastUsedTx(ctx, tx, res.KeyID, a.opts.TouchWindow); err != nil {
			return err
		}
		account, err := tenants.AssignTx(ctx, tx)
		if err != nil {
			return fmt.Errorf("resolve backend tenant: %w", err)
		}
		grant.VMAccountID = account
		grant.VMTenantPath = tenants.VMTenantPath(account)

		grant.Quota, err = SnapshotTx(ctx, tx, a.opts.ActiveHostWindow)
		return err
	})
	if err != nil {
		return Grant{}, err
	}
	if a.cache != nil {
		a.cache.Remember(grant.OrgID, grant.VMAccountID)
	}
	return grant, nil
}
