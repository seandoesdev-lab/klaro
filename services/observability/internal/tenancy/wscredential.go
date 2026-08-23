package tenancy

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// BearerSubprotocol introduces a bearer token in a WebSocket handshake:
//
//	Sec-WebSocket-Protocol: klaro-bearer, <token>
//
// It exists because the browser WebSocket API cannot set request headers. The
// three ways out of that are a query parameter, a cookie, or the subprotocol
// list, and only the last one is both readable by a browser and kept out of
// places a URL ends up:
//
//   - a query parameter puts the credential in the access log of every proxy on
//     the path, in the browser's history, and in a Referer if the page ever
//     links out. A leaked token is a leaked org, so that is not a trade worth
//     making for a shorter code path.
//   - a cookie would be attached by the browser on its own, which is exactly
//     what makes a WebSocket cross-site forgeable and would force an origin
//     check to carry the whole authorization decision.
//
// The subprotocol header is sent once, by the client, on the handshake only.
const BearerSubprotocol = "klaro-bearer"

// ginKeyWSSubprotocol marks a request that may carry its credential in the
// handshake's subprotocol list.
//
// It is an opt-in per route rather than a global rule: every REST route keeps
// reading the Authorization header and nothing else, so widening where a
// credential may come from stays confined to the one handler that needs it
// (api.getLive). A route that never calls AllowWSSubprotocolCredential cannot
// be authenticated this way even by a client that sends the header.
const ginKeyWSSubprotocol = "klaro.ws_subprotocol_auth"

// AllowWSSubprotocolCredential permits this request's bearer token to arrive in
// Sec-WebSocket-Protocol. Call it before Resolve.
func AllowWSSubprotocolCredential(c *gin.Context) { c.Set(ginKeyWSSubprotocol, true) }

// wsSubprotocolAllowed reports whether the handshake credential was opted into.
func wsSubprotocolAllowed(c *gin.Context) bool {
	allowed, ok := c.Get(ginKeyWSSubprotocol)
	yes, isBool := allowed.(bool)
	return ok && isBool && yes
}

// SubprotocolToken reads the token that follows BearerSubprotocol in the
// handshake's requested subprotocols.
//
// Sec-WebSocket-Protocol is a comma-separated list which may also be split
// across several header lines, so both forms are flattened before looking for
// the marker. The token is whatever non-empty entry follows it; nothing else in
// the list is interpreted, so a client offering other subprotocols alongside
// ours is not broken by this.
func SubprotocolToken(r *http.Request) (string, bool) {
	protocols := Subprotocols(r)
	for i, p := range protocols {
		if p != BearerSubprotocol {
			continue
		}
		if i+1 < len(protocols) && protocols[i+1] != "" {
			return protocols[i+1], true
		}
		// The marker with nothing after it is a client bug, not a credential.
		return "", false
	}
	return "", false
}

// Subprotocols returns the handshake's requested subprotocols in order.
func Subprotocols(r *http.Request) []string {
	var out []string
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, raw := range strings.Split(header, ",") {
			if v := strings.TrimSpace(raw); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// OffersBearerSubprotocol reports whether the client offered BearerSubprotocol,
// which is what the server is allowed to echo back.
//
// RFC 6455 lets the server select at most one of the subprotocols the client
// offered, and a browser closes the connection if the server names one that was
// not offered - so "did they offer it" has to be asked before echoing.
func OffersBearerSubprotocol(r *http.Request) bool {
	for _, p := range Subprotocols(r) {
		if p == BearerSubprotocol {
			return true
		}
	}
	return false
}
