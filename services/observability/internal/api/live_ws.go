package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/klaro/observability/internal/live"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/tenancy"
)

// WebSocket close codes in the private range (design section 4.3).
const (
	// closeForbidden is what an authenticated client addressing another org
	// gets. It exists because the browser WebSocket API does not expose the
	// handshake status: a client rejected before upgrade sees only a generic
	// 1006, and cannot tell "wrong org" from "network died". An application
	// close code is the only way to tell it something useful.
	closeForbidden = 4403
	// closeBadRequest covers an unusable stream parameter.
	closeBadRequest = 4400
)

const (
	// writeWait bounds a single frame write, so one wedged TCP connection
	// cannot pin a goroutine forever.
	writeWait = 10 * time.Second
	// pongWait is how long a client may go silent before it is considered gone.
	pongWait = 60 * time.Second
	// pingPeriod must be shorter than pongWait, with room for a round trip.
	pingPeriod = 25 * time.Second
	// maxClientMessage bounds anything a client sends. It is not supposed to
	// send anything at all; the reader exists only to notice it disappearing.
	maxClientMessage = 512
)

var upgrader = websocket.Upgrader{
	// Dev: allow all origins, matching the S1 stub. Real origin policy lands
	// with real authentication - a cookie-authenticated WebSocket without an
	// origin check would be cross-site forgeable, but the current credential
	// is a bearer token a browser will not attach on its own.
	CheckOrigin: func(*http.Request) bool { return true },
}

// getLive streams the org's live telemetry [OBS-01/APM-02].
//
// Authorization runs before the upgrade, but the two failure modes are
// reported differently:
//
//   - no usable credential: plain 401, no socket. An anonymous caller does not
//     get a connection just to be told about it.
//   - authenticated, wrong org: upgrade, then close 4403. The caller is a real
//     client with a real credential, and telling it exactly what is wrong costs
//     nothing and saves a support ticket.
//
// This route is mounted outside the org group precisely so it can make that
// distinction; it calls the same tenancy.Resolve the middleware uses, so the
// two cannot drift apart.
func (d Deps) getLive(c *gin.Context) {
	orgID, outcome := tenancy.Resolve(c, d.Auth)
	switch outcome {
	case tenancy.OutcomeOK:
	case tenancy.OutcomeUnauthenticated:
		httpx.Unauthenticated(c, "missing or invalid credentials")
		return
	case tenancy.OutcomeInvalidOrgParam:
		httpx.Validation(c, "orgId must be a uuid", gin.H{"orgId": c.Param("orgId")})
		return
	case tenancy.OutcomeForbidden:
		closeWith(c, closeForbidden, "org scope mismatch")
		return
	default:
		httpx.Internal(c, "authenticated org is not a valid uuid")
		return
	}

	stream := c.DefaultQuery("stream", live.StreamMetric)
	if !live.ValidStream(stream) {
		closeWith(c, closeBadRequest, "unknown stream")
		return
	}

	ctx := c.Request.Context()
	frames, unsubscribe, err := d.Live.Subscribe(ctx, orgID, stream)
	if err != nil {
		httpx.Internal(c, "subscribe to live stream")
		return
	}
	defer unsubscribe()

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return // Upgrade already wrote its own error
	}
	defer func() { _ = conn.Close() }()

	pump(ctx, conn, frames)
}

// closeWith upgrades only to deliver a close code. The socket carries no data
// and is shut immediately; the upgrade exists purely so the client can read the
// reason, which a rejected handshake would not let it do.
func closeWith(c *gin.Context, code int, reason string) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Not a WebSocket request after all - answer as the REST surface would.
		httpx.Forbidden(c, reason)
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(writeWait))
}

// pump forwards frames until the client leaves, the stream ends, or the request
// context is cancelled.
func pump(ctx context.Context, conn *websocket.Conn, frames <-chan []byte) {
	closed := make(chan struct{})
	go readUntilClosed(conn, closed)

	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-closed:
			return
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case msg, ok := <-frames:
			if !ok {
				return // the hub tore the subscription down
			}
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}
}

// readUntilClosed drains the client side. Nothing is expected to arrive; the
// read is how a disappearing client is noticed, and it is also what lets the
// pong deadline advance.
func readUntilClosed(conn *websocket.Conn, closed chan<- struct{}) {
	defer close(closed)
	conn.SetReadLimit(maxClientMessage)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}
