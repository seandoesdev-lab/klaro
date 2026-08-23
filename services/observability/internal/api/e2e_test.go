//go:build e2e

// End-to-end against a running deploy/docker-compose stack. This is the only
// test that proves the parts actually fit: telemetry authenticated at the
// gateway, routed into the right storage tenant, replicated to a WebSocket, and
// read back out through the Explorer with the org forced in server side.
//
//	E2E_OBSPLANE_URL   control plane base, e.g. http://obsplane:8090
//	E2E_OTLP_URL       collector OTLP/HTTP base, e.g. http://otel-collector:4318
//	E2E_ORG_ID         the org the dev token authenticates
//	E2E_DEV_TOKEN      bearer token for the control plane
//	E2E_OBS_KEY        an ingest key secret issued for that org
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klaro/observability/internal/live"
)

type e2eEnv struct {
	obsplane string
	otlp     string
	org      string
	token    string
	key      string
}

func env(t *testing.T) e2eEnv {
	t.Helper()
	e := e2eEnv{
		obsplane: os.Getenv("E2E_OBSPLANE_URL"),
		otlp:     os.Getenv("E2E_OTLP_URL"),
		org:      os.Getenv("E2E_ORG_ID"),
		token:    os.Getenv("E2E_DEV_TOKEN"),
		key:      os.Getenv("E2E_OBS_KEY"),
	}
	if e.obsplane == "" || e.otlp == "" || e.org == "" || e.key == "" {
		t.Skip("E2E_* environment not set")
	}
	return e
}

// runToken keeps each execution in its own metric and trace namespace, so a
// re-run never asserts on data the previous run left behind.
var runToken = fmt.Sprintf("%d", time.Now().UnixNano())

func (e e2eEnv) ingest(t *testing.T, signal, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.otlp+"/v1/"+signal, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("klaro-obs-key", e.key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ingest %s: %v", signal, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		t.Fatalf("ingest %s = %s: %s", signal, resp.Status, out)
	}
}

func (e e2eEnv) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.obsplane+"/orgs/"+e.org+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body
}

// eventually retries until the backends have indexed what was just written.
// Ingest is asynchronous end to end - a Collector flush, a remote write, an
// index build - so a single immediate read would be a race, not a test.
func eventually(t *testing.T, what string, attempt func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if attempt() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func metricName() string { return "klaro_e2e_" + runToken }

func (e e2eEnv) metricsPayload() string {
	now := time.Now().UnixNano()
	return fmt.Sprintf(`{"resourceMetrics":[{"resource":{"attributes":[
	  {"key":"service.name","value":{"stringValue":"e2e-checkout"}},
	  {"key":"service.instance.id","value":{"stringValue":"e2e-host-%s"}}]},
	 "scopeMetrics":[{"scope":{"name":"klaro-e2e"},"metrics":[
	  {"name":"%s","gauge":{"dataPoints":[
	    {"asDouble":42,"timeUnixNano":"%d"}]}}]}]}]}`, runToken, metricName(), now)
}

// The live path, end to end: SDK -> gateway -> control plane -> Redis -> socket.
// The design budget is two seconds (APM-02), which the Collector batch timeout
// sets; ten is the allowance for a cold container.
func TestE2ELiveWebSocketReceivesIngestedMetrics(t *testing.T) {
	e := env(t)

	url := "ws" + strings.TrimPrefix(e.obsplane, "http") +
		"/orgs/" + e.org + "/obs/live?stream=metric"
	hdr := http.Header{}
	if e.token != "" {
		hdr.Set("Authorization", "Bearer "+e.token)
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, hdr)
	if err != nil {
		t.Fatalf("dial live socket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Ingest after the socket is up, so the frame cannot have been published
	// before there was anyone to receive it.
	e.ingest(t, "metrics", e.metricsPayload())

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("no live frame arrived: %v", err)
		}
		var frame live.Frame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("frame is not JSON: %v", err)
		}
		if frame.Stream != live.StreamMetric {
			t.Fatalf("frame stream = %q", frame.Stream)
		}
		for _, p := range frame.Points {
			if p.Labels["__name__"] != metricName() {
				continue
			}
			if p.Value != 42 {
				t.Errorf("live value = %v, want 42", p.Value)
			}
			// Routing plumbing must not reach a browser.
			for _, banned := range []string{"klaro.org_id", "vm_account_id", "vm_project_id"} {
				if _, leaked := p.Labels[banned]; leaked {
					t.Errorf("live frame leaks %q: %v", banned, p.Labels)
				}
			}
			return
		}
	}
}

