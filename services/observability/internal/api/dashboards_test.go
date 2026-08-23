package api

import (
	"net/http"
	"strings"
	"testing"
)

// dashboardRoutes are every endpoint added for OBS-09. All must sit inside the
// org-scoped group.
var dashboardRoutes = []struct {
	method string
	path   string
}{
	{http.MethodPost, "/obs/dashboards"},
	{http.MethodGet, "/obs/dashboards"},
	{http.MethodGet, "/obs/dashboards/00000000-0000-0000-0000-0000000000d1"},
	{http.MethodPatch, "/obs/dashboards/00000000-0000-0000-0000-0000000000d1"},
	{http.MethodDelete, "/obs/dashboards/00000000-0000-0000-0000-0000000000d1"},
}

func TestDashboardRoutesRequireACredential(t *testing.T) {
	r := NewRouter(deps())
	for _, rt := range dashboardRoutes {
		w := do(r, rt.method, "/orgs/"+orgA+rt.path, "", "{}")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a credential = %d, want 401", rt.method, rt.path, w.Code)
		}
	}
}

func TestDashboardRoutesRejectCrossTenant(t *testing.T) {
	r := NewRouter(deps())
	for _, rt := range dashboardRoutes {
		w := do(r, rt.method, "/orgs/"+orgB+rt.path, "Bearer dev", "{}")
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s cross-tenant = %d, want 403", rt.method, rt.path, w.Code)
		}
	}
}

// A panel naming the org label would be a stored cross-tenant query, so it is
// refused at the edge with the shared envelope.
func TestPostDashboardRefusesReservedPanelLabel(t *testing.T) {
	body := `{"name":"sneaky","spec":{"panels":[{"id":"p1","type":"timeseries",
	  "layout":{"x":0,"y":0,"w":6,"h":4},
	  "query":{"signal":"metrics","metric":"cpu",
	           "filters":[{"label":"klaro_org_id","value":"other"}]}}]}}`
	w := do(NewRouter(deps()), http.MethodPost, "/orgs/"+orgA+"/obs/dashboards", "Bearer dev", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Errorf("body = %s, want the shared error envelope", w.Body)
	}
}

func TestPostDashboardRejectsUnparseableBody(t *testing.T) {
	w := do(NewRouter(deps()), http.MethodPost, "/orgs/"+orgA+"/obs/dashboards", "Bearer dev", "not json")
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
}

// A dashboard has to have a name: it is how it is found.
func TestPostDashboardRequiresAName(t *testing.T) {
	w := do(NewRouter(deps()), http.MethodPost, "/orgs/"+orgA+"/obs/dashboards", "Bearer dev", `{"spec":{"panels":[]}}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
}

// The snapshot route is internal-only, and validates before touching a backend.
func TestSnapshotRouteValidates(t *testing.T) {
	d, _ := internalDeps()
	for _, body := range []string{`{"org_id":"not-a-uuid"}`, `not json`} {
		w := postInternalWith(d, "/internal/snapshot", body)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %q = %d, want 422 (%s)", body, w.Code, w.Body)
		}
	}
}
