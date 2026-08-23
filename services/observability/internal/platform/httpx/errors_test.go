package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func run(h gin.HandlerFunc) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	h(c)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) Body {
	t.Helper()
	var b Body
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	return b
}

func TestHelpersMapToStatusAndCode(t *testing.T) {
	tests := []struct {
		name       string
		h          gin.HandlerFunc
		wantStatus int
		wantCode   string
	}{
		{"unauthenticated", func(c *gin.Context) { Unauthenticated(c, "no token") }, 401, CodeUnauthenticated},
		{"forbidden", func(c *gin.Context) { Forbidden(c, "other org") }, 403, CodeForbidden},
		{"not found", func(c *gin.Context) { NotFound(c, "gone") }, 404, CodeNotFound},
		{"conflict", func(c *gin.Context) { Conflict(c, "dup", nil) }, 409, CodeConflict},
		{"validation", func(c *gin.Context) { Validation(c, "bad", nil) }, 422, CodeValidation},
		{"quota", func(c *gin.Context) { QuotaExceeded(c, "over", nil) }, 429, CodeQuotaExceeded},
		{"internal", func(c *gin.Context) { Internal(c, "") }, 500, CodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := run(tc.h)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if got := decode(t, w).Error.Code; got != tc.wantCode {
				t.Errorf("code = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

func TestDetailsOmittedWhenNil(t *testing.T) {
	w := run(func(c *gin.Context) { Validation(c, "bad", nil) })
	var raw map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["error"]["details"]; ok {
		t.Error("details should be omitted when nil")
	}
}

func TestDetailsRoundTrip(t *testing.T) {
	w := run(func(c *gin.Context) {
		Validation(c, "bad", map[string]string{"field": "threshold"})
	})
	d, ok := decode(t, w).Error.Details.(map[string]any)
	if !ok {
		t.Fatalf("details type = %T", decode(t, w).Error.Details)
	}
	if d["field"] != "threshold" {
		t.Errorf("details = %v", d)
	}
}

func TestWriteAborts(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	Forbidden(c, "nope")
	if !c.IsAborted() {
		t.Error("error helpers must abort the handler chain")
	}
}
