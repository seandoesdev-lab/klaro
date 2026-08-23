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

// MemoryMapper is the build-order step 2 stub: it hands out VM AccountIDs
// sequentially on first sight and remembers them for the process lifetime.
//
// Deliberately NOT a hash of the org uuid. A 32-bit hash collides, and two orgs
// sharing an AccountID would merge their metrics inside VictoriaMetrics - a
// silent cross-tenant leak that RLS cannot catch because it happens outside
// Postgres. Sequential assignment cannot collide.
//
// The cost is that assignments are not durable: a restart renumbers, which
// would orphan already-written series. That is acceptable while nothing writes
// to VM yet (Collector routing is build-order step 4), and it MUST be replaced
// by a persisted assignment table before step 4 ships. StaticMapper shows the
// read-only shape a durable implementation will take.
type MemoryMapper struct {
	mu     sync.Mutex
	byOrg  map[string]uint32
	nextID uint32
}

// NewMemoryMapper starts assignment at 1. AccountID 0 is left unused because
// VictoriaMetrics treats it as the default account, and a bug that produced 0
// would dump one org into the shared bucket.
func NewMemoryMapper() *MemoryMapper {
	return &MemoryMapper{byOrg: map[string]uint32{}, nextID: 1}
}

// VMAccountID implements Mapper, assigning on first use.
func (m *MemoryMapper) VMAccountID(_ context.Context, orgID string) (uint32, error) {
	if !db.ValidOrgID(orgID) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidOrg, orgID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.byOrg[orgID]; ok {
		return id, nil
	}
	id := m.nextID
	m.nextID++
	m.byOrg[orgID] = id
	return id, nil
}

// ScopeOrgID implements Mapper.
func (m *MemoryMapper) ScopeOrgID(_ context.Context, orgID string) (string, error) {
	return scopeOrgID(orgID)
}

// StaticMapper resolves from a fixed table and refuses unknown orgs. This is
// the shape a durable, table-backed mapper will expose.
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
	_ Mapper = (*MemoryMapper)(nil)
	_ Mapper = (*StaticMapper)(nil)
)
