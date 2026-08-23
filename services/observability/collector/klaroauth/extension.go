package klaroauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension/extensionauth"
	"go.uber.org/zap"
)

// ErrUnauthorized is returned for any key the control plane will not vouch for.
// Like the control plane, the gateway gives one answer for every reason.
var ErrUnauthorized = errors.New("klaroauth: ingest key is not authorized")

// errNoKey is returned when the request carried no key header at all.
var errNoKey = fmt.Errorf("%w: missing ingest key header", ErrUnauthorized)

type cacheEntry struct {
	g       grant
	err     error
	expires time.Time
}

type authExtension struct {
	cfg    Config
	logger *zap.Logger
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

func newExtension(cfg Config, logger *zap.Logger) (*authExtension, error) {
	cfg = cfg.withDefaults()
	transport, err := transportFor(cfg.TLS)
	if err != nil {
		return nil, err
	}
	return &authExtension{
		cfg:     cfg,
		logger:  logger,
		client:  &http.Client{Transport: transport, Timeout: cfg.Timeout},
		now:     time.Now,
		entries: map[string]cacheEntry{},
	}, nil
}

func transportFor(t TLSConfig) (*http.Transport, error) {
	if t.Insecure {
		return http.DefaultTransport.(*http.Transport).Clone(), nil
	}
	cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("klaroauth: load client keypair: %w", err)
	}
	pem, err := os.ReadFile(t.CAFile)
	if err != nil {
		return nil, fmt.Errorf("klaroauth: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("klaroauth: no CA certificates in %s", t.CAFile)
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

// Start implements extension.Extension.
func (a *authExtension) Start(context.Context, component.Host) error { return nil }

// Shutdown implements extension.Extension.
func (a *authExtension) Shutdown(context.Context) error {
	a.client.CloseIdleConnections()
	return nil
}

// Authenticate implements extensionauth.Server.
//
// sources is the request header map with lowercased keys. On success the
// returned context carries the resolved tenant, both as client.Info.Auth and as
// metadata, because the two are read by different downstream components.
func (a *authExtension) Authenticate(ctx context.Context, sources map[string][]string) (context.Context, error) {
	secret := firstValue(sources, a.cfg.Header)
	if secret == "" {
		return ctx, errNoKey
	}

	g, err := a.resolve(ctx, secret)
	if err != nil {
		return ctx, err
	}
	return withTenant(ctx, g, sources, a.cfg.Header), nil
}

// firstValue looks the header up without regard to case.
//
// The two receivers disagree: gRPC metadata keys arrive lowercased, while the
// HTTP receiver hands over net/http canonical keys (Klaro-Obs-Key). A
// case-sensitive lookup silently works over gRPC and rejects every HTTP
// request, so the comparison has to fold.
func firstValue(sources map[string][]string, key string) string {
	if v := sources[key]; len(v) > 0 {
		return v[0]
	}
	for k, v := range sources {
		if len(v) > 0 && strings.EqualFold(k, key) {
			return v[0]
		}
	}
	return ""
}

// deleteFold removes every spelling of key from md, for the same reason.
func deleteFold(md map[string][]string, key string) {
	for k := range md {
		if strings.EqualFold(k, key) {
			delete(md, k)
		}
	}
}

// withTenant puts the control-plane-issued identity on the request context.
//
// The metadata is rebuilt from the request headers the receiver handed us
// rather than only from any client.Info already present, so the identity
// propagates even when the receiver was not configured with
// include_metadata - one less way to end up with unlabelled telemetry.
//
// The key header itself is dropped: nothing downstream needs the secret, and an
// exporter that forwarded request metadata would otherwise ship it to a storage
// backend.
func withTenant(ctx context.Context, g grant, sources map[string][]string, keyHeader string) context.Context {
	info := client.FromContext(ctx)
	md := map[string][]string{}
	for k := range info.Metadata.Keys() {
		md[k] = info.Metadata.Get(k)
	}
	for k, v := range sources {
		md[k] = v
	}
	deleteFold(md, keyHeader)

	md[MetadataScopeOrgID] = []string{g.ScopeOrgID}
	md[MetadataVMAccountID] = []string{strconv.FormatUint(uint64(g.VMAccountID), 10)}
	md[MetadataOrgID] = []string{g.OrgID}
	md[MetadataQuotaOverage] = []string{strconv.FormatBool(g.Quota.Overage)}

	info.Auth = authData{g: g}
	info.Metadata = client.NewMetadata(md)
	return client.NewContext(ctx, info)
}

// cacheKey never stores the secret, only its digest, so a heap dump of the
// gateway does not hand over working ingest credentials.
func cacheKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// resolve answers from cache when it can, and asks the control plane otherwise.
func (a *authExtension) resolve(ctx context.Context, secret string) (grant, error) {
	key := cacheKey(secret)
	if e, ok := a.cached(key); ok {
		return e.g, e.err
	}

	g, err := a.ask(ctx, secret)
	switch {
	case err == nil:
		a.store(key, cacheEntry{g: g, expires: a.now().Add(a.grantTTL(g))})
	case errors.Is(err, ErrUnauthorized):
		// Remember the rejection briefly. A fleet misconfigured with a dead key
		// retries forever, and without this every retry is a control plane call.
		a.store(key, cacheEntry{err: err, expires: a.now().Add(a.cfg.NegativeCacheTTL)})
	default:
		// Transport failures are not cached: the next batch should retry rather
		// than inherit an outage.
		a.logger.Warn("klaroauth: control plane unreachable", zap.Error(err))
	}
	return g, err
}

// grantTTL honours the control plane hint but never exceeds the configured
// ceiling, so a misbehaving control plane cannot extend how long a revoked key
// keeps working.
func (a *authExtension) grantTTL(g grant) time.Duration {
	ttl := a.cfg.CacheTTL
	if g.CacheTTLSec > 0 {
		if hinted := time.Duration(g.CacheTTLSec) * time.Second; hinted < ttl {
			ttl = hinted
		}
	}
	return ttl
}

func (a *authExtension) cached(key string) (cacheEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.entries[key]
	if !ok || !e.expires.After(a.now()) {
		return cacheEntry{}, false
	}
	return e, true
}

func (a *authExtension) store(key string, e cacheEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Drop whatever has expired while we hold the lock. The key space is bounded
	// by the number of live org keys, so this is all the eviction needed.
	for k, old := range a.entries {
		if !old.expires.After(a.now()) {
			delete(a.entries, k)
		}
	}
	a.entries[key] = e
}

// ask performs one authz round trip.
func (a *authExtension) ask(ctx context.Context, secret string) (grant, error) {
	body, err := json.Marshal(map[string]string{"key": secret})
	if err != nil {
		return grant{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return grant{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return grant{}, fmt.Errorf("klaroauth: authz request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return grant{}, ErrUnauthorized
	default:
		// Anything else is the control plane failing, not the key being bad.
		// Treating it as a rejection would drop good telemetry during an outage.
		return grant{}, fmt.Errorf("klaroauth: authz returned %s", resp.Status)
	}

	var g grant
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return grant{}, fmt.Errorf("klaroauth: decode grant: %w", err)
	}
	if g.OrgID == "" || g.VMAccountID == 0 {
		// AccountID 0 is the VictoriaMetrics default account: accepting it would
		// route this org into the shared bucket.
		return grant{}, fmt.Errorf("klaroauth: control plane returned an unusable grant (org %q, account %d)", g.OrgID, g.VMAccountID)
	}
	if g.ScopeOrgID == "" {
		g.ScopeOrgID = g.OrgID
	}
	return g, nil
}

var _ extensionauth.Server = (*authExtension)(nil)
