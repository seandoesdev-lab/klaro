package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/klaro/observability/internal/ingestkey"
	"github.com/klaro/observability/internal/platform/redisx"
)

// keyModelJSONFields renders the json tags of the stored key model, so a test
// can assert what the API is even capable of serialising.
func keyModelJSONFields() string {
	t := reflect.TypeOf(ingestkey.Key{})
	var b strings.Builder
	for i := 0; i < t.NumField(); i++ {
		b.WriteString(t.Field(i).Tag.Get("json"))
		b.WriteString(" ")
	}
	return b.String()
}

func internalDeps() (InternalDeps, *redisx.Memory) {
	mem := redisx.NewMemory()
	// A nil *db.DB suffices for the paths under test: each must reject its input
	// before any query, and reaching one would panic.
	return InternalDeps{
		Authz:  ingestkey.NewAuthorizer(nil, ingestkey.NewStore(nil), nil, ingestkey.AuthorizerOptions{}),
		Signal: mem,
	}, mem
}

func postInternal(path, body string) (*httptest.ResponseRecorder, *redisx.Memory) {
	d, mem := internalDeps()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)
	return w, mem
}

// Unknown, revoked, expired-grace and malformed must all look identical from
// outside, or this endpoint becomes an oracle for which keys exist.
func TestAuthzRejectsUnknownKeyAsUnauthenticated(t *testing.T) {
	w, _ := postInternal("/internal/authz/ingest-key", `{"key":"obsk_definitely-not-a-real-key"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "revoked") {
		t.Errorf("response discloses why the key failed: %s", w.Body)
	}
}

func TestAuthzRequiresAKey(t *testing.T) {
	w, _ := postInternal("/internal/authz/ingest-key", `{}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
}

// The Collector forwards the header it received; the body form is for tests and
// curl. Both must be accepted. A 401 here (not 422) proves the header was read.
func TestAuthzAcceptsTheHeaderForm(t *testing.T) {
	d, _ := internalDeps()
	req := httptest.NewRequest(http.MethodPost, "/internal/authz/ingest-key", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(IngestKeyHeader, "obsk_definitely-not-a-real-key")
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body)
	}
}

func TestLiveIngestPublishesOnTheOrgChannel(t *testing.T) {
	d, mem := internalDeps()

	ch, cancel, err := mem.SubscribeLive(t.Context(), orgA, "metric")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	body := `{"org_id":"` + orgA + `","stream":"metric","ts":1756000000,` +
		`"points":[{"labels":{"service":"api"},"value":1.5}]}`
	req := httptest.NewRequest(http.MethodPost, "/internal/live-ingest", strings.NewReader(body))
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)

	// Accepted, not OK: pub/sub is fire-and-forget by design (HOW-7).
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body)
	}

	select {
	case got := <-ch:
		var decoded map[string]any
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatalf("published payload is not JSON: %v", err)
		}
		if decoded["org_id"] != orgA {
			t.Errorf("published org_id = %v", decoded["org_id"])
		}
	default:
		t.Fatal("nothing was published on the org channel")
	}
}

// A subscriber for one org must never see the batch of another. The channel name
// carries the org, so the isolation is structural rather than filtered after the
// fact.
func TestLiveIngestDoesNotCrossOrgs(t *testing.T) {
	d, mem := internalDeps()

	ch, cancel, err := mem.SubscribeLive(t.Context(), orgB, "metric")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	body := `{"org_id":"` + orgA + `","stream":"metric","points":[]}`
	req := httptest.NewRequest(http.MethodPost, "/internal/live-ingest", strings.NewReader(body))
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	select {
	case got := <-ch:
		t.Fatalf("the orgB subscriber received orgA data: %s", got)
	default:
	}
}

