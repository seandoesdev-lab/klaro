package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/snapshot"
)

// snapshotRequest is the internal-plane body: a snapshot request plus the org it
// is for.
//
// The org travels in the body because the caller is the report service, not a
// tenant - it is asking on behalf of an org rather than as one. That is only
// safe because this route lives on the internal plane, where the caller
// authenticated with a client certificate; nothing on the public plane can
// reach it.
type snapshotRequest struct {
	OrgID string `json:"org_id"`
	snapshot.Request
}

// postSnapshot serves a report's window of telemetry [OBS-10, design 4.6].
//
// It is exposed over HTTP rather than only as a Go package because the report
// service (S4) is a separate service in a separate module: a package it cannot
// import would be a design on paper.
func (d InternalDeps) postSnapshot(c *gin.Context) {
	body, err := readInternalBody(c)
	if err != nil {
		httpx.Validation(c, "unreadable body", gin.H{"read": err.Error()})
		return
	}
	var req snapshotRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.Validation(c, "body must be a snapshot request object", nil)
		return
	}
	if !db.ValidOrgID(req.OrgID) {
		httpx.Validation(c, "org_id must be a uuid", nil)
		return
	}
	if d.Snapshots == nil {
		httpx.Internal(c, "snapshot adapter not configured")
		return
	}

	out, err := d.Snapshots.Take(c.Request.Context(), req.OrgID, req.Request)
	if err != nil {
		if errors.Is(err, snapshot.ErrInvalidRequest) {
			httpx.Validation(c, err.Error(), nil)
			return
		}
		httpx.Internal(c, "take snapshot")
		return
	}
	c.JSON(http.StatusOK, out)
}
