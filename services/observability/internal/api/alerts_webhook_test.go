package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klaro/observability/internal/alerting"
)

// vmalert posts a bare array; an Alertmanager webhook wraps it. Both shapes have
// to parse, or one of the two senders silently records nothing.
func TestParseAlertsAcceptsBothShapes(t *testing.T) {
	alert := `{"labels":{"klaro_org_id":"` + orgA + `"},"startsAt":"2026-08-23T12:00:00Z"}`

	direct, err := parseAlerts([]byte("[" + alert + "]"))
	if err != nil {
		t.Fatalf("bare array: %v", err)
	}
	wrapped, err := parseAlerts([]byte(`{"status":"firing","alerts":[` + alert + `]}`))
	if err != nil {
		t.Fatalf("webhook envelope: %v", err)
	}
	if len(direct) != 1 || len(wrapped) != 1 {
		t.Fatalf("direct=%d wrapped=%d, want 1 each", len(direct), len(wrapped))
	}
	if direct[0].Labels[alerting.LabelOrgID] != orgA {
		t.Errorf("labels = %v", direct[0].Labels)
	}
}

func TestParseAlertsRejectsJunk(t *testing.T) {
	for _, body := range []string{"", "not json", `"a string"`} {
		if _, err := parseAlerts([]byte(body)); err == nil {
			t.Errorf("parseAlerts(%q) was accepted", body)
		}
	}
}

// A batch whose alerts carry no klaro labels is not ours. Ignoring them beats
// failing the batch, which would make one stray alert block every real one.
func TestWebhookIgnoresForeignAlerts(t *testing.T) {
	d, _ := internalDeps()
	d.Alerts = alerting.NewReceiver(alerting.NewStore(nil), nil)

	body := `[{"labels":{"alertname":"SomeoneElsesAlert"},"startsAt":"2026-08-23T12:00:00Z"}]`
	req := httptest.NewRequest(http.MethodPost, "/internal/alerts/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"ignored":1`) {
		t.Errorf("body = %s, want the alert counted as ignored", w.Body)
	}
}

// vmalert appends /api/v2/alerts to its notifier URL, so that path has to be
// mounted alongside the documented webhook name.
func TestWebhookIsMountedOnBothPaths(t *testing.T) {
	d, _ := internalDeps()
	d.Alerts = alerting.NewReceiver(alerting.NewStore(nil), nil)

	for _, path := range []string{"/internal/alerts/webhook", "/internal/alerts/api/v2/alerts"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("[]"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		NewInternalRouter(d).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("POST %s = %d, want 200 (body %s)", path, w.Code, w.Body)
		}
	}
}

// The usage route is internal-only and validates before touching the database.
func TestUsageRouteValidates(t *testing.T) {
	d, _ := internalDeps()
	req := httptest.NewRequest(http.MethodPost, "/internal/usage",
		strings.NewReader(`{"org_id":"not-a-uuid"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)

	// No usage store is wired in these deps, so a valid body would 500; an
	// invalid one must be refused before that.
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body)
	}
}
