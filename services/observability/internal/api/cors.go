package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// corsMaxAge is how long a browser may cache a preflight answer. Ten minutes
// keeps a dashboard from re-asking on every panel while still letting an
// allowlist change take effect inside one coffee break.
const corsMaxAge = 600

// corsAllowedHeaders is what a dashboard request actually carries. It is an
// allowlist rather than a mirror of Access-Control-Request-Headers, because
// echoing whatever was asked for makes the preflight a formality that always
// says yes.
var corsAllowedHeaders = []string{"Authorization", "Content-Type", "Accept"}

// corsAllowedMethods matches what the router mounts.
var corsAllowedMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// DevCORS allows a browser on another origin to call this API.
//
// It exists for one shape of local development: the Next dashboard on
// http://localhost:3100 talking to obsplane on http://localhost:8090. Those are
// different origins, so without this every fetch fails in the browser before it
// reaches any handler - and the failure surfaces as a network error, which is
// indistinguishable from "the backend is down".
//
// Four properties make this safe enough to ship as a development affordance,
// and each is a deliberate narrowing rather than a default:
//
//   - the origin must be in an explicit allowlist and is echoed verbatim. There
//     is no wildcard and no suffix matching, so no origin nobody configured is
//     ever granted.
//   - Access-Control-Allow-Credentials is never sent. The credential here is a
//     bearer token the page has to fetch and attach itself; a cross-site page
//     cannot obtain one, so there is nothing for a browser to attach on its own
//     and no CSRF surface to open. Turning credentials on next to an echoed
//     origin is what makes a permissive CORS policy dangerous.
//   - the allowed request headers are a fixed list, not a mirror of what the
//     preflight asked for.
//   - config.Load refuses OBS_DEV_CORS_ORIGINS in the production profile, so
//     this middleware is not mounted there at all. A production dashboard is
//     served same-origin or behind a gateway that owns its own policy.
//
// Returns nil when no origin is configured, so the caller mounts nothing rather
// than mounting a middleware that does nothing.
func DevCORS(origins []string) gin.HandlerFunc {
	allowed := map[string]bool{}
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = true
		}
	}
	if len(allowed) == 0 {
		return nil
	}

	allowMethods := strings.Join(corsAllowedMethods, ", ")
	allowHeaders := strings.Join(corsAllowedHeaders, ", ")

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		// Vary is set whether or not the origin matched: the response body is
		// the same but the headers are not, and a cache that ignores Origin
		// would serve one origin's allow header to another.
		c.Header("Vary", "Origin")

		if origin == "" || !allowed[origin] {
			if c.Request.Method == http.MethodOptions {
				// An unallowed preflight is answered without CORS headers. The
				// browser fails the request, which is the correct outcome, and
				// no handler runs for a method the route does not implement.
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			// A same-origin request, or a non-browser caller: nothing to add.
			// It is not rejected here - authentication is what decides that.
			c.Next()
			return
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", allowMethods)
		c.Header("Access-Control-Allow-Headers", allowHeaders)
		c.Header("Access-Control-Max-Age", strconv.Itoa(corsMaxAge))

		if c.Request.Method == http.MethodOptions {
			// The preflight is answered here and never reaches the tenancy
			// middleware. It has to be: a preflight carries no Authorization
			// header by specification, so letting it through would answer every
			// cross-origin call with a 401 that the browser reports as a CORS
			// failure.
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
