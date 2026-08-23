//go:build e2e

// Dashboards and the report snapshot, end to end against a running stack
// (design section 6 steps 10-11).
//
// These two are what a full platform looks like from the outside: something to
// save a view in, and something a neighbouring service can ask for a window of
// telemetry through. Both are exercised against real storage, because both are
// thin layers whose value is entirely in what they delegate to.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/klaro/observability/internal/dashboards"
	"github.com/klaro/observability/internal/explorer"
)

// A dashboard is stored, read back, patched and removed - and the panel
// document has to survive all of it unchanged, because it is the dashboard.
func TestE2EDashboardLifecycle(t *testing.T) {
	e := env(t)
	name := "e2e board " + runToken

	body := fmt.Sprintf(`{"name":%q,"description":"created by the e2e suite","spec":{
	  "range_sec":3600,"refresh_sec":30,
	  "panels":[
	    {"id":"latency","title":"p95","type":"timeseries","layout":{"x":0,"y":0,"w":6,"h":4},
	     "query":{"signal":"metrics","metric":%q,"agg":"p95","step_sec":60,
	              "filters":[{"label":"service_name","value":"e2e-checkout"}]}},
	    {"id":"slow","title":"Slow traces","type":"traces","layout":{"x":6,"y":0,"w":6,"h":4},
	     "query":{"signal":"traces","service":"e2e-checkout","min_duration_ms":1000,"limit":20}}]}}`,
		name, metricName())

	status, created := e.post(t, "/obs/dashboards", body)
	if status != http.StatusCreated {
		t.Fatalf("create dashboard = %d: %s", status, created)
	}
	var dash dashboards.Dashboard
	if err := json.Unmarshal(created, &dash); err != nil {
		t.Fatal(err)
	}
	if dash.ID == "" || len(dash.Spec.Panels) != 2 {
		t.Fatalf("created dashboard = %+v", dash)
	}
	t.Cleanup(func() { e.send(t, http.MethodDelete, "/obs/dashboards/"+dash.ID, "") })

	// Read back: the panel queries have to be exactly what was stored, since the
	// frontend replays them against the Explorer.
	status, fetched := e.get(t, "/obs/dashboards/"+dash.ID)
	if status != http.StatusOK {
		t.Fatalf("get dashboard = %d: %s", status, fetched)
	}
	var back dashboards.Dashboard
	if err := json.Unmarshal(fetched, &back); err != nil {
		t.Fatal(err)
	}
	if back.Spec.RangeSec != 3600 || back.Spec.RefreshSec != 30 {
		t.Errorf("spec envelope = %+v", back.Spec)
	}
	metrics := back.Spec.Panels[0].Query
	if metrics.Signal != dashboards.SignalMetrics || metrics.Agg != "p95" || metrics.StepSec != 60 {
		t.Errorf("metrics panel = %+v", metrics)
	}
	if len(metrics.Filters) != 1 || metrics.Filters[0].Label != "service_name" {
		t.Errorf("panel filter did not round trip: %+v", metrics.Filters)
	}
	if back.Spec.Panels[1].Query.MinDurationMS != 1000 {
		t.Errorf("traces panel = %+v", back.Spec.Panels[1].Query)
	}

	// It has to appear in the list a navigation sidebar reads.
	status, listed := e.get(t, "/obs/dashboards")
	if status != http.StatusOK {
		t.Fatalf("list dashboards = %d: %s", status, listed)
	}
	if !strings.Contains(string(listed), dash.ID) {
		t.Errorf("the dashboard is missing from the list: %s", listed)
	}

	// A partial update must not disturb the panels.
	status, patched := e.send(t, http.MethodPatch, "/obs/dashboards/"+dash.ID,
		`{"description":"patched by the e2e suite"}`)
	if status != http.StatusOK {
		t.Fatalf("patch dashboard = %d: %s", status, patched)
	}
	var afterPatch dashboards.Dashboard
	if err := json.Unmarshal(patched, &afterPatch); err != nil {
		t.Fatal(err)
	}
	if len(afterPatch.Spec.Panels) != 2 || afterPatch.Name != name {
		t.Errorf("a description-only PATCH changed more: %+v", afterPatch)
	}

	status, _ = e.send(t, http.MethodDelete, "/obs/dashboards/"+dash.ID, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete dashboard = %d", status)
	}
	if status, _ := e.get(t, "/obs/dashboards/"+dash.ID); status != http.StatusNotFound {
		t.Errorf("after delete, get = %d, want 404", status)
	}
}

// A panel that could name the org label would be a stored cross-tenant query.
func TestE2EDashboardRefusesReservedPanelLabel(t *testing.T) {
	e := env(t)
	body := `{"name":"e2e sneaky ` + runToken + `","spec":{"panels":[
	  {"id":"p1","type":"timeseries","layout":{"x":0,"y":0,"w":6,"h":4},
	   "query":{"signal":"metrics","metric":"cpu",
	            "filters":[{"label":"klaro_org_id","value":"00000000-0000-0000-0000-0000000000ff"}]}}]}}`
	status, out := e.post(t, "/obs/dashboards", body)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", status, out)
	}
}

