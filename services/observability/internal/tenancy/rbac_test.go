package tenancy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/platform/jwtauth"
)

// These tests drive the real JWT path rather than the dev stub, because the
// questions they ask - can a forged claim reach another org, does a low role
// reach a write route - are only meaningful against the credential production
// actually uses.

const jwtSecret = "test-secret-that-is-long-enough!!" // 32 bytes

// mintToken signs a token with the test secret. Claims are passed as a map so a
// test can leave one out or put something unusable in.
func mintToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	h, err := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	signed := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	mac := hmac.New(sha256.New, []byte(jwtSecret))
	mac.Write([]byte(signed))
	return signed + "." + enc.EncodeToString(mac.Sum(nil))
}

func tokenFor(t *testing.T, org string, role Role) string {
	t.Helper()
	return mintToken(t, map[string]any{
		"sub":    "user-" + string(role),
		"org_id": org,
		"role":   string(role),
		"exp":    time.Now().Add(time.Hour).Unix(),
	})
}

func jwtAuth(t *testing.T) Authenticator {
	t.Helper()
	v, err := jwtauth.New(jwtauth.Options{Alg: jwtauth.HS256, HSSecret: []byte(jwtSecret)})
	if err != nil {
		t.Fatal(err)
	}
	return JWTAuthenticator(v)
}

// rbacRouter mirrors the shape of the real router: one authenticated org group
// with a member-gated read route and an admin-gated write route.
func rbacRouter(auth Authenticator) *gin.Engine {
	r := gin.New()
	org := r.Group("/orgs/:orgId", Middleware(auth))
	org.GET("/obs/quota", RequireRole(RoleMember), func(c *gin.Context) {
		p, _ := PrincipalFromGin(c)
		c.JSON(http.StatusOK, gin.H{"org": p.OrgID, "role": string(p.Role), "sub": p.Subject})
	})
	org.POST("/obs/keys", RequireRole(RoleAdmin), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"created": true})
	})
	return r
}

func call(r *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestJWTAuthCarriesOrgAndRoleToTheHandler(t *testing.T) {
	r := rbacRouter(jwtAuth(t))
	w := call(r, http.MethodGet, "/orgs/"+orgA+"/obs/quota", tokenFor(t, orgA, RoleAdmin))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["org"] != orgA || got["role"] != "admin" || got["sub"] != "user-admin" {
		t.Errorf("principal reached the handler as %v", got)
	}
}

// No credential at all is a 401 on every org-scoped route, read or write.
func TestNoTokenIsUnauthenticated(t *testing.T) {
	r := rbacRouter(jwtAuth(t))
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/orgs/" + orgA + "/obs/quota"},
		{http.MethodPost, "/orgs/" + orgA + "/obs/keys"},
	} {
		w := call(r, tc.method, tc.path, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", tc.method, tc.path, w.Code)
			continue
		}
		if code := errCode(t, w); code != httpx.CodeUnauthenticated {
			t.Errorf("%s %s: code = %q", tc.method, tc.path, code)
		}
	}
}

// A token signed with the wrong key must not authenticate, even though every
// claim in it is well formed. This is the check the dev stub could never make.
func TestForeignlySignedTokenIsUnauthenticated(t *testing.T) {
	other := mintToken(t, map[string]any{
		"org_id": orgA, "role": "owner", "exp": time.Now().Add(time.Hour).Unix(),
	})
	v, err := jwtauth.New(jwtauth.Options{
		Alg: jwtauth.HS256, HSSecret: []byte("a-completely-different-secret!!!!"),
	})
	if err != nil {
		t.Fatal(err)
	}
	w := call(rbacRouter(JWTAuthenticator(v)), http.MethodGet, "/orgs/"+orgA+"/obs/quota", other)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body)
	}
}

// Role gating. The observability plane's read threshold is member, so viewer -
// which is a read-only role elsewhere in klaro - has no grant here yet; raising
// or lowering that is one argument in the router.
func TestRoleGatesReadAndWrite(t *testing.T) {
	r := rbacRouter(jwtAuth(t))
	tests := []struct {
		role      Role
		wantRead  int
		wantWrite int
	}{
		{RoleViewer, http.StatusForbidden, http.StatusForbidden},
		{RoleMember, http.StatusOK, http.StatusForbidden},
		{RoleAdmin, http.StatusOK, http.StatusOK},
		{RoleOwner, http.StatusOK, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(string(tc.role), func(t *testing.T) {
			token := tokenFor(t, orgA, tc.role)
			if w := call(r, http.MethodGet, "/orgs/"+orgA+"/obs/quota", token); w.Code != tc.wantRead {
				t.Errorf("read status = %d, want %d (body %s)", w.Code, tc.wantRead, w.Body)
			}
			w := call(r, http.MethodPost, "/orgs/"+orgA+"/obs/keys", token)
			if w.Code != tc.wantWrite {
				t.Errorf("write status = %d, want %d (body %s)", w.Code, tc.wantWrite, w.Body)
			}
			if tc.wantWrite == http.StatusForbidden {
				if code := errCode(t, w); code != httpx.CodeForbidden {
					t.Errorf("write code = %q, want %q", code, httpx.CodeForbidden)
				}
			}
		})
	}
}

// A role outside the four is not a lesser role - it is no role. The credential
// is unusable, so the answer is 401 and never a silent downgrade to viewer.
func TestUnknownRoleClaimIsUnauthenticated(t *testing.T) {
	token := mintToken(t, map[string]any{
		"org_id": orgA, "role": "superadmin", "exp": time.Now().Add(time.Hour).Unix(),
	})
	w := call(rbacRouter(jwtAuth(t)), http.MethodGet, "/orgs/"+orgA+"/obs/quota", token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body)
	}
}