// The metric written above must come back out of the org's VictoriaMetrics
// tenant through the Explorer.
func TestE2EExplorerReadsMetricsBack(t *testing.T) {
	e := env(t)
	e.ingest(t, "metrics", e.metricsPayload())

	var body []byte
	eventually(t, "the metric to appear in the Explorer", func() bool {
		var status int
		status, body = e.get(t, "/obs/metrics/query?metric="+metricName()+"&step=60")
		if status != http.StatusOK {
			return false
		}
		var out struct {
			Series []struct {
				Labels map[string]string    `json:"labels"`
				Points [][2]json.RawMessage `json:"points"`
			} `json:"series"`
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return false
		}
		return len(out.Series) > 0 && len(out.Series[0].Points) > 0
	})

	var out struct {
		Series []struct {
			Labels map[string]string `json:"labels"`
		} `json:"series"`
		Query string `json:"query"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	// The org matcher is injected server side, and returning the generated
	// query is what makes that inspectable rather than a claim.
	if !strings.Contains(out.Query, `klaro_org_id="`+e.org+`"`) {
		t.Errorf("generated query has no org matcher: %s", out.Query)
	}
	if out.Series[0].Labels["service_name"] != "e2e-checkout" {
		t.Errorf("labels = %v", out.Series[0].Labels)
	}
	for _, banned := range []string{"klaro_org_id", "vm_account_id", "vm_project_id"} {
		if _, leaked := out.Series[0].Labels[banned]; leaked {
			t.Errorf("%q reached the client", banned)
		}
	}
}

func (e e2eEnv) tracePayload(traceID, spanID string) string {
	start := time.Now().Add(-4 * time.Second).UnixNano()
	end := time.Now().UnixNano()
	return fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[
	  {"key":"service.name","value":{"stringValue":"e2e-checkout"}}]},
	 "scopeSpans":[{"scope":{"name":"klaro-e2e"},"spans":[
	  {"traceId":"%s","spanId":"%s","name":"GET /cart","kind":2,
	   "startTimeUnixNano":"%d","endTimeUnixNano":"%d","status":{}}]}]}]}`,
		traceID, spanID, start, end)
}

// Trace search and the waterfall read, both scoped by the tenant header.
// Trace search and the waterfall read, both scoped by the tenant header.
func TestE2EExplorerReadsTracesBack(t *testing.T) {
	e := env(t)

	// A trace id is 32 hex characters; derive one from the run token so repeat
	// runs do not collide. The high nibble is forced non-zero because Tempo
	// normalises away leading zeros, and an id that comes back shorter than it
	// went in cannot be compared against what was sent.
	traceID := fmt.Sprintf("f%031x", time.Now().UnixNano())
	spanID := fmt.Sprintf("%016x", time.Now().UnixNano())
	e.ingest(t, "traces", e.tracePayload(traceID, spanID))

	var found string
	eventually(t, "the trace to be searchable", func() bool {
		status, body := e.get(t, "/obs/traces?service=e2e-checkout&min_duration_ms=1000")
		if status != http.StatusOK {
			return false
		}
		var out struct {
			Data []struct {
				TraceID     string  `json:"trace_id"`
				RootService string  `json:"root_service"`
				DurationMS  float64 `json:"duration_ms"`
				Start       int64   `json:"start"`
			} `json:"data"`
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return false
		}
		if !strings.Contains(out.Query, "e2e-checkout") {
			t.Fatalf("generated TraceQL does not filter by service: %s", out.Query)
		}
		for _, hit := range out.Data {
			if !strings.EqualFold(hit.TraceID, traceID) {
				continue
			}
			if hit.RootService != "e2e-checkout" || hit.DurationMS <= 0 || hit.Start == 0 {
				t.Fatalf("search hit is missing fields: %+v", hit)
			}
			found = hit.TraceID
			return true
		}
		return false
	})

	status, body := e.get(t, "/obs/traces/"+found)
	if status != http.StatusOK {
		t.Fatalf("GET trace = %d: %s", status, body)
	}
	var trace struct {
		TraceID string `json:"trace_id"`
		Spans   []struct {
			SpanID     string  `json:"span_id"`
			Name       string  `json:"name"`
			Service    string  `json:"service"`
			Start      int64   `json:"start"`
			DurationMS float64 `json:"duration_ms"`
		} `json:"spans"`
	}
	if err := json.Unmarshal(body, &trace); err != nil {
		t.Fatal(err)
	}
	if len(trace.Spans) == 0 {
		t.Fatalf("waterfall is empty: %s", body)
	}
	s := trace.Spans[0]
	if !strings.EqualFold(s.SpanID, spanID) || s.Service != "e2e-checkout" {
		t.Errorf("span = %+v", s)
	}
	// A waterfall needs both a position and a width, or it cannot be drawn.
	if s.Start == 0 || s.DurationMS <= 0 {
		t.Errorf("span has no position or width: %+v", s)
	}
}

