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

func TestMemoryMapperIsStableAndCollisionFree(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryMapper()

	a1, err := m.VMAccountID(ctx, orgA)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := m.VMAccountID(ctx, orgA)
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Errorf("same org got %d then %d", a1, a2)
	}

	b, err := m.VMAccountID(ctx, orgB)
	if err != nil {
		t.Fatal(err)
	}
	if b == a1 {
		t.Fatalf("orgs A and B share AccountID %d; their metrics would merge in VM", b)
	}
}

// AccountID 0 is the VictoriaMetrics default account. Handing it to an org
// would mix that tenant into the shared bucket.
func TestMemoryMapperNeverReturnsZero(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryMapper()
	for _, org := range []string{orgA, orgB, orgC} {
		id, err := m.VMAccountID(ctx, org)
		if err != nil {
			t.Fatal(err)
		}
		if id == 0 {
			t.Fatalf("org %s mapped to AccountID 0", org)
		}
	}
}

func TestMemoryMapperConcurrentAssignmentIsUnique(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryMapper()
	orgs := []string{orgA, orgB, orgC}

	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]uint32{}
	for i := 0; i < 50; i++ {
		for _, org := range orgs {
			wg.Add(1)
			go func(org string) {
				defer wg.Done()
				id, err := m.VMAccountID(ctx, org)
				if err != nil {
					t.Errorf("VMAccountID: %v", err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if prev, ok := seen[org]; ok && prev != id {
					t.Errorf("org %s got both %d and %d", org, prev, id)
				}
				seen[org] = id
			}(org)
		}
	}
	wg.Wait()

	byID := map[uint32]string{}
	for org, id := range seen {
		if other, dup := byID[id]; dup {
			t.Errorf("AccountID %d shared by %s and %s", id, org, other)
		}
		byID[id] = org
	}
}

func TestScopeOrgIDIsTheOrgUUID(t *testing.T) {
	got, err := NewMemoryMapper().ScopeOrgID(context.Background(), orgA)
	if err != nil {
		t.Fatal(err)
	}
	if got != orgA {
		t.Errorf("ScopeOrgID = %q, want %q", got, orgA)
	}
}

func TestMappersRejectMalformedOrg(t *testing.T) {
	ctx := context.Background()
	bad := []string{"", "org-a", "x' OR 1=1--"}
	for _, m := range []Mapper{NewMemoryMapper(), NewStaticMapper(map[string]uint32{orgA: 1})} {
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
