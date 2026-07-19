package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/scenario"
	"github.com/klaro/load-test/internal/store"
)

func (d Deps) createLoadTest(c *gin.Context) {
	var req struct {
		TargetURL string         `json:"target_url"`
		Scenario  model.Scenario `json:"scenario"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}
	if err := scenario.Validate(req.Scenario); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	host, err := store.HostFromURL(req.TargetURL)
	if err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid target_url", nil)
		return
	}
	pid := projectID(c)
	verified, err := d.Store.IsDomainVerified(c, pid, host)
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	if !verified {
		writeError(c, 403, "DOMAIN_NOT_VERIFIED", "target domain must be verified", nil)
		return
	}
	lt := &model.LoadTest{
		ProjectID: pid, TargetURL: req.TargetURL, Scenario: req.Scenario,
		VU: req.Scenario.VU, DurationSec: req.Scenario.DurationSec, Status: model.StatusValidating,
	}
	if err := d.Store.CreateLoadTest(c, lt); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	if err := d.Store.UpdateStatus(c, lt.ID, model.StatusQueued, nil); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	job := model.Job{LoadTestID: lt.ID, ProjectID: pid, TargetURL: lt.TargetURL, Scenario: lt.Scenario}
	if err := d.Queue.Enqueue(c, job); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": lt.ID, "status": model.StatusValidating})
}

func (d Deps) getLoadTest(c *gin.Context) {
	lt, err := d.Store.GetLoadTest(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "load test not found", nil)
		return
	}
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, lt)
}

func (d Deps) listLoadTests(c *gin.Context) {
	items, err := d.Store.ListLoadTests(c, projectID(c))
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) abortLoadTest(c *gin.Context) {
	id := c.Param("id")
	if _, err := d.Store.GetLoadTest(c, id); errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "load test not found", nil)
		return
	}
	if err := d.Signal.PublishAbort(c, id); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": id, "status": "aborting"})
}

func (d Deps) getResults(c *gin.Context) {
	r, err := d.Store.GetResult(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "results not ready", nil)
		return
	}
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, r)
}
