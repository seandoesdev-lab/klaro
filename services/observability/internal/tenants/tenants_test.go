package tenants

import (
	"context"
	"errors"
	"sync"
	"testing"
)

const (
	orgA = "00000000-0000-0000-0000-00000000000a"
	orgB = "00000000-0000-0000-0000-00000000000b"
	orgC = "00000000-0000-0000-0000-00000000000c"
)

// A malformed org must be rejected before any SQL is attempted, which is why a
// nil *db.DB is enough to prove it: reaching the database would panic.
func TestMappersRejectMalformedOrg(t *testing.T) {
	ctx := context.Background()
	bad := []string{"", "org-a", "x' OR 1=1--"}
	mappers := []Mapper{NewPGMapper(nil), NewStaticMapper(map[string]uint32{orgA: 1})}
	for _, m := range mappers {
		for _, s := range bad {
			if _, err := m.VMAccountID(ctx, s); !errors.Is(err, ErrInvalidOrg) {
				t.Errorf("%T VMAccountID(%q) = %v, want ErrInvalidOrg", m, s, err)
			}
			if _, err := m.ScopeOrgID(ctx, s); err == nil {
				t.Errorf("%T ScopeOrgID(%q) should fail", m, s)
			}
		}
	}
}

// The mapping is immutable once assigned, so a cached hit must never reach the
// database again. A nil pool makes that observable: a cache miss would panic.
func TestPGMapperServesRememberedAssignmentsWithoutTheDatabase(t *testing.T) {
	ctx := context.Background()
	m := NewPGMapper(nil)
	m.Remember(orgA, 7)

	id, err := m.VMAccountID(ctx, orgA)
	if err != nil {
		t.Fatal(err)
	}
	if id != 7 {
		t.Fatalf("VMAccountID = %d, want 7", id)
	}
	scope, err := m.ScopeOrgID(ctx, orgA)
	if err != nil {
		t.Fatal(err)
	}
	if scope != orgA {
		t.Errorf("ScopeOrgID = %q, want the org uuid", scope)
	}
}

// AccountID 0 is the VictoriaMetrics default account. Caching it would pin an
// org into the shared bucket, so a zero is refused rather than remembered.
func TestPGMapperNeverCachesAccountZero(t *testing.T) {
	m := NewPGMapper(nil)
	m.Remember(orgA, 0)
	if _, ok := m.cached(orgA); ok {
		t.Error("AccountID 0 was cached")
	}
}

func TestPGMapperCacheIsRaceFree(t *testing.T) {
	ctx := context.Background()
	m := NewPGMapper(nil)
	orgs := map[string]uint32{orgA: 1, orgB: 2, orgC: 3}
	for org, id := range orgs {
		m.Remember(org, id)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		for org, want := range orgs {
			wg.Add(1)
			go func(org string, want uint32) {
				defer wg.Done()
				got, err := m.VMAccountID(ctx, org)
				if err != nil {
					t.Errorf("VMAccountID(%s): %v", org, err)
					return
				}
				if got != want {
					t.Errorf("org %s = %d, want %d", org, got, want)
				}
			}(org, want)
		}
	}
	wg.Wait()
}

func TestStaticMapperRefusesUnknownOrg(t *testing.T) {
	ctx := context.Background()
	m := NewStaticMapper(map[string]uint32{orgA: 7})

	id, err := m.VMAccountID(ctx, orgA)
	if err != nil || id != 7 {
		t.Fatalf("VMAccountID = %d, %v", id, err)
	}
	if _, err := m.VMAccountID(ctx, orgB); !errors.Is(err, ErrNotMapped) {
		t.Errorf("unknown org: %v, want ErrNotMapped", err)
	}
	if _, err := m.ScopeOrgID(ctx, orgB); !errors.Is(err, ErrNotMapped) {
		t.Errorf("unknown org ScopeOrgID: %v, want ErrNotMapped", err)
	}
}

func TestStaticMapperScopeOrgIDIsTheOrgUUID(t *testing.T) {
	got, err := NewStaticMapper(map[string]uint32{orgA: 1}).ScopeOrgID(context.Background(), orgA)
	if err != nil {
		t.Fatal(err)
	}
	if got != orgA {
		t.Errorf("ScopeOrgID = %q, want %q", got, orgA)
	}
}

func TestNewStaticMapperCopiesInput(t *testing.T) {
	src := map[string]uint32{orgA: 1}
	m := NewStaticMapper(src)
	src[orgB] = 2 // must not leak into the mapper
	if _, err := m.VMAccountID(context.Background(), orgB); !errors.Is(err, ErrNotMapped) {
		t.Error("mapper aliased its input map")
	}
}

func TestVMTenantPath(t *testing.T) {
	if got := VMTenantPath(7); got != "7" {
		t.Errorf("VMTenantPath = %q", got)
	}
}
