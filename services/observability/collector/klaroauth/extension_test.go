package klaroauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/collector/client"
	"go.uber.org/zap"
)

const testOrg = "00000000-0000-0000-0000-0000000000aa"

// testInternalToken stands in for the shared secret this gateway presents to
// the control plane's internal plane. It is required whenever the hop runs
// without a client certificate.
const testInternalToken = "internal-token-for-tests-0123456789"

// The tenant a header-forging client would try to write into.
const (
	evilOrg     = "00000000-0000-0000-0000-0000000000ee"
	evilAccount = "666"
)

// grantServer stands in for the control plane. It counts calls so the tests can
// tell a cache hit from a round trip.
func grantServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

const okGrant = `{"org_id":"00000000-0000-0000-0000-0000000000aa","key_id":"00000000-0000-0000-0000-0000000000c1",
"vm_account_id":7,"vm_tenant_path":"7","x_scope_org_id":"00000000-0000-0000-0000-0000000000aa",
"quota":{"active_hosts":3,"ingest_gb":1.5,"overage":false},"cache_ttl_sec":30}`

func newTestExtension(t *testing.T, endpoint string, tune func(*Config)) *authExtension {
	t.Helper()
	cfg := Config{Endpoint: endpoint, TLS: TLSConfig{Insecure: true}, InternalToken: testInternalToken}
	if tune != nil {
		tune(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	a, err := newExtension(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The gateway has to authenticate itself, not just forward the customer's key:
// the control plane's internal plane refuses an unauthenticated caller whether
// or not the transport is encrypted (F-3).
func TestGatewayPresentsItsInternalTokenToTheControlPlane(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okGrant))
	}))
	defer srv.Close()

	a := newTestExtension(t, srv.URL, nil)
	if _, err := a.resolve(context.Background(), "obsk_whatever"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "Bearer "+testInternalToken {
		t.Errorf("Authorization = %q, want the internal token", got)
	}
}

// A 401 on this endpoint is ambiguous: the customer's key may be bad, or this
// gateway's own credential may be. Mistaking the second for the first would
// negatively cache a perfectly good key and read, in the logs, as every
// customer's key being revoked at once.
func TestGatewayCredentialRejectionIsNotReportedAsABadIngestKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="klaro-internal"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	a := newTestExtension(t, srv.URL, nil)
	_, err := a.resolve(context.Background(), "obsk_a-good-key")
	if err == nil {
		t.Fatal("a rejected gateway credential must still be an error")
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Errorf("gateway rejection reported as a bad ingest key: %v", err)
	}
	// Not cached either, or a fixed token would keep a whole fleet rejected
	// past the point where it is corrected.
	if _, ok := a.cached(cacheKey("obsk_a-good-key")); ok {
		t.Error("the rejection was negatively cached")
	}
}

func sources(key string) map[string][]string {
	return map[string][]string{DefaultHeader: {key}, "user-agent": {"otel-sdk"}}
}

func TestAuthenticateResolvesTenantOntoTheContext(t *testing.T) {
	srv, calls := grantServer(t, http.StatusOK, okGrant)
	a := newTestExtension(t, srv.URL, nil)

	ctx, err := a.Authenticate(context.Background(), sources("obsk_whatever"))
	if err != nil {
		t.Fatal(err)
	}

	md := client.FromContext(ctx).Metadata
	if got := md.Get(MetadataScopeOrgID); len(got) != 1 || got[0] != testOrg {
		t.Errorf("%s = %v, want the org uuid", MetadataScopeOrgID, got)
	}
	if got := md.Get(MetadataVMAccountID); len(got) != 1 || got[0] != "7" {
		t.Errorf("%s = %v, want 7", MetadataVMAccountID, got)
	}
	if got := md.Get(MetadataOrgID); len(got) != 1 || got[0] != testOrg {
		t.Errorf("%s = %v", MetadataOrgID, got)
	}

	// Unrelated request metadata survives...
	if got := md.Get("user-agent"); len(got) != 1 {
		t.Errorf("user-agent was dropped: %v", got)
	}
	// ...but the secret must not travel further into the pipeline, where an
	// exporter forwarding metadata would ship it to a storage backend.
	if got := md.Get(DefaultHeader); len(got) != 0 {
		t.Errorf("the ingest key is still on the context: %v", got)
	}

	auth := client.FromContext(ctx).Auth
	if auth == nil || auth.GetAttribute("org_id") != testOrg {
		t.Errorf("auth data = %v", auth)
	}
	if calls.Load() != 1 {
		t.Errorf("control plane calls = %d, want 1", calls.Load())
	}
}