func TestLiveIngestRejectsBadScope(t *testing.T) {
	cases := []struct{ name, body string }{
		{"missing org", `{"stream":"metric"}`},
		{"org is not a uuid", `{"org_id":"orgA","stream":"metric"}`},
		{"missing stream", `{"org_id":"` + orgA + `"}`},
		// A ':' would let the caller address a channel outside its grant.
		{"stream escapes the channel", `{"org_id":"` + orgA + `","stream":"metric:other"}`},
		{"stream too long", `{"org_id":"` + orgA + `","stream":"` + strings.Repeat("m", 33) + `"}`},
		{"not an object", `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := postInternal("/internal/live-ingest", tc.body)
			if w.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422 (body %s)", w.Code, w.Body)
			}
		})
	}
}

// The Collector exports OTLP/JSON, and one export can carry several tenants
// because the gateway batches across them. Each resource group must be
// published only to the org stamped on it - publishing the whole payload to one
// org would hand it every other tenant in the batch.
func TestLiveIngestSplitsOTLPByOrg(t *testing.T) {
	d, mem := internalDeps()

	chA, cancelA, err := mem.SubscribeLive(t.Context(), orgA, "metric")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelA()
	chB, cancelB, err := mem.SubscribeLive(t.Context(), orgB, "metric")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelB()

	body := `{"resourceMetrics":[
	  {"resource":{"attributes":[{"key":"klaro.org_id","value":{"stringValue":"` + orgA + `"}},
	                             {"key":"service.name","value":{"stringValue":"checkout"}}]},
	   "scopeMetrics":[{"metrics":[{"name":"http.server.duration"}]}]},
	  {"resource":{"attributes":[{"key":"klaro.org_id","value":{"stringValue":"` + orgB + `"}}]},
	   "scopeMetrics":[{"metrics":[{"name":"db.client.duration"}]}]}]}`

	req := httptest.NewRequest(http.MethodPost, "/internal/live-ingest", strings.NewReader(body))
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body)
	}

	for org, ch := range map[string]<-chan []byte{orgA: chA, orgB: chB} {
		select {
		case got := <-ch:
			var decoded struct {
				OrgID string `json:"org_id"`
				Group string `json:"-"`
			}
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatalf("%s: payload is not JSON: %v", org, err)
			}
			if decoded.OrgID != org {
				t.Errorf("%s channel carried org %s", org, decoded.OrgID)
			}
			// The neighbour must not be inside this payload.
			other := orgA
			if org == orgA {
				other = orgB
			}
			if strings.Contains(string(got), other) {
				t.Errorf("%s payload mentions %s: %s", org, other, got)
			}
		default:
			t.Errorf("%s received nothing", org)
		}
	}
}

// A resource with no control-plane-stamped org cannot be attributed to anyone,
// so it is refused rather than guessed at.
func TestLiveIngestRefusesUnattributedOTLP(t *testing.T) {
	body := `{"resourceMetrics":[{"resource":{"attributes":[` +
		`{"key":"service.name","value":{"stringValue":"checkout"}}]}}]}`
	w, _ := postInternal("/internal/live-ingest", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
}

// A client-chosen org attribute that is not a uuid must not reach set_config.
func TestLiveIngestRefusesMalformedOTLPOrg(t *testing.T) {
	body := `{"resourceMetrics":[{"resource":{"attributes":[` +
		`{"key":"klaro.org_id","value":{"stringValue":"not-a-uuid"}}]}}]}`
	w, _ := postInternal("/internal/live-ingest", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
}

// The Collector gzips OTLP/HTTP exports by default. A handler that only read
// raw bytes would accept every hand-written curl and reject every real batch,
// which is exactly the failure this covers.
func TestLiveIngestAcceptsGzippedOTLP(t *testing.T) {
	d, mem := internalDeps()

	ch, cancel, err := mem.SubscribeLive(t.Context(), orgA, "metric")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	plain := `{"resourceMetrics":[{"resource":{"attributes":[` +
		`{"key":"klaro.org_id","value":{"stringValue":"` + orgA + `"}}]}}]}`
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/live-ingest", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	NewInternalRouter(d).ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body)
	}
	select {
	case <-ch:
	default:
		t.Fatal("the gzipped batch was not published")
	}
}
