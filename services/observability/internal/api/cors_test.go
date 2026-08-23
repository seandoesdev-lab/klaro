package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

const devOrigin = "http://localhost:3100"

func corsRouter(t *testing.T, origins ...string) *gin.Engine {
	t.Helper()
	d := deps()
	d.DevCORSOrigins = origins
	return NewRouter(d)
}

func corsCall(r *gin.Engine, method, path, origin, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// The preflight has to be answered before authentication. A browser sends it
// without an Authorization header by specification, so a router that let it
// reach the tenancy middleware would answer every cross-origin call with a 401
// that the browser then reports as a CORS failure - two wrong diagnoses from one
// missing middleware.
func TestPreflightIsAnsweredWithoutACredential(t *testing.T) {
	r := corsRouter(t, devOrigin)

	w := corsCall(r, http.MethodOptions, "/orgs/"+orgA+"/obs/metrics/query", devOrigin, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", w.Code, w.Body)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != devOrigin {
		t.Errorf("allow-origin = %q, want %q", got, devOrigin)
	}
	// Without Authorization in the allowed list the browser drops the real
	// request, so this is the header that makes the whole thing work.
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" ||
		!containsHeader(got, "Authorization") {
		t.Errorf("allow-headers = %q, want it to include Authorization", got)
	}
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Errorf("vary = %q, want Origin", got)
	}
}

func TestAllowedOriginIsEchoedOnARealRequest(t *testing.T) {
	r := corsRouter(t, devOrigin)

	// /healthz needs no credential, so this isolates the CORS headers from the
	// authorization result.
	w := corsCall(r, http.MethodGet, "/healthz", devOrigin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != devOrigin {
		t.Errorf("allow-origin = %q, want %q", got, devOrigin)
	}
	// Credentials must stay off: the token is attached by the page, so a browser
	// has nothing of its own to send, and turning this on next to an echoed
	// origin is what makes a permissive policy dangerous.
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("allow-credentials = %q, want it unset", got)
	}
}

// An origin nobody configured gets no grant. There is no wildcard and no suffix
// matching, so a page on another port cannot read the response.
func TestUnknownOriginGetsNoGrant(t *testing.T) {
	r := corsRouter(t, devOrigin)

	w := corsCall(r, http.MethodGet, "/healthz", "http://evil.example", "")
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q, want it unset", got)
	}
	// Vary is still set: the answer depends on Origin either way, and a cache
	// that ignored it would hand one origin's grant to another.
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Errorf("vary = %q, want Origin", got)
	}
}

// With nothing configured the middleware is not mounted at all, so no response
// carries a grant. This is the production shape.
func TestNoOriginsConfiguredMountsNothing(t *testing.T) {
	if DevCORS(nil) != nil {
		t.Error("DevCORS(nil) returned a handler")
	}
	if DevCORS([]string{"", "  "}) != nil {
		t.Error("DevCORS with only blank entries returned a handler")
	}

	w := corsCall(corsRouter(t), http.MethodGet, "/healthz", devOrigin, "")
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q, want it unset", got)
	}
}

// CORS must not become an authorization bypass: the grant lets the browser read
// the answer, it does not decide what the answer is.
func TestCORSDoesNotBypassAuthentication(t *testing.T) {
	r := corsRouter(t, devOrigin)

	w := corsCall(r, http.MethodGet, "/orgs/"+orgA+"/obs/quota", devOrigin, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body)
	}
	// Cross-tenant is still cross-tenant with a valid credential.
	if w := corsCall(r, http.MethodGet, "/orgs/"+orgB+"/obs/quota", devOrigin, "Bearer dev"); w.Code != http.StatusForbidden {
		t.Errorf("cross-tenant status = %d, want 403 (body %s)", w.Code, w.Body)
	}
}

func containsHeader(list, want string) bool {
	for _, h := range splitList(list) {
		if h == want {
			return true
		}
	}
	return false
}

func splitList(list string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(list); i++ {
		if i == len(list) || list[i] == ',' {
			for start < i && (list[start] == ' ' || list[start] == '\t') {
				start++
			}
			if start < i {
				out = append(out, list[start:i])
			}
			start = i + 1
		}
	}
	return out
}
