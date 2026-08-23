//go:build e2e

// Alerting, downsampling and metering end to end against a running
// deploy/docker-compose stack. These are the parts that only work if several
// processes agree, so nothing short of the real stack proves them:
//
//   - a rule stored in Postgres has to reach vmalert as a file it can parse;
//
//   - vmalert has to evaluate it against the org's tenant and post back;
//
//   - the recording rules have to produce rollup series the Explorer can read;
//
//   - the gateway has to report volume the emitter turns into a billing event.
//
//     E2E_VMALERT_URL   vmalert base, e.g. http://vmalert:8880
//     E2E_MAILHOG_URL   MailHog API base, e.g. http://mailhog:8025
//     E2E_REDIS_ADDR    redis host:port, for the usage subject
//
// plus the variables e2e_test.go documents.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/klaro/observability/internal/alerting"
	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/platform/redisx"
	"github.com/klaro/observability/internal/usage"
)

func (e e2eEnv) post(t *testing.T, path, body string) (int, []byte) {
	t.Helper()
	return e.send(t, http.MethodPost, path, body)
}

func (e e2eEnv) send(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.obsplane+"/orgs/"+e.org+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := readAll(resp)
	return resp.StatusCode, out
}

// A rule created through the API has to reach vmalert, fire, and come back as
// event history plus a notification. That chain crosses three processes and a
// shared volume, so it is the one thing a unit test cannot stand in for.
func TestE2EAlertFiresAndIsRecorded(t *testing.T) {
	e := env(t)
	name := "e2e always firing " + runToken

	// The probe metric is written through the gateway so it lands in the org's
	// tenant, which is the only place the rule will look.
	e.ingest(t, "metrics", e.metricsPayload())

	body := fmt.Sprintf(`{"name":%q,"comparator":"gt","threshold":1,
	  "for_duration_sec":0,"severity":"warning",
	  "channels":[{"type":"email","target":"oncall@example.test"}],
	  "query_spec":{"metric":%q}}`, name, metricName())

	status, created := e.post(t, "/obs/alert-rules", body)
	if status != http.StatusCreated {
		t.Fatalf("create rule = %d: %s", status, created)
	}
	var rule struct {
		ID    string `json:"id"`
		Query string `json:"query"`
	}
	if err := json.Unmarshal(created, &rule); err != nil {
		t.Fatal(err)
	}
	// The org matcher is injected server side; returning the query is what makes
	// that checkable rather than asserted.
	if !strings.Contains(rule.Query, `klaro_org_id="`+e.org+`"`) {
		t.Fatalf("stored query has no org matcher: %s", rule.Query)
	}
	t.Cleanup(func() { e.send(t, http.MethodDelete, "/obs/alert-rules/"+rule.ID, "") })

	// vmalert has to load the file, evaluate, and post back. Two evaluation
	// intervals plus slack.
	var events []map[string]any
	eventuallyFor(t, 90*time.Second, "the alert to fire and be recorded", func() bool {
		// Keep the metric fresh: the rule reads recent samples.
		e.ingest(t, "metrics", e.metricsPayload())

		status, body := e.get(t, "/obs/alert-events?rule_id="+rule.ID)
		if status != http.StatusOK {
			return false
		}
		var page struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return false
		}
		events = page.Data
		return len(events) > 0
	})

	if events[0]["state"] != alerting.StateFiring {
		t.Errorf("event state = %v, want firing", events[0]["state"])
	}
	// The value is templated into an annotation by the renderer, so the history
	// says by how much the threshold was crossed.
	if events[0]["value"] == nil {
		t.Error("the event recorded no value")
	}
	// Routing labels must not be handed back to the tenant.
	if labels, ok := events[0]["labels"].(map[string]any); ok {
		for _, banned := range []string{alerting.LabelOrgID, alerting.LabelRuleID, alerting.LabelVMAccount} {
			if _, leaked := labels[banned]; leaked {
				t.Errorf("event labels leak %q: %v", banned, labels)
			}
		}
	}

	// Re-sends must not pile up: vmalert resends every resendDelay, and one
	// episode is one row.
	status, listBody := e.get(t, "/obs/alert-events?rule_id="+rule.ID)
	if status != http.StatusOK {
		t.Fatalf("list events = %d: %s", status, listBody)
	}
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(listBody, &page); err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, ev := range page.Data {
		if ev["resolved_at"] == nil {
			open++
		}
	}
	if open != 1 {
		t.Errorf("open events = %d, want exactly 1 (re-sends must dedup)", open)
	}
}