// Tenant isolation under the real credential: an owner of org A gets nothing in
// org B, on read or write.
func TestOrgIsolationHoldsUnderJWT(t *testing.T) {
	r := rbacRouter(jwtAuth(t))
	token := tokenFor(t, orgA, RoleOwner)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/orgs/" + orgB + "/obs/quota"},
		{http.MethodPost, "/orgs/" + orgB + "/obs/keys"},
	} {
		w := call(r, tc.method, tc.path, token)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403 (body %s)", tc.method, tc.path, w.Code, w.Body)
			continue
		}
		if code := errCode(t, w); code != httpx.CodeForbidden {
			t.Errorf("%s %s: code = %q", tc.method, tc.path, code)
		}
	}
}

// A forged org claim has two barriers. Unsigned, it never authenticates; signed
// by the issuer, it still only grants the org it names, so aiming it at another
// org's path is a 403 - and the RLS scope it opens is its own org either way.
func TestForgedOrgClaimCannotReachAnotherTenant(t *testing.T) {
	// (a) The attacker edits org_id in a token they hold. The signature no
	//     longer covers the payload.
	tampered := tamperOrg(t, tokenFor(t, orgA, RoleOwner), orgB)
	if w := call(rbacRouter(jwtAuth(t)), http.MethodGet, "/orgs/"+orgB+"/obs/quota", tampered); w.Code != http.StatusUnauthorized {
		t.Errorf("tampered org_id: status = %d, want 401 (body %s)", w.Code, w.Body)
	}

	// (b) A properly signed token for org B addressing org A. The claim decides
	//     the scope, so the path cannot widen it.
	forB := tokenFor(t, orgB, RoleOwner)
	if w := call(rbacRouter(jwtAuth(t)), http.MethodGet, "/orgs/"+orgA+"/obs/quota", forB); w.Code != http.StatusForbidden {
		t.Errorf("org B token on org A path: status = %d, want 403 (body %s)", w.Code, w.Body)
	}

	// (c) The org a valid token opens is the claim's, never the path's - which
	//     is what makes db.WithOrg's SET LOCAL safe.
	w := call(rbacRouter(jwtAuth(t)), http.MethodGet, "/orgs/"+orgB+"/obs/quota", forB)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["org"] != orgB {
		t.Errorf("scoped org = %v, want %s", got["org"], orgB)
	}
}

// tamperOrg rewrites org_id in the payload and leaves the original signature in
// place, which is exactly what an attacker holding a valid token can do.
func tamperOrg(t *testing.T, token, org string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments", len(parts))
	}
	enc := base64.RawURLEncoding
	raw, err := enc.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	claims["org_id"] = org
	out, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return parts[0] + "." + enc.EncodeToString(out) + "." + parts[2]
}

// RequireRole without an authenticated principal is a 401: there is nothing to
// compare a role against, and answering 403 would claim the caller had been
// identified.
func TestRequireRoleWithoutPrincipalIsUnauthenticated(t *testing.T) {
	r := gin.New()
	r.GET("/bare", RequireRole(RoleMember), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	if w := call(r, http.MethodGet, "/bare", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRoleRanking(t *testing.T) {
	if !RoleOwner.AtLeast(RoleAdmin) || !RoleAdmin.AtLeast(RoleMember) || !RoleMember.AtLeast(RoleViewer) {
		t.Error("role ranking is not monotonic")
	}
	if RoleViewer.AtLeast(RoleMember) || RoleMember.AtLeast(RoleAdmin) || RoleAdmin.AtLeast(RoleOwner) {
		t.Error("a lower role satisfied a higher requirement")
	}
	// An unknown role has no rank in either position, so neither a bad claim
	// nor a typo in the route table can open an endpoint.
	if Role("root").AtLeast(RoleViewer) {
		t.Error("an unknown role satisfied a requirement")
	}
	if RoleOwner.AtLeast(Role("superuser")) {
		t.Error("an unknown requirement was satisfied")
	}
	if r, ok := ParseRole("  Admin "); !ok || r != RoleAdmin {
		t.Errorf("ParseRole(%q) = %q, %v", "  Admin ", r, ok)
	}
	if _, ok := ParseRole("billing"); ok {
		t.Error("ParseRole accepted a role outside the four")
	}
}
