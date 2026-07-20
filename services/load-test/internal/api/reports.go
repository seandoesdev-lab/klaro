package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/store"
	"github.com/klaro/load-test/internal/tenancy"
)

// createReport builds inputs from a load test and/or scan, computes scores + a
// MOCK ai_summary, and stores the report synchronously (MVP).
func (d Deps) createReport(c *gin.Context) {
	var req struct {
		LoadTestID *string `json:"load_test_id"`
		ScanID     *string `json:"scan_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}
	if req.LoadTestID == nil && req.ScanID == nil {
		writeError(c, 400, "VALIDATION_ERROR", "load_test_id or scan_id required", nil)
		return
	}
	pid := projectID(c)
	tx := tenancy.Tx(c)

	perf, err := d.perfInput(c, tx, req.LoadTestID)
	if err != nil {
		writeInternal(c, err)
		return
	}
	sec, err := d.secInput(c, tx, req.ScanID)
	if err != nil {
		writeInternal(c, err)
		return
	}

	perfScore := model.ComputePerformanceScore(perf)
	secScore := model.ComputeSecurityScore(sec)
	summary := model.BuildAISummary(perf, sec)

	rep := &model.Report{
		ProjectID: pid, LoadTestID: req.LoadTestID, ScanID: req.ScanID,
		PerformanceScore: perfScore, SecurityScore: secScore,
		AISummary: summary, Status: model.ReportGenerating,
	}
	if err := d.Store.CreateReport(c, tx, rep); err != nil {
		writeInternal(c, err)
		return
	}
	// Synchronous generation: flip to ready immediately.
	if err := d.Store.UpdateReport(c, tx, rep.ID, perfScore, secScore, summary, model.ReportReady); err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": rep.ID, "status": model.ReportReady})
}

// perfInput reads load_test_results for a load test, if provided.
func (d Deps) perfInput(c *gin.Context, tx store.Querier, loadTestID *string) (model.PerfInput, error) {
	if loadTestID == nil {
		return model.PerfInput{}, nil
	}
	r, err := d.Store.GetResult(c, tx, *loadTestID)
	if errors.Is(err, store.ErrNotFound) {
		return model.PerfInput{}, nil // no results yet → skip perf scoring
	}
	if err != nil {
		return model.PerfInput{}, err
	}
	return model.PerfInput{
		HasData:    true,
		RPSAvg:     r.RPSAvg,
		LatencyP95: r.LatencyP95,
		ErrorRate:  r.ErrorRate,
		MaxVU:      r.MaxVUBeforeDegradation,
		Bottleneck: r.BottleneckEndpoint,
	}, nil
}

// secInput reads a scan + its finding severity counts, if provided.
func (d Deps) secInput(c *gin.Context, tx store.Querier, scanID *string) (model.SecInput, error) {
	if scanID == nil {
		return model.SecInput{}, nil
	}
	sc, err := d.Store.GetScan(c, tx, *scanID)
	if errors.Is(err, store.ErrNotFound) {
		return model.SecInput{}, nil
	}
	if err != nil {
		return model.SecInput{}, err
	}
	counts, err := d.Store.SeverityCounts(c, tx, *scanID)
	if err != nil {
		return model.SecInput{}, err
	}
	return model.SecInput{
		HasData:   true,
		Critical:  counts[model.SeverityCritical],
		High:      counts[model.SeverityHigh],
		Medium:    counts[model.SeverityMedium],
		Low:       counts[model.SeverityLow],
		Info:      counts[model.SeverityInfo],
		ScanScore: sc.Score,
	}, nil
}

func (d Deps) listReports(c *gin.Context) {
	items, err := d.Store.ListReports(c, tenancy.Tx(c), projectID(c))
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) getReport(c *gin.Context) {
	rep, err := d.Store.GetReport(c, tenancy.Tx(c), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "report not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, rep)
}

func (d Deps) createShare(c *gin.Context) {
	reportID := c.Param("id")
	tx := tenancy.Tx(c)
	if _, err := d.Store.GetReport(c, tx, reportID); errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "report not found", nil)
		return
	}
	var req struct {
		Password     string `json:"password"`
		ExpiresInDay int    `json:"expires_in_days"`
	}
	_ = c.ShouldBindJSON(&req) // body optional

	var pwHash *string
	if req.Password != "" {
		h := model.HashReportPassword(req.Password)
		pwHash = &h
	}
	var expires *time.Time
	if req.ExpiresInDay > 0 {
		t := time.Now().UTC().Add(time.Duration(req.ExpiresInDay) * 24 * time.Hour)
		expires = &t
	}
	sh, err := d.Store.CreateShare(c, tx, reportID, pwHash, expires)
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"slug":       sh.Slug,
		"url":        "/shared/" + sh.Slug,
		"expires_at": sh.ExpiresAt,
	})
}

func (d Deps) revokeShare(c *gin.Context) {
	err := d.Store.RevokeShare(c, tenancy.Tx(c), c.Param("id"), c.Param("shareId"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "share not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": c.Param("shareId"), "status": "revoked"})
}

// getSharedReport is PUBLIC (outside the auth group). Password via X-Report-Password.
func (d Deps) getSharedReport(c *gin.Context) {
	sh, err := d.Store.GetShareBySlug(c, c.Param("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "share not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	if sh.RevokedAt != nil {
		writeError(c, 410, "GONE", "share link revoked", nil)
		return
	}
	if sh.ExpiresAt != nil && time.Now().After(*sh.ExpiresAt) {
		writeError(c, 410, "GONE", "share link expired", nil)
		return
	}
	if sh.PasswordHash != nil {
		if !model.VerifyReportPassword(*sh.PasswordHash, c.GetHeader("X-Report-Password")) {
			writeError(c, 401, "UNAUTHENTICATED", "invalid password", nil)
			return
		}
	}
	rep, err := d.Store.GetReportSys(c, sh.ReportID)
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, rep)
}
