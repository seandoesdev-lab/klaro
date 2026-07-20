package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/tenancy"
)

func (d Deps) createAgent(c *gin.Context) {
	var req struct {
		Language string `json:"language"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}
	lang := model.AgentLanguage(req.Language)
	if !model.ValidAgentLanguage(lang) {
		writeError(c, 400, "VALIDATION_ERROR", "language must be nodejs, springboot or fastapi", nil)
		return
	}
	a := &model.ApmAgent{ProjectID: projectID(c), Language: lang}
	if err := d.Store.CreateAgent(c, tenancy.Tx(c), a); err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": a.ID, "ingest_token": a.IngestToken})
}

func (d Deps) listAgents(c *gin.Context) {
	items, err := d.Store.ListAgents(c, tenancy.Tx(c), projectID(c))
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// apmIngest is authenticated by authenticateIngest (X-Ingest-Token → org) and
// runs inside an org-scoped tx (tenancyTx). Registered outside the auth group.
func (d Deps) apmIngest(c *gin.Context) {
	pid := c.Param("id")
	agent, _ := c.MustGet(ctxIngestAgent).(*model.ApmAgent)

	var req struct {
		Spans []model.ApmSpan `json:"spans"`
		Logs  []model.ApmLog  `json:"logs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}

	now := time.Now().UTC()
	for i := range req.Spans {
		req.Spans[i].ProjectID = pid
		if req.Spans[i].Service == "" && agent != nil {
			req.Spans[i].Service = string(agent.Language)
		}
		if req.Spans[i].Ts.IsZero() {
			req.Spans[i].Ts = now
		}
	}
	for i := range req.Logs {
		req.Logs[i].ProjectID = pid
		if req.Logs[i].Ts.IsZero() {
			req.Logs[i].Ts = now
		}
	}
	tx := tenancy.Tx(c)
	if err := d.Store.InsertSpans(c, tx, req.Spans); err != nil {
		writeInternal(c, err)
		return
	}
	if err := d.Store.InsertLogs(c, tx, req.Logs); err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"accepted": gin.H{"spans": len(req.Spans), "logs": len(req.Logs)},
	})
}

func (d Deps) listSlowTraces(c *gin.Context) {
	minMs := 3000.0
	if q := c.Query("min_ms"); q != "" {
		if v, err := strconv.ParseFloat(q, 64); err == nil {
			minMs = v
		}
	}
	items, err := d.Store.SlowTraces(c, tenancy.Tx(c), projectID(c), minMs)
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) getTrace(c *gin.Context) {
	spans, err := d.Store.TraceByID(c, tenancy.Tx(c), projectID(c), c.Param("traceId"))
	if err != nil {
		writeInternal(c, err)
		return
	}
	if len(spans) == 0 {
		writeError(c, 404, "NOT_FOUND", "trace not found", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"trace_id": c.Param("traceId"), "spans": spans})
}

func (d Deps) listApmLogs(c *gin.Context) {
	limit := 100
	if q := c.Query("limit"); q != "" {
		if v, err := strconv.Atoi(q); err == nil {
			limit = v
		}
	}
	items, err := d.Store.ListLogs(c, tenancy.Tx(c), projectID(c), limit)
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// seedApmDemo inserts a realistic synthetic trace set + logs so the APM view is
// not empty in the MVP demo. DEV-ONLY seed helper.
func (d Deps) seedApmDemo(c *gin.Context) {
	pid := projectID(c)
	tx := tenancy.Tx(c)
	now := time.Now().UTC()
	spans, logs := demoTelemetry(pid, now)
	if err := d.Store.InsertSpans(c, tx, spans); err != nil {
		writeInternal(c, err)
		return
	}
	if err := d.Store.InsertLogs(c, tx, logs); err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"seeded": gin.H{"spans": len(spans), "logs": len(logs)},
		"note":   "dev-only synthetic APM demo data",
	})
}

// demoTelemetry builds a few traces (some slow >=3s DB queries, some fast) and
// a handful of logs. Pure helper so it stays deterministic-ish and testable.
func demoTelemetry(pid string, now time.Time) ([]model.ApmSpan, []model.ApmLog) {
	ptr := func(s string) *string { return &s }

	// Trace 1: a slow request dominated by a DB query (>= 3s).
	t1 := "trace_" + model.NewIngestToken()[7:19]
	root1 := "span_" + model.NewIngestToken()[7:19]
	db1 := "span_" + model.NewIngestToken()[7:19]
	// Trace 2: a fast, healthy request.
	t2 := "trace_" + model.NewIngestToken()[7:19]
	root2 := "span_" + model.NewIngestToken()[7:19]
	cache2 := "span_" + model.NewIngestToken()[7:19]

	spans := []model.ApmSpan{
		{ProjectID: pid, Service: "api-gateway", TraceID: t1, SpanID: root1, Name: "GET /orders", DurationMs: 3480, Status: "ok", Ts: now.Add(-2 * time.Minute)},
		{ProjectID: pid, Service: "orders-svc", TraceID: t1, SpanID: db1, ParentSpanID: ptr(root1), Name: "SELECT orders JOIN items", DurationMs: 3200, Status: "ok", Ts: now.Add(-2 * time.Minute)},
		{ProjectID: pid, Service: "api-gateway", TraceID: t2, SpanID: root2, Name: "GET /health", DurationMs: 42, Status: "ok", Ts: now.Add(-1 * time.Minute)},
		{ProjectID: pid, Service: "cache", TraceID: t2, SpanID: cache2, ParentSpanID: ptr(root2), Name: "redis GET session", DurationMs: 3, Status: "ok", Ts: now.Add(-1 * time.Minute)},
	}
	logs := []model.ApmLog{
		{ProjectID: pid, Level: "warn", Message: "slow query detected on orders (3.2s)", Ts: now.Add(-2 * time.Minute)},
		{ProjectID: pid, Level: "info", Message: "health check ok", Ts: now.Add(-1 * time.Minute)},
		{ProjectID: pid, Level: "error", Message: "upstream timeout talking to payment provider", Ts: now.Add(-30 * time.Second)},
	}
	return spans, logs
}