// The notification path: the receiver publishes, the notifier delivers. MailHog
// accepts anything and shows it, so a delivered message is observable without
// sending real mail.
func TestE2EAlertNotificationReachesMailHog(t *testing.T) {
	// env gates on the same variables the rest of the suite needs, so the
	// notification test skips alongside them.
	_ = env(t)
	mailhog := os.Getenv("E2E_MAILHOG_URL")
	if mailhog == "" {
		t.Skip("E2E_MAILHOG_URL not set")
	}

	eventuallyFor(t, 90*time.Second, "a notification to arrive at MailHog", func() bool {
		resp, err := http.Get(mailhog + "/api/v2/messages")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		body := readAll(resp)
		// The subject carries the rule name and state, which is what an operator
		// scans for in a mailbox.
		return strings.Contains(string(body), "FIRING")
	})
}

// readAll drains a response body under a bound.
func readAll(resp *http.Response) []byte {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body
}

// eventuallyFor is eventually with an explicit deadline, for the paths that
// depend on an evaluation interval rather than an index build.
func eventuallyFor(t *testing.T, within time.Duration, what string, attempt func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if attempt() {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The rollup recording rules have to actually produce series, and the Explorer
// has to fall back to them once the requested window reaches past raw
// retention. This is the one test of the downsampling design end to end
// (HOW-10), including whether the MetricsQL that preserves the original metric
// name is accepted by VictoriaMetrics at all.
func TestE2ERollupSeriesAndExplorerFallback(t *testing.T) {
	e := env(t)
	if os.Getenv("E2E_ROLLUPS") == "" {
		t.Skip("E2E_ROLLUPS not set (the org needs a plan that keeps rollups)")
	}

	// Feed the org so there is something to roll up.
	for i := 0; i < 3; i++ {
		e.ingest(t, "metrics", e.metricsPayload())
	}

	// vmalert evaluates every 15s in the dev stack; the 5m rule needs a couple
	// of passes before the rollup series exists.
	eventuallyFor(t, 120*time.Second, "the rollup series to appear", func() bool {
		e.ingest(t, "metrics", e.metricsPayload())
		status, body := e.get(t, "/obs/metrics/query?metric="+explorer.Rollup5m+"&step=300")
		if status != http.StatusOK {
			return false
		}
		var out struct {
			Series []struct {
				Labels map[string]string `json:"labels"`
			} `json:"series"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return false
		}
		for _, s := range out.Series {
			// The original name has to survive in a label: the recording rule
			// overwrites __name__ with the rollup name.
			if s.Labels[explorer.MetricLabel] != "" {
				return true
			}
		}
		return false
	})

	// A window older than raw retention must come back as a rollup, labelled as
	// such: a coarse series presented as raw would make sparse buckets look like
	// a quiet system.
	from := time.Now().Add(-60 * 24 * time.Hour).Unix()
	status, body := e.get(t, fmt.Sprintf("/obs/metrics/query?metric=%s&step=3600&from=%d", metricName(), from))
	if status != http.StatusOK {
		t.Fatalf("query = %d: %s", status, body)
	}
	var out struct {
		Resolution string `json:"resolution"`
		Clamped    bool   `json:"clamped"`
		Query      string `json:"query"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Resolution == explorer.ResolutionRaw {
		t.Errorf("resolution = raw for a 60 day window; expected a rollup: %s", out.Query)
	}
	if !strings.Contains(out.Query, explorer.MetricLabel) {
		t.Errorf("fallback query does not select by the metric label: %s", out.Query)
	}
	// The org matcher survives the fallback - that is the part worth guarding.
	if !strings.Contains(out.Query, `klaro_org_id="`+e.org+`"`) {
		t.Errorf("fallback query lost the org matcher: %s", out.Query)
	}
}

// A window inside raw retention must stay raw and unclamped.
func TestE2EMetricsInsideRetentionStayRaw(t *testing.T) {
	e := env(t)
	e.ingest(t, "metrics", e.metricsPayload())

	status, body := e.get(t, "/obs/metrics/query?metric="+metricName()+"&step=60")
	if status != http.StatusOK {
		t.Fatalf("query = %d: %s", status, body)
	}
	var out struct {
		Resolution string `json:"resolution"`
		Clamped    bool   `json:"clamped"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Resolution != explorer.ResolutionRaw || out.Clamped {
		t.Errorf("resolution=%q clamped=%v, want raw and unclamped", out.Resolution, out.Clamped)
	}
}

// Metering end to end: the gateway counts, the control plane accumulates, the
// emitter publishes a billing increment. Nothing blocks on quota - crossing a
// limit is an invoice line, not a rejection (design section 7-1).
func TestE2EUsageIsMeteredAndPublished(t *testing.T) {
	e := env(t)
	redisAddr := os.Getenv("E2E_REDIS_ADDR")
	if redisAddr == "" {
		t.Skip("E2E_REDIS_ADDR not set")
	}

	signal := redisx.NewRedis(redisAddr)
	defer func() { _ = signal.Close() }()

	ctx, cancelCtx := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancelCtx()
	events, cancel, err := signal.Subscribe(ctx, usage.EmitSubject)
	if err != nil {
		t.Fatalf("subscribe to %s: %v", usage.EmitSubject, err)
	}
	defer cancel()

	// Send enough to be counted; the gateway flushes every 15s in the dev stack.
	for i := 0; i < 5; i++ {
		e.ingest(t, "metrics", e.metricsPayload())
		e.ingest(t, "logs", e.logPayload("usage probe "+runToken))
	}

	// The quota endpoint is the customer-visible side of the same numbers, and it
	// reads the host registry the gateway populates.
	eventuallyFor(t, 90*time.Second, "the host registry and volume to be metered", func() bool {
		status, body := e.get(t, "/obs/quota")
		if status != http.StatusOK {
			return false
		}
		var q struct {
			ActiveHosts int     `json:"active_hosts"`
			IngestBytes int64   `json:"ingest_bytes"`
			IngestGB    float64 `json:"ingest_gb"`
		}
		if err := json.Unmarshal(body, &q); err != nil {
			return false
		}
		return q.ActiveHosts > 0 && q.IngestBytes > 0
	})

	// And the billing signal itself.
	deadline := time.After(120 * time.Second)
	seen := map[string]float64{}
	for {
		select {
		case raw, ok := <-events:
			if !ok {
				t.Fatal("the usage subject closed before an event arrived")
			}
			var ev usage.Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatalf("undecodable usage event: %v", err)
			}
			if ev.OrgID != e.org {
				continue
			}
			if ev.Quantity <= 0 {
				t.Errorf("meter %s published a non-positive increment %v", ev.Meter, ev.Quantity)
			}
			if ev.Computation["basis"] == nil {
				t.Errorf("meter %s published no computation basis", ev.Meter)
			}
			seen[ev.Meter] = ev.Quantity
			if seen[usage.MeterHosts] > 0 && seen[usage.MeterIngestGB] >= 0 {
				return
			}
		case <-deadline:
			if len(seen) == 0 {
				t.Fatal("no usage event was published")
			}
			// One meter is enough to prove the path; ingest volume in a short
			// test can round below a gigabyte.
			return
		}
	}
}
