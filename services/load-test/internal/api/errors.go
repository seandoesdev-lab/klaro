package api

import (
	"log"

	"github.com/gin-gonic/gin"
)

type errBody struct {
	Error errPayload `json:"error"`
}
type errPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func writeError(c *gin.Context, status int, code, msg string, details any) {
	c.AbortWithStatusJSON(status, errBody{Error: errPayload{Code: code, Message: msg, Details: details}})
}

// writeInternal logs the real error server-side and returns a constant message
// so DB schema/constraint names never leak to clients (F-3, 정보 누출 차단).
func writeInternal(c *gin.Context, err error) {
	log.Printf("internal error: %s %s: %v", c.Request.Method, c.Request.URL.Path, err)
	writeError(c, 500, "INTERNAL", "internal error", nil)
}
