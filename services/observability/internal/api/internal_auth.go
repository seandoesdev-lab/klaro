package api

import (
	"crypto/subtle"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/tenancy"
)

// internalRealm is the WWW-Authenticate value on an internal-plane rejection.
// It is the wire contract the Collector matches on, so it must stay in step with
// collector/klaroauth (internalRealm there).
const internalRealm = `Bearer realm="klaro-internal"`

// InternalAuth authenticates a caller on the internal plane.
//
// This exists because transport security and authentication are two different
// things, and OBS_INTERNAL_INSECURE used to switch off both at once (finding
// F-3). Serving the internal plane in plaintext for a local compose run is a
// reasonable convenience; serving it *unauthenticated* means anything that can
// reach the port can resolve ingest keys, publish live frames for any org,
// write usage rows and inject alert events. Those two properties are separable,
// so they are now separate.
//
// Two credentials are accepted, and a caller needs one of them:
//
//   - a verified client certificate. mTLS is the contract for this hop
//     (CLAUDE.md: "Control Plane <-> Worker ... mTLS 필수"), and
//     mtls.ServerConfig uses RequireAndVerifyClientCert, so a non-empty
//     VerifiedChains means the TLS stack already authenticated the peer against
//     the configured CA. No token is needed on top of that.
//   - a shared bearer token, compared in constant time. This is what keeps the
//     plane authenticated when TLS is off: config.Load refuses to enable
//     OBS_INTERNAL_INSECURE without OBS_INTERNAL_TOKEN, so "plaintext" can no
//     longer mean "open".
//
// A request with neither is refused. An empty configured token authenticates
// nobody rather than everybody: the failure mode of a missing secret has to be
// a closed door.
func InternalAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if state := c.Request.TLS; state != nil && len(state.VerifiedChains) > 0 {
			c.Next()
			return
		}
		if token != "" {
			if got, ok := tenancy.BearerToken(c); ok &&
				subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
				c.Next()
				return
			}
		}
		// The realm distinguishes "the gateway itself is not authenticated"
		// from "the ingest key it asked about is not authorized" - both are 401
		// on /internal/authz/ingest-key, and confusing them would make a
		// mistyped internal token look like every customer's key being revoked.
		// The Collector keys off this (klaroauth.ask).
		c.Header("WWW-Authenticate", internalRealm)
		// Beyond that, one answer for "no credential", "wrong token" and "no
		// client cert": the caller is our own Collector, so a precise reason
		// helps an attacker more than it helps an operator, who has the log.
		httpx.Unauthenticated(c, "internal plane requires a client certificate or the internal token")
	}
}
