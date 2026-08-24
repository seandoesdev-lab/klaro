package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klaro/observability/internal/inventory"
	"github.com/klaro/observability/internal/platform/httpx"
)

// fakeInventory stands in for the Postgres-backed service. These tests are
// about routing and scoping, which have to hold without a database, and the org
// it was asked for is recorded so the tenancy guard can be proved rather than
// assumed.
type fakeInventory struct {
	askedFor string
	result   inventory.Result
	err      error
}

func (f *fakeInventory) List(_ context.Context, orgID string) (inventory.Result, error) {
	f.askedFor = orgID
	return f.result, f.err
}

func inventoryDeps(inv HostInventory) Deps {
	d := deps()
	d.Inventory = inv
	return d
}

func oneHost() inventory.Result {
	return inventory.Result{
		Data: []inventory.Host{{
			Registration: inventory.Registration{
				HostIdent:   "host-a",
				Service:     "checkout-api",
				Env:         "prod",
				FirstSeenAt: time.Now().Add(-24 * time.Hour),
				LastSeenAt:  time.Now().Add(-20 * time.Second),
			},
			Status: inventory.StatusUp,
		}},
		ActiveWindowSec:  900,
		Total:            1,
		Active:           1,
		MetricsAvailable: true,
	}
}

// A credential for one org must never reach another org's fleet. The guard runs
// before the handler, so this holds even though the fake would happily answer.
func TestHostsRejectsCrossTenant(t *testing.T) {
	inv := &fakeInventory{result: oneHost()}
	w := get(NewRouter(inventoryDeps(inv)), "/orgs/"+orgB+"/obs/hosts", "Bearer dev")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", w.Code, w.Body)
	}
	if inv.askedFor != "" {
		t.Errorf("the inventory was queried for %q despite the scope mismatch", inv.askedFor)
	}
}

func TestHostsRequiresCredentials(t *testing.T) {
	w := get(NewRouter(inventoryDeps(&fakeInventory{})), "/orgs/"+orgA+"/obs/hosts", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", w.Code, w.Body)
	}
}

// The org the handler passes down is the one the credential resolved to, never
// anything read off the request path by the handler itself.
func TestHostsScopesToTheAuthenticatedOrg(t *testing.T) {
	inv := &fakeInventory{result: oneHost()}
	w := get(NewRouter(inventoryDeps(inv)), "/orgs/"+orgA+"/obs/hosts", "Bearer dev")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if inv.askedFor != orgA {
		t.Errorf("inventory asked for %q, want %q", inv.askedFor, orgA)
	}

	var body inventory.Result
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].HostIdent != "host-a" {
		t.Errorf("body = %s", w.Body)
	}
	if body.ActiveWindowSec != 900 {
		t.Errorf("active_window_sec = %d, want the server's own threshold echoed back",
			body.ActiveWindowSec)
	}
}

// A store failure must not leak the driver error, which can quote SQL and row
// values.
func TestHostsHidesStoreErrors(t *testing.T) {
	inv := &fakeInventory{err: errors.New(`pq: relation "observability_hosts" does not exist`)}
	w := get(NewRouter(inventoryDeps(inv)), "/orgs/"+orgA+"/obs/hosts", "Bearer dev")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body)
	}
	var b httpx.Body
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Error.Code != httpx.CodeInternal {
		t.Errorf("code = %q", b.Error.Code)
	}
	if strings.Contains(b.Error.Message, "observability_hosts") {
		t.Errorf("message %q leaks the underlying error", b.Error.Message)
	}
}

// The objective is validated rather than clamped: a target written as 99
// (meaning percent) must be refused, not silently read as 1.
func TestUptimeRejectsATargetOutsideZeroToOne(t *testing.T) {
	w := get(NewRouter(inventoryDeps(&fakeInventory{})),
		"/orgs/"+orgA+"/obs/slo/uptime?target=99", "Bearer dev")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body)
	}
	var b httpx.Body
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Error.Code != httpx.CodeValidation {
		t.Errorf("code = %q", b.Error.Code)
	}
}

func TestUptimeAndHostSeriesRejectCrossTenant(t *testing.T) {
	r := NewRouter(inventoryDeps(&fakeInventory{}))
	for _, path := range []string{
		"/orgs/" + orgB + "/obs/slo/uptime",
		"/orgs/" + orgB + "/obs/hosts/host-a/metrics",
	} {
		if w := get(r, path, "Bearer dev"); w.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403 (body %s)", path, w.Code, w.Body)
		}
	}
}

// With no metrics backend configured these routes report a backend problem
// (502) rather than pretending the fleet is idle.
func TestHostSeriesReportsAnUnconfiguredBackend(t *testing.T) {
	w := get(NewRouter(inventoryDeps(&fakeInventory{})),
		"/orgs/"+orgA+"/obs/hosts/host-a/metrics", "Bearer dev")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", w.Code, w.Body)
	}
}
