// Package httpx carries the error envelope shared with S1
// (services/load-test/internal/api/errors.go): every failure serialises as
//
//	{"error": {"code": "...", "message": "...", "details": ...}}
//
// so a single frontend error handler covers both control planes.
package httpx

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Error codes from the design (services/observability design 4.2).
const (
	CodeUnauthenticated = "UNAUTHENTICATED"
	CodeForbidden       = "FORBIDDEN"
	CodeNotFound        = "NOT_FOUND"
	CodeConflict        = "CONFLICT"
	CodeValidation      = "VALIDATION_ERROR"
	CodeQuotaExceeded   = "QUOTA_EXCEEDED"
	CodeInternal        = "INTERNAL_ERROR"
)

// Body is the outer envelope.
type Body struct {
	Error Payload `json:"error"`
}

// Payload is the error itself.
type Payload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// Write aborts the request with the shared error envelope.
func Write(c *gin.Context, status int, code, message string, details any) {
	c.AbortWithStatusJSON(status, Body{Error: Payload{Code: code, Message: message, Details: details}})
}

// Unauthenticated: no credential, or one that did not resolve to an org.
func Unauthenticated(c *gin.Context, message string) {
	Write(c, http.StatusUnauthorized, CodeUnauthenticated, message, nil)
}

// Forbidden: authenticated, but not for this org/resource. This is the code a
// cross-tenant attempt gets before it ever reaches Postgres.
func Forbidden(c *gin.Context, message string) {
	Write(c, http.StatusForbidden, CodeForbidden, message, nil)
}

// NotFound: the resource does not exist within the caller org.
func NotFound(c *gin.Context, message string) {
	Write(c, http.StatusNotFound, CodeNotFound, message, nil)
}

// Conflict: uniqueness or state conflict (duplicate key name, rule name, ...).
func Conflict(c *gin.Context, message string, details any) {
	Write(c, http.StatusConflict, CodeConflict, message, details)
}

// Validation: syntactically parseable but semantically rejected input.
func Validation(c *gin.Context, message string, details any) {
	Write(c, http.StatusUnprocessableEntity, CodeValidation, message, details)
}

// QuotaExceeded reports a plan limit. Per design 7-1 the confirmed policy is
// overage billing rather than blocking, so this is used for advisory
// surfaces (quota endpoint, ingest response headers) - not to reject ingest.
func QuotaExceeded(c *gin.Context, message string, details any) {
	Write(c, http.StatusTooManyRequests, CodeQuotaExceeded, message, details)
}

// Internal hides the underlying error from the client; log it at the call site.
func Internal(c *gin.Context, message string) {
	if message == "" {
		message = "internal error"
	}
	Write(c, http.StatusInternalServerError, CodeInternal, message, nil)
}
