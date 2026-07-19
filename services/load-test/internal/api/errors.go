package api

import "github.com/gin-gonic/gin"

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
