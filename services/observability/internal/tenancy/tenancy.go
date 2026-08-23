// Package tenancy resolves the caller for a request and carries their org into
// the database session.
//
// Three layers guard tenant isolation and authorization, and all three must
// hold:
//
//  1. Authentication (JWTAuthenticator): a signed token yields the org and the
//     role. Both come from the signature-verified payload, never from a header
//     or query parameter the caller controls.
//  2. This middleware, which rejects a request whose :orgId does not match the
//     org the credential authenticated (a 403 before any SQL runs), and
//     RequireRole, which rejects a caller whose role is too low for the route.
//  3. Postgres RLS, which filters on app.current_org inside db.WithOrg.
//
// Layer 2 gives a clean error and keeps handler code honest; layer 3 is the one
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

// ctxKey is unexported so no other package can plant a principal in the
// context.
type ctxKey struct{}

// ginKeyPrincipal is the gin.Context slot holding the resolved principal.
const ginKeyPrincipal = "klaro.principal"

// Authenticator maps an incoming request to the principal its credential
// represents.
//
// JWTAuthenticator is the real implementation. DevTokenAuthenticator remains as
// a local-development stub and is reachable only behind OBS_DEV_AUTH, so the
// default path is always signature-verified.
type Authenticator interface {
	// Authenticate returns the caller, or ok=false to reject with 401.
	Authenticate(c *gin.Context) (Principal, bool)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(c *gin.Context) (Principal, bool)

// Authenticate implements Authenticator.
func (f AuthenticatorFunc) Authenticate(c *gin.Context) (Principal, bool) { return f(c) }

// BearerToken returns the token from an Authorization: Bearer header.
//
// Anything else - Basic, a bare token, an empty value after the scheme - is not
// a bearer credential and is refused rather than guessed at.
func BearerToken(c *gin.Context) (string, bool) {
	token, found := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !found {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// DevTokenAuthenticator accepts one bearer token and pins it to one org and
// role, the same dev stub shape S1 uses
// (services/load-test/internal/api/middleware.go).
//
// It is a development convenience and nothing more: config.Load refuses to
// enable it unless OBS_DEV_AUTH is set, and refuses it outright in the
// production profile. An unset token authenticates nobody, so a half-configured
// deployment fails closed instead of accepting the empty string.
func DevTokenAuthenticator(token, orgID string, role Role) Authenticator {
	return AuthenticatorFunc(func(c *gin.Context) (Principal, bool) {
		if token == "" || !role.Valid() {
			return Principal{}, false
		}
		got, ok := BearerToken(c)
		if !ok || got != token {
			return Principal{}, false
		}
		return Principal{OrgID: orgID, Role: role, Subject: "dev"}, true
	})
}

// Outcome is why Resolve accepted or rejected a request.
//
// Resolve reports rather than writes, because the two transports disagree about
// how a rejection looks: REST answers with the shared JSON envelope, while a
// WebSocket has to say it in a close code (design section 4.3). Sharing the
// decision and splitting only the reporting keeps them from drifting apart -
// which would be the kind of drift where one transport quietly stops checking.
type Outcome int

// Resolve outcomes.
const (
	// OutcomeOK means the caller is authenticated and, if the route carries an
	// :orgId, addressing its own org.
	OutcomeOK Outcome = iota
	// OutcomeUnauthenticated: no credential, or one that did not resolve.
	OutcomeUnauthenticated
	// OutcomeForbidden: authenticated for one org, addressing another.
	OutcomeForbidden
	// OutcomeInvalidOrgParam: the :orgId in the path is not a uuid.
	OutcomeInvalidOrgParam
	// OutcomeServerError: the authenticator produced a principal that cannot be
	// used - a non-uuid org, or a role outside the four. Either is a server or
	// issuer bug and must never reach set_config or an authorization decision.
	OutcomeServerError
)

// Resolve authenticates the caller and matches the :orgId path parameter
// without writing anything to the response.
//
// On OutcomeOK the principal is attached to the context, exactly as Middleware
// would.
func Resolve(c *gin.Context, auth Authenticator) (Principal, Outcome) {
	p, ok := auth.Authenticate(c)
	if !ok {
		return Principal{}, OutcomeUnauthenticated
	}
	if !db.ValidOrgID(p.OrgID) || !p.Role.Valid() {
		return Principal{}, OutcomeServerError
	}
	if param := c.Param("orgId"); param != "" {
		if !db.ValidOrgID(param) {
			return Principal{}, OutcomeInvalidOrgParam
		}
		// The org that scopes the request is the one the credential vouched
		// for; the path is only allowed to name it, never to choose it. A
		// forged org_id claim therefore cannot reach another tenant's data - it
		// can only fail to match the path it was aimed at, and it has to
		// survive signature verification to get this far at all.
		if !strings.EqualFold(param, p.OrgID) {
			return Principal{}, OutcomeForbidden
		}
	}
	SetPrincipal(c, p)
	return p, OutcomeOK
}

// Middleware authenticates the caller and, when the route carries an :orgId
// path parameter, requires it to match.
func Middleware(auth Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch _, outcome := Resolve(c, auth); outcome {
		case OutcomeOK:
			c.Next()
		case OutcomeUnauthenticated:
			httpx.Unauthenticated(c, "missing or invalid credentials")
		case OutcomeInvalidOrgParam:
			httpx.Validation(c, "orgId must be a uuid", gin.H{"orgId": c.Param("orgId")})
		case OutcomeForbidden:
			// Cross-tenant attempt: authenticated for one org, addressing another.
			httpx.Forbidden(c, "org scope mismatch")
		default:
			httpx.Internal(c, "authenticated principal is not usable")
		}
	}
}

// RequireRole rejects a caller whose role is below min.
//
// It must be mounted after Middleware: with no principal on the context there
// is nothing to compare, and the answer is then 401 rather than 403 because the
// caller has not been identified at all.
func RequireRole(min Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := PrincipalFromGin(c)
		if !ok {
			httpx.Unauthenticated(c, "missing or invalid credentials")
			return
		}
		if !p.Role.AtLeast(min) {
			// The message names the requirement but not the caller's role: the
			// client already knows what it authenticated as, and echoing a
			// claim back is how claim values end up in someone's log pipeline.
			httpx.Forbidden(c, "role must be at least "+string(min))
			return
		}
		c.Next()
	}
}

// SetPrincipal stores the resolved caller on both the gin context and the
// request context, so handlers and anything reading the plain context.Context
// agree.
func SetPrincipal(c *gin.Context, p Principal) {
	c.Set(ginKeyPrincipal, p)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxKey{}, p))
}

// PrincipalFromGin returns the caller resolved by Middleware.
func PrincipalFromGin(c *gin.Context) (Principal, bool) {
	v, exists := c.Get(ginKeyPrincipal)
	p, isPrincipal := v.(Principal)
	return p, exists && isPrincipal && p.OrgID != ""
}

// FromGin returns the org resolved by Middleware.
func FromGin(c *gin.Context) (string, bool) {
	p, ok := PrincipalFromGin(c)
	return p.OrgID, ok
}

// FromContext returns the org carried on a plain context.
func FromContext(ctx context.Context) (string, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p.OrgID, ok && p.OrgID != ""
}

// PrincipalFromContext returns the caller carried on a plain context.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok && p.OrgID != ""
}

// WithContext attaches an org to a context (background jobs, tests).
//
// Background work carries no role, because giving it one would invent an
// authorization decision nobody made. RequireRole never sees these contexts -
// they do not pass through the HTTP surface.
func WithContext(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, Principal{OrgID: orgID})
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