func (e e2eEnv) logPayload(message string) string {
	return fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[
	  {"key":"service.name","value":{"stringValue":"e2e-checkout"}}]},
	 "scopeLogs":[{"scope":{"name":"klaro-e2e"},"logRecords":[
	  {"timeUnixNano":"%d","severityNumber":17,"severityText":"ERROR",
	   "body":{"stringValue":"%s"}}]}]}]}`, time.Now().UnixNano(), message)
}

func TestE2EExplorerReadsLogsBack(t *testing.T) {
	e := env(t)
	message := "e2e log line " + runToken
	e.ingest(t, "logs", e.logPayload(message))

	var body []byte
	eventually(t, "the log line to be queryable", func() bool {
		var status int
		status, body = e.get(t, "/obs/logs?filter=service_name%3De2e-checkout&limit=100")
		return status == http.StatusOK && strings.Contains(string(body), message)
	})

	var page struct {
		Data []struct {
			TS      int64             `json:"ts"`
			Level   string            `json:"level"`
			Message string            `json:"message"`
			Labels  map[string]string `json:"labels"`
		} `json:"data"`
		Query string `json:"query"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.Query, `klaro_org_id="`+e.org+`"`) {
		t.Errorf("generated LogQL has no org matcher: %s", page.Query)
	}
	var found bool
	for _, entry := range page.Data {
		if entry.Message != message {
			continue
		}
		found = true
		if entry.TS == 0 {
			t.Error("log entry has no timestamp")
		}
		if entry.Level == "" {
			t.Error("log entry has no level")
		}
		if _, leaked := entry.Labels["klaro_org_id"]; leaked {
			t.Error("the org label reached the client")
		}
	}
	if !found {
		t.Errorf("the ingested line is not in the page: %s", body)
	}
}

// A caller must not be able to read another org, and must not be able to take
// the org matcher off its own query.
func TestE2EExplorerRefusesCrossTenantAndReservedFilters(t *testing.T) {
	e := env(t)
	other := "00000000-0000-0000-0000-0000000000ff"

	req, err := http.NewRequest(http.MethodGet,
		e.obsplane+"/orgs/"+other+"/obs/metrics/query?metric=up", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-tenant read = %d, want 403", resp.StatusCode)
	}

	if status, body := e.get(t, "/obs/logs?filter=klaro_org_id%3D"+other); status != http.StatusUnprocessableEntity {
		t.Errorf("reserved filter = %d, want 422: %s", status, body)
	}
}

// The tenancy invariant that has no visible failure mode: telemetry must land in
// the org's VictoriaMetrics tenant and NOT in account 0, the default account
// every org would otherwise share.
//
// This is a white-box check against vmselect rather than through the Explorer,
// because the Explorer can only ever query the caller's own tenant - which is
// the point, and also why nothing in the product surface would notice the leak.
// It found one: routing the tenant by request header lost it inside the remote
// write exporter's goroutine hop, so the samples went to account 0 while only
// target_info arrived correctly.
func TestE2EMetricsDoNotLandInTheDefaultTenant(t *testing.T) {
	e := env(t)
	vmselect := os.Getenv("E2E_VMSELECT_URL")
	if vmselect == "" {
		t.Skip("E2E_VMSELECT_URL not set")
	}
	e.ingest(t, "metrics", e.metricsPayload())

	names := func(tenant string) string {
		resp, err := http.Get(vmselect + "/select/" + tenant + "/prometheus/api/v1/label/__name__/values")
		if err != nil {
			t.Fatalf("vmselect tenant %s: %v", tenant, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return string(body)
	}

	// The org's tenant is the AccountID the control plane assigned; ask it for
	// the mapping rather than assuming 1.
	status, body := e.get(t, "/obs/tenant")
	if status != http.StatusOK {
		t.Fatalf("GET tenant = %d: %s", status, body)
	}
	var tenant struct {
		VMTenantPath string `json:"vm_tenant_path"`
	}
	if err := json.Unmarshal(body, &tenant); err != nil {
		t.Fatal(err)
	}
	if tenant.VMTenantPath == "" || tenant.VMTenantPath == "0" {
		t.Fatalf("org is mapped to tenant %q, which is the shared default account", tenant.VMTenantPath)
	}

	eventually(t, "the metric to appear in the org tenant", func() bool {
		return strings.Contains(names(tenant.VMTenantPath), metricName())
	})
	if got := names("0"); strings.Contains(got, metricName()) {
		t.Errorf("metric %s leaked into the default account 0", metricName())
	}
}