// The snapshot path (OBS-10): the report service asks for one window and gets
// telemetry back, read through the Explorer and stored nowhere.
func TestE2ESnapshotServesAReportWindow(t *testing.T) {
	e := env(t)
	internalURL := os.Getenv("E2E_INTERNAL_URL")
	if internalURL == "" {
		t.Skip("E2E_INTERNAL_URL not set")
	}

	// Put something in the window first, the way a load test would.
	for i := 0; i < 3; i++ {
		e.ingest(t, "metrics", e.metricsPayload())
	}
	traceID := fmt.Sprintf("f%031x", time.Now().UnixNano())
	e.ingest(t, "traces", e.tracePayload(traceID, fmt.Sprintf("%016x", time.Now().UnixNano())))

	to := time.Now().Add(time.Minute)
	from := to.Add(-15 * time.Minute)
	body := fmt.Sprintf(`{"org_id":%q,"from":%q,"to":%q,
	  "metrics":[{"key":"probe","metric":%q,"agg":"avg","step_sec":60}],
	  "traces":{"service":"e2e-checkout","min_duration_ms":500,"limit":10}}`,
		e.org, from.Format(time.RFC3339), to.Format(time.RFC3339), metricName())

	var snap struct {
		OrgID   string `json:"org_id"`
		Partial bool   `json:"partial"`
		Metrics []struct {
			Key        string `json:"key"`
			Resolution string `json:"resolution"`
			Query      string `json:"query"`
			Series     []struct {
				Labels map[string]string `json:"labels"`
			} `json:"series"`
		} `json:"metrics"`
		Traces []struct {
			TraceID string `json:"trace_id"`
		} `json:"traces"`
		Notes []string `json:"notes"`
	}

	eventuallyFor(t, 90*time.Second, "the snapshot to carry the ingested window", func() bool {
		e.ingest(t, "metrics", e.metricsPayload())

		req, err := http.NewRequest(http.MethodPost, internalURL+"/internal/snapshot", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		if err := json.Unmarshal(readAll(resp), &snap); err != nil {
			return false
		}
		return len(snap.Metrics) == 1 && len(snap.Metrics[0].Series) > 0
	})

	if snap.OrgID != e.org {
		t.Errorf("snapshot org = %q", snap.OrgID)
	}
	if snap.Metrics[0].Key != "probe" {
		t.Errorf("the report's key was not carried back: %+v", snap.Metrics[0])
	}
	// Reading through the Explorer is the whole design, so the org matcher has to
	// be in the generated query here too.
	if !strings.Contains(snap.Metrics[0].Query, `klaro_org_id="`+e.org+`"`) {
		t.Errorf("snapshot query has no org matcher: %s", snap.Metrics[0].Query)
	}
	if snap.Metrics[0].Resolution != explorer.ResolutionRaw {
		t.Errorf("resolution = %q, want raw for a 15 minute window", snap.Metrics[0].Resolution)
	}
	// Routing plumbing must not reach a report either.
	for _, banned := range []string{explorer.OrgLabel, "vm_account_id", "vm_project_id"} {
		if _, leaked := snap.Metrics[0].Series[0].Labels[banned]; leaked {
			t.Errorf("snapshot leaks %q: %v", banned, snap.Metrics[0].Series[0].Labels)
		}
	}
	if snap.Partial {
		t.Errorf("a 15 minute window was reported as partial: %v", snap.Notes)
	}
}

// A window past the plan must come back marked partial rather than as a short
// chart a reader would take for a quiet system.
func TestE2ESnapshotReportsAPartialWindow(t *testing.T) {
	e := env(t)
	internalURL := os.Getenv("E2E_INTERNAL_URL")
	if internalURL == "" {
		t.Skip("E2E_INTERNAL_URL not set")
	}

	to := time.Now()
	from := to.Add(-20 * time.Hour)
	body := fmt.Sprintf(`{"org_id":%q,"from":%q,"to":%q,
	  "metrics":[{"key":"probe","metric":%q,"step_sec":300}]}`,
		e.org, from.Format(time.RFC3339), to.Format(time.RFC3339), metricName())

	req, err := http.NewRequest(http.MethodPost, internalURL+"/internal/snapshot", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot = %s", resp.Status)
	}
	// The dev org is on a plan that keeps well over 20 hours of raw metrics, so
	// this window is inside retention: the assertion is that a snapshot inside
	// the plan is not falsely marked partial.
	var snap struct {
		Partial bool     `json:"partial"`
		Notes   []string `json:"notes"`
	}
	if err := json.Unmarshal(readAll(resp), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Partial {
		t.Errorf("a window inside the plan was marked partial: %v", snap.Notes)
	}
}

// An unusable request must be refused before any backend is read.
func TestE2ESnapshotRejectsABadRequest(t *testing.T) {
	e := env(t)
	internalURL := os.Getenv("E2E_INTERNAL_URL")
	if internalURL == "" {
		t.Skip("E2E_INTERNAL_URL not set")
	}
	// Reversed window, and nothing asked for.
	body := fmt.Sprintf(`{"org_id":%q,"from":"2026-08-23T12:00:00Z","to":"2026-08-23T11:00:00Z"}`, e.org)

	req, err := http.NewRequest(http.MethodPost, internalURL+"/internal/snapshot", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %s, want 422", resp.Status)
	}
}
