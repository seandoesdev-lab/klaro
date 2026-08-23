package klarousage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"

	"github.com/klaro/observability/collector/klaroauth"
)

const orgA = "00000000-0000-0000-0000-0000000000aa"

// cpServer stands in for the control plane usage route.
func cpServer(t *testing.T) (*httptest.Server, *[]report) {
	t.Helper()
	var got []report
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var rep report
		if err := json.Unmarshal(body, &rep); err != nil {
			t.Errorf("control plane got undecodable report: %v", err)
		}
		got = append(got, rep)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func tenantCtx(org string) context.Context {
	md := map[string][]string{klaroauth.MetadataOrgID: {org}}
	return client.NewContext(context.Background(), client.Info{Metadata: client.NewMetadata(md)})
}

func metricsBatch(instance string) pmetric.Metrics {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr(attrServiceName, "checkout")
	rm.Resource().Attributes().PutStr(attrInstanceID, instance)
	rm.Resource().Attributes().PutStr(attrEnvironment, "prod")
	dp := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty().SetEmptyGauge().DataPoints().AppendEmpty()
	dp.SetDoubleValue(1)
	return md
}

func newCounter(t *testing.T, endpoint string) *counter {
	t.Helper()
	cfg := Config{Endpoint: endpoint, TLS: TLSConfig{Insecure: true}, FlushInterval: time.Hour}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	m, err := newMeter(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return &counter{meter: m}
}

func TestCountsBytesItemsAndHosts(t *testing.T) {
	srv, got := cpServer(t)
	c := newCounter(t, srv.URL)
	ctx := tenantCtx(orgA)

	for i := 0; i < 3; i++ {
		if _, err := c.countMetrics(ctx, metricsBatch("pod-1")); err != nil {
			t.Fatal(err)
		}
	}
	c.meter.flush(context.Background())

	if len(*got) != 1 {
		t.Fatalf("reports = %d, want 1", len(*got))
	}
	rep := (*got)[0]
	if rep.OrgID != orgA {
		t.Errorf("org = %q", rep.OrgID)
	}
	usage := rep.Signals[signalMetrics]
	if usage.Items != 3 {
		t.Errorf("items = %d, want 3 (one data point per batch)", usage.Items)
	}
	// Bytes are the OTLP protobuf size: what the customer actually sent, and
	// stable regardless of which compression a client enabled.
	if usage.Bytes <= 0 {
		t.Errorf("bytes = %d, want the protobuf size", usage.Bytes)
	}
	if len(rep.Hosts) != 1 || rep.Hosts[0].Ident != "pod-1" {
		t.Fatalf("hosts = %+v, want one deduplicated entry", rep.Hosts)
	}
	if rep.Hosts[0].Service != "checkout" || rep.Hosts[0].Env != "prod" {
		t.Errorf("host = %+v", rep.Hosts[0])
	}
}

// A batch with no resolved org cannot be attributed, and guessing would bill the
// wrong customer.
func TestUnattributedBatchesAreNotMetered(t *testing.T) {
	srv, got := cpServer(t)
	c := newCounter(t, srv.URL)

	if _, err := c.countMetrics(context.Background(), metricsBatch("pod-1")); err != nil {
		t.Fatal(err)
	}
	c.meter.flush(context.Background())
	if len(*got) != 0 {
		t.Errorf("reports = %v, want none", *got)
	}
}

// Two orgs must never share a counter.
func TestOrgsAreMeteredSeparately(t *testing.T) {
	srv, got := cpServer(t)
	c := newCounter(t, srv.URL)
	const orgB = "00000000-0000-0000-0000-0000000000bb"

	if _, err := c.countMetrics(tenantCtx(orgA), metricsBatch("pod-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.countMetrics(tenantCtx(orgB), metricsBatch("pod-b")); err != nil {
		t.Fatal(err)
	}
	c.meter.flush(context.Background())

	if len(*got) != 2 {
		t.Fatalf("reports = %d, want one per org", len(*got))
	}
	for _, rep := range *got {
		if len(rep.Hosts) != 1 {
			t.Errorf("org %s got %d hosts, want its own only", rep.OrgID, len(rep.Hosts))
		}
	}
}

// Draining resets: a flush must not re-report volume already billed.
func TestFlushResetsCounters(t *testing.T) {
	srv, got := cpServer(t)
	c := newCounter(t, srv.URL)

	if _, err := c.countMetrics(tenantCtx(orgA), metricsBatch("pod-1")); err != nil {
		t.Fatal(err)
	}
	c.meter.flush(context.Background())
	c.meter.flush(context.Background())

	if len(*got) != 1 {
		t.Errorf("reports = %d, want 1 (the second flush had nothing to send)", len(*got))
	}
}

func TestConfigValidate(t *testing.T) {
	ok := []Config{
		{Endpoint: "https://cp:8443/internal/usage",
			TLS: TLSConfig{CAFile: "ca", CertFile: "c", KeyFile: "k"}},
		{Endpoint: "http://cp:8443/internal/usage", TLS: TLSConfig{Insecure: true}},
	}
	for _, cfg := range ok {
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v", cfg, err)
		}
	}

	bad := []Config{
		{},
		// The dangerous case: plaintext without saying so out loud would send
		// metering traffic, and the org ids in it, in the clear.
		{Endpoint: "http://cp/usage"},
		{Endpoint: "https://cp/usage"},
		{Endpoint: "ftp://cp/usage", TLS: TLSConfig{Insecure: true}},
		{Endpoint: "http://cp/usage", TLS: TLSConfig{Insecure: true}, Timeout: -time.Second},
	}
	for _, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate(%+v) accepted an unusable config", cfg)
		}
	}
}

// A report the control plane rejects is dropped, not retried: re-queueing risks
// counting a customer's volume twice.
func TestRejectedReportIsNotRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := newCounter(t, srv.URL)
	if _, err := c.countMetrics(tenantCtx(orgA), metricsBatch("pod-1")); err != nil {
		t.Fatal(err)
	}
	c.meter.flush(context.Background())
	c.meter.flush(context.Background())

	if calls != 1 {
		t.Errorf("control plane calls = %d, want 1 (no retry)", calls)
	}
}

func TestEndpointMustNotBeEmptyAfterDefaults(t *testing.T) {
	cfg := Config{Endpoint: "http://cp/usage", TLS: TLSConfig{Insecure: true}}.withDefaults()
	if cfg.FlushInterval != DefaultFlushInterval || cfg.MaxHosts != DefaultMaxHosts {
		t.Errorf("defaults = %+v", cfg)
	}
	if !strings.HasPrefix(cfg.Endpoint, "http") {
		t.Errorf("endpoint = %q", cfg.Endpoint)
	}
}
