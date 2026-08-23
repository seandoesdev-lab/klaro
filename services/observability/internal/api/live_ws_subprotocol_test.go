package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klaro/observability/internal/live"
	"github.com/klaro/observability/internal/tenancy"
)

// dialLiveSubprotocol dials with the credential in the subprotocol list, the way
// a browser has to send it. The Authorization header is deliberately absent.
func dialLiveSubprotocol(t *testing.T, srv *httptest.Server, org string, protocols []string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: protocols}
	conn, resp, err := dialer.Dial(wsURL(srv, "/orgs/"+org+"/obs/live?stream=metric"), nil)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return conn, resp, err
}

// The whole point of the subprotocol path: a client that cannot set headers
// still authenticates, and still receives its org's frames.
func TestLiveAcceptsTheCredentialInTheSubprotocol(t *testing.T) {
	srv, signal := liveServer(t)

	conn, resp, err := dialLiveSubprotocol(t, srv, orgA,
		[]string{tenancy.BearerSubprotocol, "dev"})
	if err != nil {
		t.Fatalf("dial: %v (resp %v)", err, resp)
	}

	// The echo is not cosmetic: a browser fails a connection whose handshake
	// offered a subprotocol and came back without one, so a missing echo would
	// authenticate and then disconnect.
	if got := conn.Subprotocol(); got != tenancy.BearerSubprotocol {
		t.Errorf("negotiated subprotocol = %q, want %q", got, tenancy.BearerSubprotocol)
	}
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != tenancy.BearerSubprotocol {
		t.Errorf("handshake response echoed %q, want %q", got, tenancy.BearerSubprotocol)
	}

	frame := live.Frame{TS: 1756000000000, Stream: live.StreamMetric,
		Points: []live.Point{{Labels: map[string]string{"__name__": "cpu"}, Value: 0.5}}}
	if err := live.Publish(t.Context(), signal, orgA, frame); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got live.Frame
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 1 || got.Points[0].Value != 0.5 {
		t.Errorf("frame = %+v", got)
	}
}

// A wrong token in the subprotocol is not a socket. The subprotocol widens where
// the credential may come from, never whether one is checked.
func TestLiveRejectsABadSubprotocolToken(t *testing.T) {
	srv, _ := liveServer(t)

	_, resp, err := dialLiveSubprotocol(t, srv, orgA,
		[]string{tenancy.BearerSubprotocol, "not-the-dev-token"})
	if err == nil {
		t.Fatal("a handshake with a bad subprotocol token was upgraded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// The marker with nothing after it is a client bug, and must not be read as
// "authenticated with the empty string".
func TestLiveRejectsTheMarkerWithoutAToken(t *testing.T) {
	srv, _ := liveServer(t)

	_, resp, err := dialLiveSubprotocol(t, srv, orgA, []string{tenancy.BearerSubprotocol})
	if err == nil {
		t.Fatal("a handshake carrying only the marker was upgraded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// Cross-tenant over the subprotocol path answers the same way the header path
// does: a socket, then 4403. The transport of the credential must not change the
// authorization decision or how it is reported.
func TestLiveSubprotocolCrossTenantClosesWith4403(t *testing.T) {
	srv, _ := liveServer(t)

	conn, _, err := dialLiveSubprotocol(t, srv, orgB,
		[]string{tenancy.BearerSubprotocol, "dev"})
	if err != nil {
		t.Fatalf("dial: %v (the connection must be accepted so the close code can be read)", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, readErr := conn.ReadMessage()
	if !websocket.IsCloseError(readErr, closeForbidden) {
		t.Fatalf("close error = %v, want code %d", readErr, closeForbidden)
	}
}

// The Authorization header still wins, so a non-browser client is unaffected by
// any of the above.
func TestLiveStillPrefersTheAuthorizationHeader(t *testing.T) {
	srv, _ := liveServer(t)

	dialer := websocket.Dialer{Subprotocols: []string{tenancy.BearerSubprotocol, "wrong"}}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer dev")
	conn, resp, err := dialer.Dial(wsURL(srv, "/orgs/"+orgA+"/obs/live?stream=metric"), hdr)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	if err != nil {
		t.Fatalf("dial: %v (resp %v)", err, resp)
	}
}
