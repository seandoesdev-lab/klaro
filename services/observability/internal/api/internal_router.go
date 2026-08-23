package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/ingestkey"
	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/platform/redisx"
)

// IngestKeyHeader is the header the SDK sets and the Collector forwards.
const IngestKeyHeader = "klaro-obs-key"

// maxInternalBody caps an internal request. Live metric batches are small
// (a Collector flush every couple of seconds); anything larger is a bug or an
// attempt to exhaust memory.
const maxInternalBody = 1 << 20 // 1 MiB

// InternalDeps are the collaborators of the internal plane.
type InternalDeps struct {
	Authz  *ingestkey.Authorizer
	Signal redisx.Signaler
}

// NewInternalRouter builds the engine served on the internal listener.
//
// Only the Collector and vmalert call these routes, over mTLS (design section
// 4.4), which is why they carry no user credential. They live on a separate
// listener from the public API, so a request arriving on the public port can
// never reach them.
func NewInternalRouter(d InternalDeps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	in := r.Group("/internal")
	{
		in.POST("/authz/ingest-key", d.postAuthzIngestKey)
		in.POST("/live-ingest", d.postLiveIngest)
	}
	return r
}

// authzRequest is the Collector's question: is this secret good, and whose
// telemetry is it?
type authzRequest struct {
	Key string `json:"key"`
}

// postAuthzIngestKey resolves a presented secret to an org plus the backend
// tenant identities to route it with [OBS-02, design 4.1/4.4].
//
// The Collector caches the answer for Grant.CacheTTLSec, so this runs once per
// key per TTL rather than once per batch.
func (d InternalDeps) postAuthzIngestKey(c *gin.Context) {
	var req authzRequest
	// The header form exists so the Collector can forward what it received
	// verbatim; the body form is easier to call from tests and from curl.
	if err := c.ShouldBindJSON(&req); err != nil || req.Key == "" {
		req.Key = c.GetHeader(IngestKeyHeader)
	}
	if req.Key == "" {
		httpx.Validation(c, "key is required", nil)
		return
	}

	grant, err := d.Authz.Authorize(c.Request.Context(), req.Key)
	if errors.Is(err, ingestkey.ErrUnauthorized) {
		// One answer for unknown, revoked and grace-expired keys alike.
		httpx.Unauthenticated(c, "ingest key is not authorized")
		return
	}
	if err != nil {
		httpx.Internal(c, "authorize ingest key")
		return
	}
	c.JSON(http.StatusOK, grant)
}

// liveIngestRequest is the Collector's replicated metric sample.
//
// OrgID is set by the Collector from the grant it just verified, never from
// anything the SDK sent. The Collector is trusted here only because it
// authenticated with a client certificate on this listener.
type liveIngestRequest struct {
	OrgID  string          `json:"org_id"`
	Stream string          `json:"stream"`
	TS     json.Number     `json:"ts"`
	Points json.RawMessage `json:"points"`
}

// validStream keeps the stream name inside a character set that cannot escape
// its Redis channel. The channel is klaro:obs:live:<org>:<stream>, so a ':' in
// the stream would let a caller address a channel it was not granted.
func validStream(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// postLiveIngest publishes a replicated metric batch onto the org's live
// channel [OBS-01/APM-02, design HOW-7].
//
// This is the ingest half of the live path. The WebSocket fan-out that consumes
// the channel is build-order step 5; publishing already works, so a subscriber
// can be attached without touching the Collector again.
func (d InternalDeps) postLiveIngest(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxInternalBody))
	if err != nil {
		httpx.Validation(c, "unreadable body", nil)
		return
	}
	var req liveIngestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.Validation(c, "body must be a JSON object", gin.H{"parse": err.Error()})
		return
	}
	if !db.ValidOrgID(req.OrgID) {
		httpx.Validation(c, "org_id must be a uuid", nil)
		return
	}
	if !validStream(req.Stream) {
		httpx.Validation(c, "stream must be 1-32 chars of [a-z0-9_-]", gin.H{"stream": req.Stream})
		return
	}

	// Republish the payload as received. Reshaping it here would put a second
	// schema between the Collector and the WebSocket clients for no gain.
	if err := d.Signal.PublishLive(c.Request.Context(), req.OrgID, req.Stream, body); err != nil {
		httpx.Internal(c, "publish live batch")
		return
	}
	// Accepted, not OK: pub/sub is fire-and-forget by design (HOW-7), so a 200
	// would imply a delivery guarantee that does not exist.
	c.Status(http.StatusAccepted)
}
