package tenancy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// requestWithProtocols builds a gin context whose request carries the given
// Sec-WebSocket-Protocol header lines.
func requestWithProtocols(lines ...string) *gin.Context {
	req := httptest.NewRequest(http.MethodGet, "/orgs/x/obs/live", nil)
	for _, l := range lines {
		req.Header.Add("Sec-WebSocket-Protocol", l)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

func TestSubprotocolTokenReadsTheValueAfterTheMarker(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
		ok    bool
	}{
		{"one line", []string{"klaro-bearer, tok"}, "tok", true},
		{"no space", []string{"klaro-bearer,tok"}, "tok", true},
		// A browser may fold the list, and Go's Header.Values keeps the lines
		// separate, so both shapes have to flatten to the same list.
		{"two lines", []string{"klaro-bearer", "tok"}, "tok", true},
		{"other protocols first", []string{"graphql-ws, klaro-bearer, tok"}, "tok", true},
		{"marker last", []string{"klaro-bearer"}, "", false},
		{"marker absent", []string{"graphql-ws, tok"}, "", false},
		{"nothing sent", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SubprotocolToken(requestWithProtocols(tc.lines...).Request)
			if got != tc.want || ok != tc.ok {
				t.Errorf("SubprotocolToken = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The widening is per route. A request that never opted in must not be
// authenticated from the handshake header, however well formed it is.
func TestBearerTokenIgnoresTheSubprotocolWithoutOptIn(t *testing.T) {
	c := requestWithProtocols("klaro-bearer, tok")
	if got, ok := BearerToken(c); ok {
		t.Fatalf("BearerToken = (%q, true) without the opt-in", got)
	}

	AllowWSSubprotocolCredential(c)
	got, ok := BearerToken(c)
	if !ok || got != "tok" {
		t.Errorf("after opt-in BearerToken = (%q, %v), want (\"tok\", true)", got, ok)
	}
}

// The header is the primary source everywhere, so it must win even on the one
// route that also reads the handshake.
func TestAuthorizationHeaderWinsOverTheSubprotocol(t *testing.T) {
	c := requestWithProtocols("klaro-bearer, from-subprotocol")
	c.Request.Header.Set("Authorization", "Bearer from-header")
	AllowWSSubprotocolCredential(c)

	got, ok := BearerToken(c)
	if !ok || got != "from-header" {
		t.Errorf("BearerToken = (%q, %v), want (\"from-header\", true)", got, ok)
	}
}

// An empty Authorization: Bearer is a broken client, not a reason to go looking
// for a credential elsewhere - otherwise a stale header would silently fall
// through to whatever the handshake carried.
func TestEmptyBearerHeaderDoesNotFallThrough(t *testing.T) {
	c := requestWithProtocols("klaro-bearer, from-subprotocol")
	c.Request.Header.Set("Authorization", "Bearer ")
	AllowWSSubprotocolCredential(c)

	if got, ok := BearerToken(c); ok {
		t.Errorf("BearerToken = (%q, true), want no credential", got)
	}
}

func TestOffersBearerSubprotocol(t *testing.T) {
	if !OffersBearerSubprotocol(requestWithProtocols("graphql-ws, klaro-bearer, tok").Request) {
		t.Error("offer not detected")
	}
	if OffersBearerSubprotocol(requestWithProtocols("graphql-ws").Request) {
		t.Error("offer detected where there was none")
	}
	if OffersBearerSubprotocol(requestWithProtocols().Request) {
		t.Error("offer detected with no header at all")
	}
}
