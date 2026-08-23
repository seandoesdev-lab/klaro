package api

import (
	"net/http"
	"strings"
	"testing"
)

// alertRoutes are every endpoint added for OBS-06/07. All of them must sit
// inside the org-scoped group.
var alertRoutes = []struct {
	method string
	path   string
}{
	{http.MethodPost, "/obs/alert-rules"},
	{http.MethodGet, "/obs/alert-rules"},
	{http.MethodGet, "/obs/alert-rules/00000000-0000-0000-0000-0000000000c1"},
	{http.MethodPatch, "/obs/alert-rules/00000000-0000-0000-0000-0000000000c1"},
	{http.MethodDelete, "/obs/alert-rules/00000000-0000-0000-0000-0000000000c1"},
	{http.MethodGet, "/obs/alert-events"},
}

func TestAlertRoutesRequireACredential(t *testing.T) {
	r := NewRouter(deps())
	for _, rt := range alertRoutes {
		w := do(r, rt.method, "/orgs/"+orgA+rt.path, "", "{}")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a credential = %d, want 401", rt.method, rt.path, w.Code)
		}
	}
}

func TestAlertRoutesRejectCrossTenant(t *testing.T) {
	r := NewRouter(deps())
	for _, rt := range alertRoutes {
		w := do(r, rt.method, "/orgs/"+orgB+rt.path, "Bearer dev", "{}")
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s cross-tenant = %d, want 403", rt.method, rt.path, w.Code)
		}
	}
}

// An unsupported signal is refused at the edge with the shared envelope. The
// evaluator is metric-only, and a stored log rule would silently never fire.
func TestPostAlertRuleRefusesNonMetricSignal(t *testing.T) {
	body := `{"name":"log rule","signal":"log","comparator":"gt","threshold":1,
	          "query_spec":{"metric":"cpu"}}`
	w := do(NewRouter(deps()), http.MethodPost, "/orgs/"+orgA+"/obs/alert-rules", "Bearer dev", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Errorf("body = %s, want the shared error envelope", w.Body)
	}
}

// A rule may not restate the org matcher the server injects.
func TestPostAlertRuleRefusesReservedFilterLabel(t *testing.T) {
	body := `{"name":"sneaky","comparator":"gt","threshold":1,
	          "query_spec":{"metric":"cpu","filters":[{"label":"klaro_org_id","value":"other"}]}}`
	w := do(NewRouter(deps()), http.MethodPost, "/orgs/"+orgA+"/obs/alert-rules", "Bearer dev", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
}

func TestPostAlertRuleRejectsUnparseableBody(t *testing.T) {
	w := do(NewRouter(deps()), http.MethodPost, "/orgs/"+orgA+"/obs/alert-rules", "Bearer dev", "not json")
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
}

// An unknown comparator must be refused rather than rendered into MetricsQL.
func TestPostAlertRuleRefusesUnknownComparator(t *testing.T) {
	body := `{"name":"x","comparator":"approximately","threshold":1,"query_spec":{"metric":"cpu"}}`
	w := do(NewRouter(deps()), http.MethodPost, "/orgs/"+orgA+"/obs/alert-rules", "Bearer dev", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
}
