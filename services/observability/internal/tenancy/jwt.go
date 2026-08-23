package tenancy

import (
	"log"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/jwtauth"
)

// JWTAuthenticator authenticates callers from a signed bearer JWT.
//
// This is the default and only production credential for the public plane. The
// org and the role are read out of the verified payload, which is the whole
// point: a caller can present any org_id it likes, but an org_id it did not get
// from the issuer will not survive signature verification, and an org_id that
// does survive still has to match the :orgId in the path (Resolve) and then
// scopes the RLS session it opens (db.WithOrg).
//
// A claim that is present but unusable - an org_id that is not a uuid, a role
// outside the four - is a 401 rather than a 500. From the service's point of
// view that is an unusable credential, not an internal fault, and treating it
// as a server error would make a malformed token show up as a 5xx spike instead
// of as the authentication failure it is.
func JWTAuthenticator(v *jwtauth.Verifier) Authenticator {
	return AuthenticatorFunc(func(c *gin.Context) (Principal, bool) {
		if v == nil {
			// A nil verifier means the process was wired without a key. Nobody
			// gets in; config.Load is what should have caught it.
			return Principal{}, false
		}
		token, ok := BearerToken(c)
		if !ok {
			return Principal{}, false
		}
		claims, err := v.Verify(token)
		if err != nil {
			// Logged, not returned: the client is told only
			// "unauthenticated", so the response cannot be used to tell
			// "expired" from "wrong key". Nothing caller-controlled is
			// interpolated - jwtauth's errors name the algorithm and the
			// configured issuer/audience, never a claim value.
			log.Printf("auth: token rejected: %v", err)
			return Principal{}, false
		}
		if !db.ValidOrgID(claims.OrgID) {
			log.Print("auth: token carries a non-uuid org_id")
			return Principal{}, false
		}
		role, ok := ParseRole(claims.Role)
		if !ok {
			log.Print("auth: token carries an unknown role")
			return Principal{}, false
		}
		return Principal{OrgID: claims.OrgID, Role: role, Subject: claims.Subject}, true
	})
}
