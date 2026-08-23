package klarousage

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/collector/client"
	"go.uber.org/zap"

	"github.com/klaro/observability/collector/klaroauth"
)

// Resource attributes read for host identity. service.instance.id is the
// normalised host identity the SDK sets (design HOW-4); the other two decorate
// it.
const (
	attrInstanceID  = "service.instance.id"
	attrServiceName = "service.name"
	attrEnvironment = "deployment.environment"
)

// signalUsage is the volume counted for one signal.
type signalUsage struct {
	Bytes int64 `json:"bytes"`
	Items int64 `json:"items"`
}

// host is one reporting instance.
type host struct {
	Ident   string `json:"host_ident"`
	Service string `json:"service,omitempty"`
	Env     string `json:"env,omitempty"`
}

// report is the control plane payload.
type report struct {
	OrgID   string                 `json:"org_id"`
	Signals map[string]signalUsage `json:"signals"`
	Hosts   []host                 `json:"hosts,omitempty"`
}

// orgCounters accumulates one org's usage between flushes.
type orgCounters struct {
	signals map[string]signalUsage
	hosts   map[string]host
}

// meter is the counting state.
//
// One instance exists per pipeline, so three run side by side. They need no
// shared state: each counts a different signal, and host upserts on the control
// plane are idempotent, so the same host arriving three times is one row.
type meter struct {
	cfg    Config
	logger *zap.Logger
	client *http.Client

	mu   sync.Mutex
	orgs map[string]*orgCounters
}

func newMeter(cfg Config, logger *zap.Logger) (*meter, error) {
	cfg = cfg.withDefaults()
	transport, err := transportFor(cfg.TLS)
	if err != nil {
		return nil, err
	}
	return &meter{
		cfg:    cfg,
		logger: logger,
		client: &http.Client{Transport: transport, Timeout: cfg.Timeout},
		orgs:   map[string]*orgCounters{},
	}, nil
}

func transportFor(t TLSConfig) (*http.Transport, error) {
	if t.Insecure {
		return http.DefaultTransport.(*http.Transport).Clone(), nil
	}
	cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("klarousage: load client keypair: %w", err)
	}
	pem, err := os.ReadFile(t.CAFile)
	if err != nil {
		return nil, fmt.Errorf("klarousage: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("klarousage: no CA certificates in %s", t.CAFile)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   t.ServerName,
		MinVersion:   tls.VersionTLS13,
	}
	return tr, nil
}

// orgOf reads the org klaroauth resolved. An unattributed batch cannot be
// metered, and guessing would bill the wrong customer.
func orgOf(ctx context.Context) string {
	md := client.FromContext(ctx).Metadata
	if v := md.Get(klaroauth.MetadataOrgID); len(v) > 0 {
		return v[0]
	}
	return ""
}

// add records volume for one org and signal.
func (m *meter) add(orgID, signal string, size, items int64, hosts []host) {
	if orgID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.orgs[orgID]
	if !ok {
		c = &orgCounters{signals: map[string]signalUsage{}, hosts: map[string]host{}}
		m.orgs[orgID] = c
	}
	u := c.signals[signal]
	u.Bytes += size
	u.Items += items
	c.signals[signal] = u

	for _, h := range hosts {
		if h.Ident == "" || len(c.hosts) >= m.cfg.MaxHosts {
			continue
		}
		c.hosts[h.Ident] = h
	}
}

// drain takes the accumulated counters and resets them.
func (m *meter) drain() []report {
	m.mu.Lock()
	orgs := m.orgs
	m.orgs = map[string]*orgCounters{}
	m.mu.Unlock()

	out := make([]report, 0, len(orgs))
	for orgID, c := range orgs {
		r := report{OrgID: orgID, Signals: c.signals}
		for _, h := range c.hosts {
			r.Hosts = append(r.Hosts, h)
		}
		out = append(out, r)
	}
	return out
}

// flush reports every org's counters.
//
// A failed report is dropped, not retried: the counters were already taken, and
// re-queueing them risks counting a customer's volume twice. Under-reporting one
// flush is recoverable; over-reporting is a refund.
func (m *meter) flush(ctx context.Context) {
	for _, r := range m.drain() {
		payload, err := json.Marshal(r)
		if err != nil {
			m.logger.Warn("klarousage: encode report", zap.Error(err))
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.Endpoint, bytes.NewReader(payload))
		if err != nil {
			m.logger.Warn("klarousage: build request", zap.Error(err))
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := m.client.Do(req)
		if err != nil {
			m.logger.Warn("klarousage: report failed", zap.String("org", r.OrgID), zap.Error(err))
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			m.logger.Warn("klarousage: report rejected",
				zap.String("org", r.OrgID), zap.String("status", resp.Status))
		}
	}
}

// run flushes on an interval until the context ends.
func (m *meter) run(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.flush(ctx)
		}
	}
}
