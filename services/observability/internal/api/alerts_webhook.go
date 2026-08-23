package api

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/alerting"
	"github.com/klaro/observability/internal/platform/httpx"
)

// postAlertsWebhook receives firing and resolved alerts [OBS-07, design 4.4].
//
// Two payload shapes are accepted because two senders exist. vmalert posts the
// Alertmanager v2 API - a bare JSON array - which is why the route is also
// mounted at /internal/alerts/api/v2/alerts: pointing vmalert at the control
// plane directly saves running an Alertmanager whose only job would be to
// forward the same data. An actual Alertmanager webhook wraps the array in an
// object, so that form is accepted too.
func (d InternalDeps) postAlertsWebhook(c *gin.Context) {
	body, err := readInternalBody(c)
	if err != nil {
		httpx.Validation(c, "unreadable body", gin.H{"read": err.Error()})
		return
	}
	alerts, err := parseAlerts(body)
	if err != nil {
		httpx.Validation(c, err.Error(), nil)
		return
	}
	if d.Alerts == nil {
		httpx.Internal(c, "alert receiver not configured")
		return
	}

	result, err := d.Alerts.Handle(c.Request.Context(), alerts)
	if err != nil {
		httpx.Internal(c, "record alerts")
		return
	}
	c.JSON(http.StatusOK, result)
}

// parseAlerts accepts either a bare array of alerts or an Alertmanager webhook
// envelope containing one.
func parseAlerts(body []byte) ([]alerting.Alert, error) {
	if len(body) == 0 {
		return nil, errJSONObject
	}
	var direct []alerting.Alert
	if err := json.Unmarshal(body, &direct); err == nil {
		return direct, nil
	}
	var wrapped struct {
		Alerts []alerting.Alert `json:"alerts"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, errJSONObject
	}
	return wrapped.Alerts, nil
}
