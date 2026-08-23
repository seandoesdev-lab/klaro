package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klaro/observability/internal/live"
	"github.com/klaro/observability/internal/platform/redisx"
)

// liveServer starts the router over a real listener, because a WebSocket needs
// a hijackable connection that httptest.ResponseRecorder cannot provide.
func liveServer(t *testing.T) (*httptest.Server, redisx.Signaler) {
	t.Helper()
	signal := redisx.NewMemory()
	d := deps()
	d.Signal = signal
	d.Live = live.NewHub(signal)
	srv := httptest.NewServer(NewRouter(d))
	t.Cleanup(srv.Close)
	return srv, signal
}

func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path
}

func dialLive(t *testing.T, srv *httptest.Server, org, query, auth string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	hdr := http.Header{}
	if auth != "" {
		hdr.Set("Authorization", auth)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(srv, "/orgs/"+org+"/obs/live"+query), hdr)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return conn, resp, err
}

func TestLiveStreamsFramesForTheCallerOrg(t *testing.T) {
	srv, signal := liveServer(t)

	conn, _, err := dialLive(t, srv, orgA, "?stream=metric", "Bearer dev")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// The hub subscribes before the upgrade completes, so by the time Dial
	// returns the subscription exists and nothing published now can be missed.
	frame := live.Frame{TS: 1756000000000, Stream: live.StreamMetric,
		Points: []live.Point{{Labels: map[string]string{"__name__": "cpu"}, Value: 0.75}}}
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
	if got.Stream != live.StreamMetric || len(got.Points) != 1 || got.Points[0].Value != 0.75 {
		t.Errorf("frame = %+v", got)
	}
}

// Authenticated for orgA, addressing orgB. The client is real, so it gets a
// socket just long enough to be told exactly what is wrong - a rejected
// handshake would reach the browser as an indistinguishable 1006.
func TestLiveClosesCrossTenantWith4403(t *testing.T) {
	srv, _ := liveServer(t)

	conn, _, err := dialLive(t, srv, orgB, "?stream=metric", "Bearer dev")
	if err != nil {
		t.Fatalf("dial: %v (the connection must be accepted so the close code can be read)", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, readErr := conn.ReadMessage()
	if !websocket.IsCloseError(readErr, closeForbidden) {
		t.Fatalf("close error = %v, want code %d", readErr, closeForbidden)
	}
}

// An anonymous caller gets no socket at all: there is nothing to tell it that
// it is entitled to hear.
func TestLiveRefusesTheHandshakeWithoutACredential(t *testing.T) {
	srv, _ := liveServer(t)

	_, resp, err := dialLive(t, srv, orgA, "?stream=metric", "")
	if err == nil {
		t.Fatal("an unauthenticated handshake was upgraded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

func TestLiveRejectsUnknownStream(t *testing.T) {
	srv, _ := liveServer(t)

	conn, _, err := dialLive(t, srv, orgA, "?stream=bogus", "Bearer dev")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, readErr := conn.ReadMessage()
	if !websocket.IsCloseError(readErr, closeBadRequest) {
		t.Fatalf("close error = %v, want code %d", readErr, closeBadRequest)
	}
}

// Omitting the parameter is the common case for a metrics dashboard.
func TestLiveDefaultsToTheMetricStream(t *testing.T) {
	srv, signal := liveServer(t)

	conn, _, err := dialLive(t, srv, orgA, "", "Bearer dev")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := live.Publish(t.Context(), signal, orgA,
		live.Frame{TS: 1, Stream: live.StreamMetric, Points: []live.Point{{Value: 2}}}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read: %v", err)
	}
}

// A watcher on one org must never receive another org's frames, even though
// both sockets are served by the same process.
func TestLiveDoesNotLeakAcrossOrgs(t *testing.T) {
	srv, signal := liveServer(t)

	conn, _, err := dialLive(t, srv, orgA, "?stream=metric", "Bearer dev")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// orgB has no watcher here; publishing to it must not reach orgA's socket.
	if err := live.Publish(t.Context(), signal, orgB,
		live.Frame{TS: 1, Stream: live.StreamMetric,
			Points: []live.Point{{Labels: map[string]string{"tenant": "b"}, Value: 9}}}); err != nil {
		t.Fatal(err)
	}
	if err := live.Publish(t.Context(), signal, orgA,
		live.Frame{TS: 2, Stream: live.StreamMetric,
			Points: []live.Point{{Labels: map[string]string{"tenant": "a"}, Value: 1}}}); err != nil {
		t.Fatal(err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The first frame to arrive must be orgA's: orgB's was published first, so
	// receiving it would mean the channels are not isolated.
	if strings.Contains(string(raw), `"tenant":"b"`) {
		t.Fatalf("orgA socket received orgB data: %s", raw)
	}
}
