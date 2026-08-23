package api

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klaro/observability/internal/platform/httpx"
)

// The internal plane is where ingest keys are resolved, live frames are
// published for arbitrary orgs, usage is written and alert events are injected.
// Plaintext transport is a development convenience; unauthenticated access is
// not, and these tests are what keep the two from being the same switch again
// (finding F-3).

// internalRoutes are the routes that must never answer an unauthenticated
// caller. /healthz is deliberately absent: it is a liveness probe.
var internalRoutes = []struct{ method, path, body string }{
	{http.MethodPost, "/internal/authz/ingest-key", `{"key":"obsk_x"}`},
	{http.MethodPost, "/internal/live-ingest", `{"org_id":"` + orgA + `","stream":"metric","points":[]}`},
	{http.MethodPost, "/internal/alerts/webhook", `[]`},
	{http.MethodPost, "/internal/alerts/api/v2/alerts", `[]`},
	{http.MethodPost, "/internal/usage", `{"org_id":"` + orgA + `"}`},
	{http.MethodPost, "/internal/snapshot", `{}`},
}

// liveBody is a valid smallest live batch, used wherever the test cares about
// the credential rather than the payload.
func liveBody() string {
	return `{"org_id":"` + orgA + `","stream":"metric","points":[]}`
}

func internalRequest(d InternalDeps, method, path, body, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)
	return w
}

func TestInternalPlaneRejectsAnUnauthenticatedCaller(t *testing.T) {
	for _, r := range internalRoutes {
		t.Run(r.path, func(t *testing.T) {
			d, _ := internalDeps()
			w := internalRequest(d, r.method, r.path, r.body, "")
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body)
			}
			var envelope httpx.Body
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != httpx.CodeUnauthenticated {
				t.Errorf("code = %q", envelope.Error.Code)
			}
		})
	}
}

func TestInternalPlaneRejectsAWrongToken(t *testing.T) {
	for _, header := range []string{
		"Bearer wrong-token",
		"Bearer ",
		"Basic " + testInternalToken,
		testInternalToken, // no scheme
		// A prefix of the real token must not pass: the comparison covers the
		// whole value, not a starts-with.
		"Bearer " + testInternalToken[:len(testInternalToken)-1],
	} {
		d, _ := internalDeps()
		w := internalRequest(d, http.MethodPost, "/internal/live-ingest", liveBody(), header)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Authorization=%q: status = %d, want 401", header, w.Code)
		}
	}
}

func TestInternalPlaneAcceptsTheConfiguredToken(t *testing.T) {
	d, _ := internalDeps()
	w := internalRequest(d, http.MethodPost, "/internal/live-ingest", liveBody(),
		"Bearer "+testInternalToken)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body)
	}
}

// An unset token authenticates nobody. A deployment that forgot the secret must
// serve a closed door, not an open one - which is the whole point of splitting
// authentication from transport.
func TestInternalPlaneWithNoTokenConfiguredRefusesEveryone(t *testing.T) {
	for _, header := range []string{"", "Bearer ", "Bearer anything"} {
		d, _ := internalDeps()
		d.Token = ""
		w := internalRequest(d, http.MethodPost, "/internal/live-ingest", liveBody(), header)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Authorization=%q: status = %d, want 401 (body %s)", header, w.Code, w.Body)
		}
	}
}

// mTLS is the other accepted credential: a verified client chain is itself the
// authentication, so a Collector presenting one needs no token. VerifiedChains
// is only ever non-empty when the TLS stack verified the peer against the
// configured CA (mtls.ServerConfig uses RequireAndVerifyClientCert).
func TestInternalPlaneAcceptsAVerifiedClientCertificateWithoutAToken(t *testing.T) {
	d, _ := internalDeps()
	d.Token = ""

	req := httptest.NewRequest(http.MethodPost, "/internal/live-ingest", strings.NewReader(liveBody()))
	req.Header.Set("Content-Type", "application/json")
	req.TLS = &tls.ConnectionState{
		HandshakeComplete: true,
		VerifiedChains: [][]*x509.Certificate{{
			{Subject: pkix.Name{CommonName: "klaro-collector"}},
		}},
	}

	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body)
	}
}

// A TLS connection whose peer presented nothing verifiable is not a credential:
// an empty VerifiedChains must not be mistaken for "mTLS was used".
func TestInternalPlaneRejectsTLSWithoutAVerifiedChain(t *testing.T) {
	d, _ := internalDeps()
	d.Token = ""

	req := httptest.NewRequest(http.MethodPost, "/internal/live-ingest", strings.NewReader(liveBody()))
	req.TLS = &tls.ConnectionState{HandshakeComplete: true}

	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body)
	}
}

// Liveness has to stay reachable, or a deployment cannot be health-checked by
// an orchestrator that holds no credential.
func TestInternalHealthzStaysOpen(t *testing.T) {
	d, _ := internalDeps()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
	}
}
