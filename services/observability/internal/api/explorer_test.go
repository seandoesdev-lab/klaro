package api

import (
	"net/http"
	"testing"
	"time"
)

// explorerRoutes are every read endpoint added for OBS-03/04/05. Each must sit
// inside the org group: one mounted outside it would proxy another tenant's
// telemetry to whoever asked.
var explorerRoutes = []string{
	"/obs/metrics/query?metric=cpu",
	"/obs/traces",
	"/obs/traces/5b8efff798038103d269b633813fc60c",
	"/obs/traces/5b8efff798038103d269b633813fc60c/correlated",
	"/obs/logs",
}

func TestExplorerRoutesRequireACredential(t *testing.T) {
	r := NewRouter(deps())
	for _, route := range explorerRoutes {
		w := get(r, "/orgs/"+orgA+route, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a credential = %d, want 401 (body %s)", route, w.Code, w.Body)
		}
	}
}

// Authenticated for orgA, addressing orgB. The guard must stop it before a
// handler builds any upstream query at all.
func TestExplorerRoutesRejectCrossTenant(t *testing.T) {
	r := NewRouter(deps())
	for _, route := range explorerRoutes {
		w := get(r, "/orgs/"+orgB+route, "Bearer dev")
		if w.Code != http.StatusForbidden {
			t.Errorf("GET %s cross-tenant = %d, want 403 (body %s)", route, w.Code, w.Body)
		}
	}
}

func TestParseTimeAcceptsTheThreeCallerDialects(t *testing.T) {
	want := time.Unix(1756000000, 0)
	cases := map[string]time.Time{
		"2025-08-24T01:46:40Z": want,
		"1756000000":           want,
		"1756000000000":        want,
	}
	for raw, expect := range cases {
		got, ok := parseTime(raw)
		if !ok {
			t.Fatalf("parseTime(%q) rejected", raw)
		}
		if !got.Equal(expect) {
			t.Errorf("parseTime(%q) = %v, want %v", raw, got.UTC(), expect.UTC())
		}
	}
	for _, bad := range []string{"", "yesterday", "12.5", "-"} {
		if _, ok := parseTime(bad); ok {
			t.Errorf("parseTime(%q) was accepted", bad)
		}
	}
}

// A reserved label in a filter must be refused at the edge, with the shared
// envelope, rather than reaching the query builder and being silently ignored.
func TestExplorerRejectsReservedFilterLabels(t *testing.T) {
	r := NewRouter(deps())
	w := get(r, "/orgs/"+orgA+"/obs/logs?filter=klaro_org_id%3D"+orgB, "Bearer dev")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
}

func TestExplorerRejectsUnparseableTimeRange(t *testing.T) {
	r := NewRouter(deps())
	w := get(r, "/orgs/"+orgA+"/obs/metrics/query?metric=cpu&from=yesterday", "Bearer dev")
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
}

// With no backend configured the deployment says so as a gateway failure, not
// as a generic 500 that looks like a control plane bug.
func TestExplorerReportsAMissingBackendAsBadGateway(t *testing.T) {
	r := NewRouter(deps())
	w := get(r, "/orgs/"+orgA+"/obs/metrics/query?metric=cpu", "Bearer dev")
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (body %s)", w.Code, w.Body)
	}
}

// The correlated route reads three backends behind one URL, so its parameter
// validation is worth pinning at the edge: a bad value must be a 422 with the
// shared envelope, not an upstream call with a nonsense window.
func TestCorrelatedRouteValidatesItsParameters(t *testing.T) {
	r := NewRouter(deps())
	const trace = "5b8efff798038103d269b633813fc60c"

	bad := []string{
		"/obs/traces/" + trace + "/correlated?pad_sec=-1",
		"/obs/traces/" + trace + "/correlated?log_limit=nope",
		"/obs/traces/" + trace + "/correlated?step=-5",
		// Past MaxCorrelationPad: the window of this endpoint is one trace.
		"/obs/traces/" + trace + "/correlated?pad_sec=100000",
		// A metric name is interpolated into MetricsQL.
		"/obs/traces/" + trace + `/correlated?metric=cpu%22%7D+or+up%7B`,
	}
	for _, route := range bad {
		w := get(r, "/orgs/"+orgA+route, "Bearer dev")
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("GET %s = %d, want 422 (body %s)", route, w.Code, w.Body)
		}
	}
}

// A trace id that is not hex reaches an upstream URL path if it is not stopped
// here, so the route rejects it before any backend is dialled.
func TestCorrelatedRouteRejectsANonHexTraceID(t *testing.T) {
	r := NewRouter(deps())
	w := get(r, "/orgs/"+orgA+"/obs/traces/not-a-trace/correlated", "Bearer dev")
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
}
