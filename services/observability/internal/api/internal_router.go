package api

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/alerting"
	"github.com/klaro/observability/internal/ingestkey"
	"github.com/klaro/observability/internal/live"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/platform/redisx"
	"github.com/klaro/observability/internal/snapshot"
	"github.com/klaro/observability/internal/usage"
)

// IngestKeyHeader is the header the SDK sets and the Collector forwards.
const IngestKeyHeader = "klaro-obs-key"

// maxInternalBody caps an internal request. Live metric batches are small
// (a Collector flush every couple of seconds); anything larger is a bug or an
// attempt to exhaust memory.
const maxInternalBody = 1 << 20 // 1 MiB

// InternalDeps are the collaborators of the internal plane.
type InternalDeps struct {
	Authz     *ingestkey.Authorizer
	Alerts    *alerting.Receiver
	Usage     *usage.Store
	Snapshots *snapshot.Adapter
	Signal    redisx.Signaler

	// Token is the shared secret the Collector and vmalert present as
	// `Authorization: Bearer`. It is what keeps the internal plane
	// authenticated when transport security is relaxed for local development -
	// see InternalAuth.
	Token string
}

// NewInternalRouter builds the engine served on the internal listener.
//
// Only the Collector and vmalert call these routes. They live on a separate
// listener from the public API, so a request arriving on the public port can
// never reach them, and every route under /internal is authenticated by
// InternalAuth.
func NewInternalRouter(d InternalDeps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// Liveness stays open: it is a container probe that reads nothing and
	// returns nothing about the tenant.
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	in := r.Group("/internal", InternalAuth(d.Token))
	{
		in.POST("/authz/ingest-key", d.postAuthzIngestKey)
		in.POST("/live-ingest", d.postLiveIngest)

		// Alerts. The api/v2/alerts path is what vmalert posts to; the webhook
		// path is the documented name and accepts the Alertmanager envelope.
		in.POST("/alerts/webhook", d.postAlertsWebhook)
		in.POST("/alerts/api/v2/alerts", d.postAlertsWebhook)

		// Volume and host metering from the gateway [BILL-03].
		in.POST("/usage", d.postUsage)

		// A report service asking for one window of telemetry [OBS-10].
		in.POST("/snapshot", d.postSnapshot)
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

// postLiveIngest publishes a replicated metric batch onto the org's live
// channel [OBS-01/APM-02, design HOW-7].
//
// This is the ingest half of the live path. The WebSocket fan-out that consumes
// the channel is build-order step 5; publishing already works, so a subscriber
// can be attached without touching the Collector again.
// readInternalBody reads a request body, transparently decompressing gzip.
//
// The Collector compresses OTLP/HTTP exports by default, so a handler that read
// raw bytes would see gzip framing and reject every real batch while passing
// every hand-written curl. The limit is applied to both the compressed and the
// decompressed stream, so a small compressed payload cannot expand into an
// unbounded allocation.
func readInternalBody(c *gin.Context) ([]byte, error) {
	var reader io.Reader = io.LimitReader(c.Request.Body, maxInternalBody)
	if strings.EqualFold(c.GetHeader("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(reader)
		if err != nil {
			return nil, fmt.Errorf("gzip body: %w", err)
		}
		defer func() { _ = zr.Close() }()
		reader = io.LimitReader(zr, maxInternalBody)
	}
	return io.ReadAll(reader)
}

func (d InternalDeps) postLiveIngest(c *gin.Context) {
	body, err := readInternalBody(c)
	if err != nil {
		httpx.Validation(c, "unreadable body", gin.H{"read": err.Error()})
		return
	}

	frames, err := parseLiveFrames(body, time.Now().UnixMilli())
	if err != nil {
		httpx.Validation(c, err.Error(), nil)
		return
	}
	for _, f := range frames {
		if err := live.Publish(c.Request.Context(), d.Signal, f.OrgID, f.Frame); err != nil {
			httpx.Internal(c, "publish live frame")
			return
		}
	}
	// Accepted, not OK: pub/sub is fire-and-forget by design (HOW-7), so a 200
	// would imply a delivery guarantee that does not exist.
	c.Status(http.StatusAccepted)
}

// parseLiveBody accepts either shape the endpoint has to handle.
//
// The Collector exports OTLP/JSON, because that is what an otlphttp exporter
// emits and adding a bespoke exporter to the gateway to reshape it would be a
// component to maintain for no benefit. The flat {org_id, stream, points} form
// stays supported because it is what tests and curl send, and what a future
// non-OTLP producer would use.

// errJSONObject is the single message for "this body is not what we can read".
// Echoing the decoder's error for a payload that may be an alert batch would
// put customer label values into an error string.
var errJSONObject = errors.New("body must be a JSON array of alerts or an object containing one")
