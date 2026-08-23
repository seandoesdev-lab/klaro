// Package tenants maps a klaro org onto the native tenant identity of each
// telemetry backend (design HOW-8).
//
// Isolation is enforced by the stores themselves, not by label matchers:
// VictoriaMetrics keys tenants by a numeric AccountID, Tempo and Loki by the
// X-Scope-OrgID header. Only the control plane sets these, derived from the org
// that RLS already resolved, so a caller cannot widen its own scope.
package tenants

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/platform/db"
)

// ErrNotMapped is returned when an org has no backend tenant assigned.
var ErrNotMapped = errors.New("org has no backend tenant mapping")

// ErrInvalidOrg is returned for a malformed org id.
var ErrInvalidOrg = errors.New("invalid org id")

// Mapper resolves the per-backend tenant identity for an org.
type Mapper interface {
	// VMAccountID is the VictoriaMetrics tenant (vminsert/vmselect path segment).
	VMAccountID(ctx context.Context, orgID string) (uint32, error)
	// ScopeOrgID is the X-Scope-OrgID value for Tempo and Loki.
	ScopeOrgID(ctx context.Context, orgID string) (string, error)
}

// scopeOrgID uses the org uuid verbatim. Tempo and Loki accept arbitrary
// strings, so there is nothing to invent and nothing that can collide.
func scopeOrgID(orgID string) (string, error) {
	if !db.ValidOrgID(orgID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidOrg, orgID)
	}
	return orgID, nil
}

// PGMapper is the durable mapper backed by observability_tenants (migration
// 0008). Assignment happens on first sight and is then immutable: the table
// grants the app role SELECT and INSERT but not UPDATE, because changing an
// AccountID would hand one org's already-written VictoriaMetrics series to
// another org - a leak that happens outside Postgres, where RLS cannot see it.
//
// Because the mapping is immutable, it is safe to memoise for the process
// lifetime. That matters: the Collector authz path resolves a tenant on every
// cache miss, and this keeps it off the database.
type PGMapper struct {
	db *db.DB

	mu    sync.RWMutex
	byOrg map[string]uint32
}

// NewPGMapper builds a durable mapper over d.
func NewPGMapper(d *db.DB) *PGMapper {
	return &PGMapper{db: d, byOrg: map[string]uint32{}}
}

// assignSQL takes the AccountID from a sequence rather than from
// max(vm_account_id)+1: under RLS this transaction can only see its own org's
// row, so it has no way to look at the others. A sequence needs no such read
// and cannot hand out a duplicate.
//
// ON CONFLICT DO NOTHING makes a concurrent first-use race harmless - the loser
// simply reads the winner's row back.
const assignSQL = `
INSERT INTO observability_tenants (org_id, scope_org_id)
VALUES (current_setting('app.current_org')::uuid, current_setting('app.current_org'))
ON CONFLICT (org_id) DO NOTHING
RETURNING vm_account_id`

const selectSQL = `
SELECT vm_account_id FROM observability_tenants
WHERE org_id = current_setting('app.current_org')::uuid`

// VMAccountID implements Mapper, assigning durably on first use.
func (m *PGMapper) VMAccountID(ctx context.Context, orgID string) (uint32, error) {
	if !db.ValidOrgID(orgID) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidOrg, orgID)
	}
	if id, ok := m.cached(orgID); ok {
		return id, nil
	}
	var id uint32
	err := m.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = AssignTx(ctx, tx)
		return err
	})
	if err != nil {
		return 0, err
	}
	m.remember(orgID, id)
	return id, nil
}

// ScopeOrgID implements Mapper. An org with no VM assignment is not routable to
// Tempo or Loki either, so resolving the header also materialises the row.
func (m *PGMapper) ScopeOrgID(ctx context.Context, orgID string) (string, error) {
	if _, err := m.VMAccountID(ctx, orgID); err != nil {
		return "", err
	}
	return scopeOrgID(orgID)
}

// Remember seeds the cache from a lookup a caller already performed inside its
// own transaction (the Collector authz path resolves key, tenant and quota in
// one round trip).
func (m *PGMapper) Remember(orgID string, accountID uint32) { m.remember(orgID, accountID) }

func (m *PGMapper) cached(orgID string) (uint32, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.byOrg[orgID]
	return id, ok
}

func (m *PGMapper) remember(orgID string, id uint32) {
	if id == 0 {
		return // never cache the VictoriaMetrics default account
	}
	m.mu.Lock()
	m.byOrg[orgID] = id
	m.mu.Unlock()
}

// AssignTx resolves - creating if absent - the VM AccountID for the org of an
// already tenant-scoped transaction. Callers that are mid-transaction (authz,
// quota) use this so the assignment commits with the rest of their work.
func AssignTx(ctx context.Context, tx pgx.Tx) (uint32, error) {
	var id uint32
	err := tx.QueryRow(ctx, assignSQL).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case errors.Is(err, pgx.ErrNoRows):
		// DO NOTHING returns no row when the org was already assigned.
	default:
		return 0, fmt.Errorf("assign vm account id: %w", err)
	}
	if err := tx.QueryRow(ctx, selectSQL).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotMapped
		}
		return 0, fmt.Errorf("read vm account id: %w", err)
	}
	return id, nil
}

// StaticMapper resolves from a fixed table and refuses unknown orgs. It exists
// for tests and for the read-only fixtures the router tests use; production
// wiring uses PGMapper.
type StaticMapper struct{ ByOrg map[string]uint32 }

// NewStaticMapper copies the supplied assignments.
func NewStaticMapper(byOrg map[string]uint32) *StaticMapper {
	m := &StaticMapper{ByOrg: make(map[string]uint32, len(byOrg))}
	for k, v := range byOrg {
		m.ByOrg[k] = v
	}
	return m
}

// VMAccountID implements Mapper.
func (m *StaticMapper) VMAccountID(_ context.Context, orgID string) (uint32, error) {
	if !db.ValidOrgID(orgID) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidOrg, orgID)
	}
	id, ok := m.ByOrg[orgID]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrNotMapped, orgID)
	}
	return id, nil
}

// ScopeOrgID implements Mapper. An org with no VM assignment is not routable to
// Tempo or Loki either, so both resolvers fail together.
func (m *StaticMapper) ScopeOrgID(ctx context.Context, orgID string) (string, error) {
	if _, err := m.VMAccountID(ctx, orgID); err != nil {
		return "", err
	}
	return scopeOrgID(orgID)
}

// VMTenantPath renders the AccountID as the tenant path segment used by
// vminsert and vmselect URLs (for example /insert/7/prometheus).
func VMTenantPath(accountID uint32) string { return strconv.FormatUint(uint64(accountID), 10) }

var (
	_ Mapper = (*PGMapper)(nil)
	_ Mapper = (*StaticMapper)(nil)
)
