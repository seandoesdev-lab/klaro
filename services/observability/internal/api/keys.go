package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/ingestkey"
	"github.com/klaro/observability/internal/platform/audit"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/tenancy"
)

// issueKeyRequest is the POST /orgs/:orgId/obs/keys body.
type issueKeyRequest struct {
	Name       string                `json:"name"`
	ScopeLabel *ingestkey.ScopeLabel `json:"scope_label"`
}

// issuedKey is the one and only response that carries a plaintext secret.
//
// It is a distinct type from ingestkey.Key on purpose: the stored model has no
// secret field, so no list or read handler can ever accidentally serialise one.
type issuedKey struct {
	ingestkey.Key
	// Secret is shown exactly once. There is no endpoint that can return it
	// again, because only sha256(secret) was persisted.
	Secret string `json:"secret"`
	// GraceUntil, on a rotation response, is when the superseded key stops being
	// accepted (design section 4.2).
	GraceUntil *time.Time `json:"grace_until,omitempty"`
}

// writeKeyError maps the store's sentinels onto the shared error envelope.
func writeKeyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ingestkey.ErrInvalidName):
		httpx.Validation(c, err.Error(), nil)
	case errors.Is(err, ingestkey.ErrDuplicateName):
		httpx.Conflict(c, err.Error(), nil)
	case errors.Is(err, ingestkey.ErrNotFound):
		httpx.NotFound(c, "observability key not found")
	case errors.Is(err, ingestkey.ErrRevoked):
		httpx.Conflict(c, "observability key is revoked; issue a new one instead of rotating", nil)
	default:
		// Never surface the driver error: it can quote SQL and column values.
		httpx.Internal(c, "observability key operation failed")
	}
}

// postKey issues a key for the caller org [OBS-02].
func (d Deps) postKey(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	var req issueKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Validation(c, "request body must be a JSON object", gin.H{"parse": err.Error()})
		return
	}

	key, secret, err := d.Keys.Issue(c.Request.Context(), orgID, req.Name, req.ScopeLabel,
		ingestkey.AuditHook(d.Audit, audit.ActionKeyIssue, nil))
	if err != nil {
		writeKeyError(c, err)
		return
	}
	c.JSON(http.StatusCreated, issuedKey{Key: key, Secret: secret})
}

// listKeys returns the org's keys. Secrets are structurally absent.
func (d Deps) listKeys(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	keys, err := d.Keys.List(c.Request.Context(), orgID)
	if err != nil {
		writeKeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": keys})
}

// rotateKey issues a replacement and starts the old key's grace clock [OBS-02].
func (d Deps) rotateKey(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	fresh, secret, graceUntil, err := d.Keys.Rotate(c.Request.Context(), orgID, c.Param("keyId"),
		d.RotationGrace, ingestkey.AuditHook(d.Audit, audit.ActionKeyRotate, nil))
	if err != nil {
		writeKeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, issuedKey{Key: fresh, Secret: secret, GraceUntil: &graceUntil})
}

// deleteKey revokes immediately - no grace window [OBS-02].
func (d Deps) deleteKey(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	if _, err := d.Keys.Revoke(c.Request.Context(), orgID, c.Param("keyId"),
		ingestkey.AuditHook(d.Audit, audit.ActionKeyRevoke, nil)); err != nil {
		writeKeyError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// getQuota reports consumption against the plan [OBS-02].
//
// This always answers 200, including when the org is over its limits. Crossing
// a limit bills as overage rather than blocking ingest (design section 7-1), so
// there is no state in which this endpoint is the bearer of a 429.
func (d Deps) getQuota(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	q, err := d.Keys.Snapshot(c.Request.Context(), orgID, d.ActiveHostWindow)
	if err != nil {
		httpx.Internal(c, "read observability quota")
		return
	}
	c.JSON(http.StatusOK, q)
}