func TestAuthenticateRequiresTheHeader(t *testing.T) {
	srv, calls := grantServer(t, http.StatusOK, okGrant)
	a := newTestExtension(t, srv.URL, nil)

	if _, err := a.Authenticate(context.Background(), map[string][]string{}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
	// A missing header must not cost a control plane round trip.
	if calls.Load() != 0 {
		t.Errorf("control plane calls = %d, want 0", calls.Load())
	}
}

func TestAuthenticateRejectsAndRemembersRejection(t *testing.T) {
	srv, calls := grantServer(t, http.StatusUnauthorized, `{"error":{"code":"UNAUTHENTICATED"}}`)
	a := newTestExtension(t, srv.URL, nil)

	for i := 0; i < 3; i++ {
		if _, err := a.Authenticate(context.Background(), sources("obsk_dead")); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("attempt %d: err = %v, want ErrUnauthorized", i, err)
		}
	}
	// A fleet retrying with a dead key must not turn into a request flood.
	if calls.Load() != 1 {
		t.Errorf("control plane calls = %d, want 1 (negative cache)", calls.Load())
	}
}

func TestSuccessfulGrantsAreCached(t *testing.T) {
	srv, calls := grantServer(t, http.StatusOK, okGrant)
	a := newTestExtension(t, srv.URL, nil)

	for i := 0; i < 5; i++ {
		if _, err := a.Authenticate(context.Background(), sources("obsk_live")); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("control plane calls = %d, want 1", calls.Load())
	}

	// Once the TTL passes the key must be re-checked, which is what bounds how
	// long a revoked key keeps working.
	a.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := a.Authenticate(context.Background(), sources("obsk_live")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("control plane calls after expiry = %d, want 2", calls.Load())
	}
}

// A control plane outage must not look like a bad key: the batch should fail and
// be retried, not be remembered as rejected.
func TestServerErrorsAreNotCachedAsRejections(t *testing.T) {
	srv, calls := grantServer(t, http.StatusInternalServerError, `{}`)
	a := newTestExtension(t, srv.URL, nil)

	for i := 0; i < 3; i++ {
		_, err := a.Authenticate(context.Background(), sources("obsk_live"))
		if err == nil {
			t.Fatal("a 500 was treated as success")
		}
		if errors.Is(err, ErrUnauthorized) {
			t.Fatal("a control plane outage was reported as an invalid key")
		}
	}
	if calls.Load() != 3 {
		t.Errorf("control plane calls = %d, want 3 (no caching of outages)", calls.Load())
	}
}

// AccountID 0 is the VictoriaMetrics default account. Accepting a grant that
// carries it would route the org into a bucket shared with everyone else.
func TestGrantWithoutATenantIsRefused(t *testing.T) {
	for _, body := range []string{
		`{"org_id":"00000000-0000-0000-0000-0000000000aa","vm_account_id":0}`,
		`{"org_id":"","vm_account_id":7}`,
	} {
		srv, _ := grantServer(t, http.StatusOK, body)
		a := newTestExtension(t, srv.URL, nil)
		if _, err := a.Authenticate(context.Background(), sources("obsk_live")); err == nil {
			t.Errorf("grant %s was accepted", body)
		}
	}
}

// The control plane hint may shorten the cache but never extend it past the
// configured ceiling - otherwise it could stretch how long a revoked key lives.
func TestGrantTTLIsCappedByConfig(t *testing.T) {
	a := newTestExtension(t, "http://cp/internal/authz/ingest-key", func(c *Config) {
		c.CacheTTL = 10 * time.Second
	})
	if got := a.grantTTL(grant{CacheTTLSec: 600}); got != 10*time.Second {
		t.Errorf("ttl = %v, want the 10s ceiling", got)
	}
	if got := a.grantTTL(grant{CacheTTLSec: 2}); got != 2*time.Second {
		t.Errorf("ttl = %v, want the 2s hint", got)
	}
	if got := a.grantTTL(grant{}); got != 10*time.Second {
		t.Errorf("ttl with no hint = %v, want the configured value", got)
	}
}

// The cache is keyed by a digest so a heap dump of the gateway does not hand
// over working ingest credentials.
func TestCacheKeyIsNotTheSecret(t *testing.T) {
	secret := "obsk_super-secret-value"
	k := cacheKey(secret)
	if strings.Contains(k, "super-secret") {
		t.Error("cache key contains the secret")
	}
	if k == cacheKey(secret+"x") {
		t.Error("distinct secrets share a cache key")
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"mTLS endpoint", Config{
			Endpoint: "https://cp:8443/internal/authz/ingest-key",
			TLS:      TLSConfig{CAFile: "ca.pem", CertFile: "c.pem", KeyFile: "k.pem"},
		}, true},
		{"explicit dev plaintext", Config{
			Endpoint:      "http://cp:8443/internal/authz/ingest-key",
			TLS:           TLSConfig{Insecure: true},
			InternalToken: testInternalToken,
		}, true},
		{"no endpoint", Config{TLS: TLSConfig{Insecure: true}, InternalToken: testInternalToken}, false},
		// The dangerous case: plaintext without saying so out loud would send
		// ingest secrets in the clear.
		{"plaintext without opting in", Config{Endpoint: "http://cp/x"}, false},
		// Plaintext transport skips encryption, not authentication: without a
		// client certificate the token is the only thing identifying this
		// gateway, and the control plane will refuse it (F-3).
		{"plaintext without a credential", Config{
			Endpoint: "http://cp:8443/internal/authz/ingest-key",
			TLS:      TLSConfig{Insecure: true},
		}, false},
		{"both token forms", Config{
			Endpoint:          "http://cp:8443/internal/authz/ingest-key",
			TLS:               TLSConfig{Insecure: true},
			InternalToken:     testInternalToken,
			InternalTokenFile: "/run/secrets/token",
		}, false},
		{"https without a client bundle", Config{Endpoint: "https://cp/x"}, false},
		{"negative ttl", Config{
			Endpoint: "https://cp/x",
			TLS:      TLSConfig{CAFile: "ca", CertFile: "c", KeyFile: "k"},
			CacheTTL: -time.Second,
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Errorf("Validate = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Error("Validate accepted an unusable config")
			}
		})
	}
}

