package tenancy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/platform/httpx"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	orgA = "00000000-0000-0000-0000-00000000000a"
	orgB = "00000000-0000-0000-0000-00000000000b"
)

// router mounts an org-scoped route guarded by Middleware and echoes the org
// the handler observed.
func router(auth Authenticator) *gin.Engine {
	r := gin.New()
	r.GET("/orgs/:orgId/obs/keys", Middleware(auth), func(c *gin.Context) {
		org, ok := FromGin(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"org": nil})
			return
		}
		ctxOrg, ctxOK := FromContext(c.Request.Context())
		c.JSON(http.StatusOK, gin.H{"org": org, "ctx_org": ctxOrg, "ctx_ok": ctxOK})
	})
	// A route with no :orgId still authenticates but has nothing to compare.
	r.GET("/obs/self", Middleware(auth), func(c *gin.Context) {
		org, _ := FromGin(c)
		c.JSON(http.StatusOK, gin.H{"org": org})
	})
	return r
}

func do(r *gin.Engine, path, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var b httpx.Body
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	return b.Error.Code
}

func TestMiddlewareResolvesOrg(t *testing.T) {
	r := router(DevTokenAuthenticator("dev", orgA))
	w := do(r, "/orgs/"+orgA+"/obs/keys", "Bearer dev")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["org"] != orgA {
		t.Errorf("gin org = %v, want %s", got["org"], orgA)
	}
	if got["ctx_org"] != orgA || got["ctx_ok"] != true {
		t.Errorf("request context org = %v (ok=%v)", got["ctx_org"], got["ctx_ok"])
	}
}

// The core cross-tenant guard: a credential for org A addressing org B is
// refused before any query runs.
func TestMiddlewareRejectsCrossTenantPath(t *testing.T) {
	r := router(DevTokenAuthenticator("dev", orgA))
	w := do(r, "/orgs/"+orgB+"/obs/keys", "Bearer dev")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", w.Code, w.Body)
	}
	if code := errCode(t, w); code != httpx.CodeForbidden {
		t.Errorf("code = %q, want %q", code, httpx.CodeForbidden)
	}
}

func TestMiddlewareRejectsMissingAndBadCredentials(t *testing.T) {
	r := router(DevTokenAuthenticator("dev", orgA))
	for _, h := range []string{"", "Bearer wrong", "dev", "Basic dev"} {
		w := do(r, "/orgs/"+orgA+"/obs/keys", h)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Authorization=%q: status = %d, want 401", h, w.Code)
			continue
		}
		if code := errCode(t, w); code != httpx.CodeUnauthenticated {
			t.Errorf("Authorization=%q: code = %q", h, code)
		}
	}
}

func TestMiddlewareRejectsMalformedOrgParam(t *testing.T) {
	r := router(DevTokenAuthenticator("dev", orgA))
	w := do(r, "/orgs/not-a-uuid/obs/keys", "Bearer dev")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
	if code := errCode(t, w); code != httpx.CodeValidation {
		t.Errorf("code = %q", code)
	}
}

// A SQL-injection shaped :orgId must be rejected by the uuid guard rather than
// reaching set_config.
func TestMiddlewareRejectsInjectionShapedOrgParam(t *testing.T) {
	r := router(DevTokenAuthenticator("dev", orgA))
	w := do(r, "/orgs/"+orgA+"%27%3B%20DROP%20TABLE%20observability_keys--/obs/keys", "Bearer dev")
	if w.Code == http.StatusOK {
		t.Fatalf("injection-shaped orgId was accepted: %s", w.Body)
	}
}

func TestMiddlewareAllowsRouteWithoutOrgParam(t *testing.T) {
	r := router(DevTokenAuthenticator("dev", orgA))
	w := do(r, "/obs/self", "Bearer dev")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
}

// An authenticator returning a non-uuid org is a server bug; the request must
// fail 500 rather than be handed to the database layer.
func TestMiddlewareRejectsNonUUIDAuthenticatedOrg(t *testing.T) {
	r := router(AuthenticatorFunc(func(*gin.Context) (string, bool) { return "org-one", true }))
	w := do(r, "/obs/self", "Bearer dev")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body)
	}
}

func TestDevTokenAuthenticatorRejectsEmptyConfiguredToken(t *testing.T) {
	a := DevTokenAuthenticator("", orgA)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := a.Authenticate(c); ok {
		t.Error("an unset dev token must not authenticate anyone")
	}
}

func TestFromContextEmpty(t *testing.T) {
	if _, ok := FromContext(httptest.NewRequest(http.MethodGet, "/", nil).Context()); ok {
		t.Error("bare context should carry no org")
	}
}
