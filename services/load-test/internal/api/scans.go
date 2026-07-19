package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/store"
)

func (d Deps) createScan(c *gin.Context) {
	var req struct {
		Type      string `json:"type"`
		TargetURL string `json:"target_url"`
		PRNumber  *int   `json:"pr_number"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}
	st := model.ScanType(req.Type)
	if st != model.ScanTypeSAST && st != model.ScanTypeDAST {
		writeError(c, 400, "VALIDATION_ERROR", "type must be sast or dast", nil)
		return
	}

	pid := projectID(c)
	sc := &model.Scan{ProjectID: pid, Type: st, Status: model.ScanStatusPending}

	// DAST requires a verified target domain.
	if st == model.ScanTypeDAST {
		if req.TargetURL == "" {
			writeError(c, 400, "VALIDATION_ERROR", "target_url required for dast", nil)
			return
		}
		host, err := store.HostFromURL(req.TargetURL)
		if err != nil {
			writeError(c, 400, "VALIDATION_ERROR", "invalid target_url", nil)
			return
		}
		verified, err := d.Store.IsDomainVerified(c, pid, host)
		if err != nil {
			writeError(c, 500, "INTERNAL", err.Error(), nil)
			return
		}
		if !verified {
			writeError(c, 403, "DOMAIN_NOT_VERIFIED", "target domain must be verified", nil)
			return
		}
		sc.TargetURL = &req.TargetURL
	}

	// trigger: PR-driven if a pr_number is supplied, else manual.
	if req.PRNumber != nil {
		sc.Trigger = model.ScanTriggerPR
		sc.PRNumber = req.PRNumber
	} else {
		sc.Trigger = model.ScanTriggerManual
	}

	if err := d.Store.CreateScan(c, sc); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}

	job := model.ScanJob{ScanID: sc.ID, ProjectID: pid, Type: st, TargetURL: req.TargetURL}
	if err := d.ScanQueue.EnqueueScan(c, job); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": sc.ID, "status": sc.Status})
}

func (d Deps) listScans(c *gin.Context) {
	items, err := d.Store.ListScans(c, projectID(c))
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) getScan(c *gin.Context) {
	sc, err := d.Store.GetScan(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "scan not found", nil)
		return
	}
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, sc)
}

func (d Deps) listFindings(c *gin.Context) {
	if _, err := d.Store.GetScan(c, c.Param("id")); errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "scan not found", nil)
		return
	}
	items, err := d.Store.ListFindings(c, c.Param("id"))
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) updateFinding(c *gin.Context) {
	var req struct {
		Status       string  `json:"status"`
		IgnoreReason *string `json:"ignore_reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}
	fs := model.FindingStatus(req.Status)
	if fs != model.FindingOpen && fs != model.FindingIgnored && fs != model.FindingFixed {
		writeError(c, 400, "VALIDATION_ERROR", "status must be open, ignored or fixed", nil)
		return
	}
	err := d.Store.UpdateFindingStatus(c, c.Param("findingId"), fs, req.IgnoreReason)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "finding not found", nil)
		return
	}
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": c.Param("findingId"), "status": fs})
}
