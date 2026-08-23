package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func do(r *gin.Engine, method, path, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// keyRoutes are every route added for OBS-02. They must all sit inside the
// org-scoped group: one of them mounted outside it would serve the keys of
// another tenant to whoever asked.
var keyRoutes = []struct {
	method string
	path   string
}{
	{http.MethodPost, "/obs/keys"},
	{http.MethodGet, "/obs/keys"},
	{http.MethodPost, "/obs/keys/00000000-0000-0000-0000-0000000000c1/rotate"},
	{http.MethodDelete, "/obs/keys/00000000-0000-0000-0000-0000000000c1"},
	{http.MethodGet, "/obs/quota"},
}

func TestKeyRoutesRequireACredential(t *testing.T) {
	r := NewRouter(deps())
	for _, rt := range keyRoutes {
		w := do(r, rt.method, "/orgs/"+orgA+rt.path, "", "{}")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a credential = %d, want 401 (body %s)", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// Authenticated for orgA, addressing orgB: must be refused by the guard, not by
// a handler. A nil DB makes the distinction visible - reaching a handler would
// produce a 500 instead.
func TestKeyRoutesRejectCrossTenant(t *testing.T) {
	r := NewRouter(deps())
	for _, rt := range keyRoutes {
		w := do(r, rt.method, "/orgs/"+orgB+rt.path, "Bearer dev", "{}")
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s cross-tenant = %d, want 403 (body %s)", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// An unparseable body must be rejected before the store is consulted. Nothing
// but the 422 can prove that here: the deps carry no database, so any path that
// reached the store would panic.
func TestPostKeyRejectsUnparseableBody(t *testing.T) {
	w := do(NewRouter(deps()), http.MethodPost, "/orgs/"+orgA+"/obs/keys", "Bearer dev", "not json")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Errorf("body = %s, want the shared error envelope", w.Body)
	}
}

// The response type that carries a secret is separate from the stored model, so
// a read projection cannot grow a secret field by accident. This asserts the
// stored model has nothing secret to leak in the first place.
func TestStoredKeyModelHasNoSecretField(t *testing.T) {
	fields := keyModelJSONFields()
	for _, name := range []string{"secret", "key_hash"} {
		if strings.Contains(fields, name) {
			t.Errorf("the key model serialises %q (tags: %s)", name, fields)
		}
	}
}