// The gRPC receiver lowercases metadata keys; the HTTP receiver hands over
// net/http canonical keys. A case-sensitive lookup would work over gRPC and
// reject every HTTP request, which is exactly the bug this guards.
func TestHeaderLookupIsCaseInsensitive(t *testing.T) {
	srv, _ := grantServer(t, http.StatusOK, okGrant)
	a := newTestExtension(t, srv.URL, nil)

	for _, spelling := range []string{"klaro-obs-key", "Klaro-Obs-Key", "KLARO-OBS-KEY"} {
		ctx, err := a.Authenticate(context.Background(), map[string][]string{spelling: {"obsk_live"}})
		if err != nil {
			t.Fatalf("header %q: %v", spelling, err)
		}
		// Whichever spelling arrived, the secret must not continue downstream.
		md := client.FromContext(ctx).Metadata
		if got := md.Get(spelling); len(got) != 0 {
			t.Errorf("header %q survived on the context: %v", spelling, got)
		}
	}
}

// A client must not be able to name its own tenant. This is the metadata
// equivalent of the label forgery klarotenant already blocks: the collector
// stores request metadata case-sensitively but client.NewMetadata lowercases
// every key on the way in, so a forged Klaro-Vm-Account-Id used to survive
// next to the authoritative klaro-vm-account-id and the two collapsed into one
// entry whose winner Go's map iteration order picked. Losing that coin flip
// routes the batch to the org the client asked for.
//
// The loop is what makes the check bite: one iteration could pass by luck, and
// map order is randomised per range.
func TestForgedTenantHeadersAreIgnored(t *testing.T) {
	srv, _ := grantServer(t, http.StatusOK, okGrant)
	a := newTestExtension(t, srv.URL, nil)

	// Every casing a client could reach for, including the ones the two
	// receivers themselves produce (lowercase over gRPC, canonical over HTTP).
	forged := map[string]string{
		"x-scope-orgid":       evilOrg,
		"X-Scope-OrgID":       evilOrg,
		"X-SCOPE-ORGID":       evilOrg,
		"klaro-vm-account-id": evilAccount,
		"Klaro-Vm-Account-Id": evilAccount,
		"KLARO-VM-ACCOUNT-ID": evilAccount,
		"klaro-org-id":        evilOrg,
		"Klaro-Org-Id":        evilOrg,
		"klaro-quota-overage": "true",
		"Klaro-Quota-Overage": "true",
	}
	want := map[string]string{
		MetadataScopeOrgID:   testOrg,
		MetadataVMAccountID:  "7",
		MetadataOrgID:        testOrg,
		MetadataQuotaOverage: "false",
	}

	for i := 0; i < 64; i++ {
		src := sources("obsk_live")
		for k, v := range forged {
			src[k] = []string{v}
		}

		ctx, err := a.Authenticate(context.Background(), src)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		md := client.FromContext(ctx).Metadata

		for key, expected := range want {
			got := md.Get(key)
			if len(got) != 1 || got[0] != expected {
				t.Fatalf("iteration %d: %s = %v, want [%s] - the client's value won",
					i, key, got, expected)
			}
		}
		// Nothing the client sent may linger anywhere on the context, under any
		// key: a stray copy is a value some later component could read.
		for key := range md.Keys() {
			for _, v := range md.Get(key) {
				if v == evilOrg || v == evilAccount {
					t.Fatalf("iteration %d: forged value %q survived under %s", i, v, key)
				}
			}
		}
	}
}

// deleteFold is the whole defence, so pin its contract directly.
func TestDeleteFoldRemovesEverySpelling(t *testing.T) {
	md := map[string][]string{
		"klaro-vm-account-id": {"1"},
		"Klaro-Vm-Account-Id": {"2"},
		"KLARO-VM-ACCOUNT-ID": {"3"},
		"user-agent":          {"otel-sdk"},
	}
	deleteFold(md, MetadataVMAccountID)

	if len(md) != 1 || len(md["user-agent"]) != 1 {
		t.Errorf("md = %v, want only user-agent left", md)
	}
}
