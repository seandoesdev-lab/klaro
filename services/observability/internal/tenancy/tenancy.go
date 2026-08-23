// Package tenancy resolves the caller org for a request and carries it into the
// database session.
//
// Two layers guard tenant isolation and both must hold:
//
//  1. This middleware, which rejects a request whose :orgId does not match the
//     org the credential authenticated (a 403 before any SQL runs).
//  2. Postgres RLS, which filters on app.current_org inside db.WithOrg.
//
// Layer 1 gives a clean error and keeps handler code honest; layer 2 is the one
// that actually cannot be bypassed by a handler that forgets to filter.
package tenancy

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/httpx"
)

// ctxKey is unexported so no other package can plant an org in the context.
type ctxKey struct{}

// ginKey is the gin.Context slot holding the resolved org.
const ginKey = "klaro.org_id"

// Authenticator maps an incoming request to the org its credential belongs to.
//
// Real auth (JWT/OAuth + Org > Project > Resource RBAC) is not in this
// foundation slice; DevTokenAuthenticator is the S1-equivalent stub and is the
// single place to swap in.
type Authenticator interface {
	// Authenticate returns the caller org id, or ok=false to reject with 401.
	Authenticate(c *gin.Context) (orgID string, ok bool)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(c *gin.Context) (string, bool)

// Authenticate implements Authenticator.
func (f AuthenticatorFunc) Authenticate(c *gin.Context) (string, bool) { return f(c) }

// DevTokenAuthenticator accepts one bearer token and pins it to one org, the
// same dev stub shape S1 uses (services/load-test/internal/api/middleware.go).
func DevTokenAuthenticator(token, orgID string) Authenticator {
	return AuthenticatorFunc(func(c *gin.Context) (string, bool) {
		if token == "" {
			return "", false
		}
		h := c.GetHeader("Authorization")
		got, found := strings.CutPrefix(h, "Bearer ")
		if !found || got != token {
			return "", false
		}
		return orgID, true
	})
}

// Middleware authenticates the caller and, when the route carries an :orgId
// path parameter, requires it to match.
func Middleware(auth Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		orgID, ok := auth.Authenticate(c)
		if !ok {
			httpx.Unauthenticated(c, "missing or invalid credentials")
			return
		}
		if !db.ValidOrgID(orgID) {
			// An authenticator that yields a non-uuid org is a server bug, not a
			// client error - never let it reach set_config.
			httpx.Internal(c, "authenticated org is not a valid uuid")
			return
		}

		if param := c.Param("orgId"); param != "" {
			if !db.ValidOrgID(param) {
				httpx.Validation(c, "orgId must be a uuid", gin.H{"orgId": param})
				return
			}
			// Cross-tenant attempt: authenticated for one org, addressing another.
			if !strings.EqualFold(param, orgID) {
				httpx.Forbidden(c, "org scope mismatch")
				return
			}
		}

		Set(c, orgID)
		c.Next()
	}
}

// Set stores the resolved org on both the gin context and the request context,
// so handlers and anything reading the plain context.Context agree.
func Set(c *gin.Context, orgID string) {
	c.Set(ginKey, orgID)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxKey{}, orgID))
}

// FromGin returns the org resolved by Middleware.
func FromGin(c *gin.Context) (string, bool) {
	v, ok := c.Get(ginKey)
	s, isStr := v.(string)
	return s, ok && isStr && s != ""
}

// FromContext returns the org carried on a plain context.
func FromContext(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(ctxKey{}).(string)
	return s, ok && s != ""
}

// WithContext attaches an org to a context (background jobs, tests).
func WithContext(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, orgID)
}

// InTx runs fn inside a transaction scoped to the request org.
//
// Handlers should reach the database only through this, so that the SET LOCAL
// and the query can never drift apart.
func InTx(c *gin.Context, d *db.DB, fn func(context.Context, pgx.Tx) error) error {
	orgID, ok := FromGin(c)
	if !ok {
		return db.ErrInvalidOrgID
	}
	return d.WithOrg(c.Request.Context(), orgID, fn)
}
