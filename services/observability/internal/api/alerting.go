package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/observability/internal/alerting"
	"github.com/klaro/observability/internal/platform/audit"
	"github.com/klaro/observability/internal/platform/httpx"
	"github.com/klaro/observability/internal/tenancy"
)

// writeRuleError maps the alerting sentinels onto the shared envelope.
func writeRuleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, alerting.ErrUnsupportedSignal), errors.Is(err, alerting.ErrInvalidRule):
		httpx.Validation(c, err.Error(), nil)
	case errors.Is(err, alerting.ErrDuplicateName):
		httpx.Conflict(c, err.Error(), nil)
	case errors.Is(err, alerting.ErrNotFound):
		httpx.NotFound(c, "alert rule not found")
	default:
		httpx.Internal(c, "alert rule operation failed")
	}
}

// postAlertRule creates a rule [OBS-06].
func (d Deps) postAlertRule(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	var in alerting.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Validation(c, "request body must be a JSON object", gin.H{"parse": err.Error()})
		return
	}
	rule, err := d.Rules.Create(c.Request.Context(), orgID, in,
		alerting.AuditHook(d.Audit, audit.ActionRuleCreate, nil))
	if err != nil {
		writeRuleError(c, err)
		return
	}
	// Push the change to vmalert now rather than waiting for the next periodic
	// sync: a rule the user just created should be live, not eventually live.
	d.syncRules(c)
	c.JSON(http.StatusCreated, rule)
}

// listAlertRules returns the org's rules [OBS-06].
func (d Deps) listAlertRules(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	rules, err := d.Rules.List(c.Request.Context(), orgID)
	if err != nil {
		writeRuleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rules})
}

// getAlertRule reads one rule [OBS-06].
func (d Deps) getAlertRule(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	rule, err := d.Rules.Get(c.Request.Context(), orgID, c.Param("ruleId"))
	if err != nil {
		writeRuleError(c, err)
		return
	}
	c.JSON(http.StatusOK, rule)
}

// patchAlertRule applies a partial update [OBS-06].
func (d Deps) patchAlertRule(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	var in alerting.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Validation(c, "request body must be a JSON object", gin.H{"parse": err.Error()})
		return
	}
	rule, err := d.Rules.Update(c.Request.Context(), orgID, c.Param("ruleId"), in,
		alerting.AuditHook(d.Audit, audit.ActionRuleUpdate, nil))
	if err != nil {
		writeRuleError(c, err)
		return
	}
	d.syncRules(c)
	c.JSON(http.StatusOK, rule)
}

// deleteAlertRule removes a rule and its history [OBS-06].
func (d Deps) deleteAlertRule(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	if err := d.Rules.Remove(c.Request.Context(), orgID, c.Param("ruleId"),
		alerting.AuditHook(d.Audit, audit.ActionRuleDelete, nil)); err != nil {
		writeRuleError(c, err)
		return
	}
	// Syncing after a delete matters more than after a create: a rule left in
	// the file keeps firing for something the customer already turned off.
	d.syncRules(c)
	c.Status(http.StatusNoContent)
}

// listAlertEvents returns the org's alert history [OBS-07].
func (d Deps) listAlertEvents(c *gin.Context) {
	orgID, ok := tenancy.FromGin(c)
	if !ok {
		httpx.Internal(c, "org not resolved")
		return
	}
	filter := alerting.EventFilter{State: c.Query("state"), RuleID: c.Query("rule_id")}
	if raw := c.Query("from"); raw != "" {
		t, ok := parseTime(raw)
		if !ok {
			httpx.Validation(c, "from must be RFC3339 or a unix timestamp", nil)
			return
		}
		filter.From = t
	}
	if raw := c.Query("to"); raw != "" {
		t, ok := parseTime(raw)
		if !ok {
			httpx.Validation(c, "to must be RFC3339 or a unix timestamp", nil)
			return
		}
		filter.To = t
	}
	if n, ok := intQuery(c, "limit"); ok {
		filter.Limit = n
	} else {
		httpx.Validation(c, "limit must be a non-negative integer", nil)
		return
	}

	events, err := d.Rules.ListEvents(c.Request.Context(), orgID, filter)
	if err != nil {
		writeRuleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": events})
}

// syncRules re-renders the vmalert file after a change.
//
// Failure is logged inside the syncer and not surfaced: the rule is already
// stored, and the periodic sync will pick it up. Failing the request would tell
// the user their rule was not created when it was.
func (d Deps) syncRules(c *gin.Context) {
	if d.RuleSync == nil || !d.RuleSync.Enabled() {
		return
	}
	_ = d.RuleSync.Sync(c.Request.Context())
}
