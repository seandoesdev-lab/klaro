package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/usage"
)

// postUsage records a metering report from the gateway [BILL-03].
//
// The gateway reports rather than the control plane counting, because the
// gateway is the only component every signal passes through: metrics also reach
// the control plane as a live replica, but traces and logs go straight to
// storage, so counting here would silently meter one signal out of three.
//
// The org comes from the report body, which is trusted for exactly one reason:
// this route lives on the internal plane, where the caller authenticated with a
// client certificate. Nothing on the public plane can reach it.
func (d InternalDeps) postUsage(c *gin.Context) {
	body, err := readInternalBody(c)
	if err != nil {
		httpx.Validation(c, "unreadable body", gin.H{"read": err.Error()})
		return
	}
	var report usage.Report
	if err := json.Unmarshal(body, &report); err != nil {
		httpx.Validation(c, "body must be a usage report object", nil)
		return
	}
	if d.Usage == nil {
		httpx.Internal(c, "usage metering not configured")
		return
	}

	if err := d.Usage.Record(c.Request.Context(), report); err != nil {
		if errors.Is(err, usage.ErrInvalidReport) {
			httpx.Validation(c, err.Error(), nil)
			return
		}
		httpx.Internal(c, "record usage")
		return
	}
	// Accepted: the numbers are stored, but the billing event they feed is
	// published by the emitter on its own schedule.
	c.Status(http.StatusAccepted)
}
