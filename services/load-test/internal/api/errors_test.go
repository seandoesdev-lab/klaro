package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWriteError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeError(c, 403, "DOMAIN_NOT_VERIFIED", "nope", nil)
	if w.Code != 403 {
		t.Fatalf("code=%d", w.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error.Code != "DOMAIN_NOT_VERIFIED" {
		t.Fatalf("code=%q", body.Error.Code)
	}
}
