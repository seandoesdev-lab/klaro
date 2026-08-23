package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/live"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/platform/redisx"
	"github.com/klaro/observability/internal/tenancy"
	"github.com/klaro/observability/internal/tenants"
)

const (
	orgA = "00000000-0000-0000-0000-00000000000a"
	orgB = "00000000-0000-0000-0000-00000000000b"
)

// deps builds a router with no database: the health route and the tenancy guard
// are reachable without one, and the tenant route is expected to fail past the
// guard rather than be allowed through it.
func deps() Deps {
	return Deps{
		DB:      nil,
		Signal:  redisx.NewMemory(),
		Tenants: tenants.NewStaticMapper(map[string]uint32{orgA: 1}),
		Auth:    tenancy.DevTokenAuthenticator("dev", orgA),
		// No backend URLs: the explorer handlers are reachable but every query
		// reports an unconfigured backend, which is what the routing tests want.
		Explorer: explorer.New(explorer.Config{}, tenants.NewStaticMapper(map[string]uint32{orgA: 1}), nil),
		Live:     live.NewHub(redisx.NewMemory()),
	}
}

func get(r *gin.Engine, path, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHealthzIsUnauthenticated(t *testing.T) {
	w := get(NewRouter(deps()), "/healthz", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

func TestReadyzReportsMissingDatabase(t *testing.T) {
	w := get(NewRouter(deps()), "/readyz", "")
	if w.Code == http.StatusOK {
		t.Fatal("readyz must not report ready without a database")
	}
}

// The org-scoped group must reject before reaching a handler, which is why this
// passes with a nil DB: a leak would surface as a 500 from the handler instead.
func TestOrgScopedGroupRejectsCrossTenant(t *testing.T) {
	r := NewRouter(deps())

	w := get(r, "/orgs/"+orgB+"/obs/tenant", "Bearer dev")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", w.Code, w.Body)
	}
	var b httpx.Body
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Error.Code != httpx.CodeForbidden {
		t.Errorf("code = %q", b.Error.Code)
	}
}

func TestOrgScopedGroupRequiresCredentials(t *testing.T) {
	w := get(NewRouter(deps()), "/orgs/"+orgA+"/obs/tenant", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body)
	}
}

func TestErrorsUseTheSharedEnvelope(t *testing.T) {
	w := get(NewRouter(deps()), "/orgs/"+orgA+"/obs/tenant", "")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["error"]; !ok {
		t.Errorf("response %s is not wrapped in the shared error envelope", w.Body)
	}
}
