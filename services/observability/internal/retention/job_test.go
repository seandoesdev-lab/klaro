package retention

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klaro/observability/internal/plans"
	"github.com/klaro/observability/internal/tenants"
)

const orgA = "00000000-0000-0000-0000-0000000000aa"

// fakeVM records what the job asked for and answers with a configurable series
// count per window.
type fakeVM struct {
	mu       sync.Mutex
	recent   int
	old      int
	deletes  []url.Values
	seriesQs []url.Values
}

func (f *fakeVM) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.URL.Query()

		if strings.Contains(r.URL.Path, "delete_series") {
			f.deletes = append(f.deletes, q)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		f.seriesQs = append(f.seriesQs, q)

		// The job asks twice: the window it must keep first, then everything
		// before it. The first call is the recent one.
		count := f.old
		if q.Get("start") != "0" && len(f.seriesQs) == 1 {
			count = f.recent
		}
		data := make([]map[string]string, count)
		for i := range data {
			data[i] = map[string]string{"__name__": "cpu"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": data})
	})
}

func jobFor(t *testing.T, vmURL, lokiURL string, dryRun bool) *Job {
	t.Helper()
	j := New(Config{VMSelectURL: vmURL, LokiURL: lokiURL, DryRun: dryRun},
		nil, tenants.NewStaticMapper(map[string]uint32{orgA: 7}))
	j.now = func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }
	return j
}

var freePlan = plans.Plan{
	Code: "free", MetricsRetention: 24 * time.Hour, LogsRetention: 24 * time.Hour,
	TracesRetention: 24 * time.Hour,
}

// An org still sending must never be pruned: delete_series takes no time range,
// so pruning an active org would erase its current data with no way back.
func TestPruneSkipsAnActiveOrg(t *testing.T) {
	vm := &fakeVM{recent: 3, old: 10}
	srv := httptest.NewServer(vm.handler(t))
	t.Cleanup(srv.Close)

	if jobFor(t, srv.URL, "", false).pruneMetrics(context.Background(), orgA, freePlan) {
		t.Error("an active org was pruned")
	}
	if len(vm.deletes) != 0 {
		t.Errorf("delete was called for an active org: %v", vm.deletes)
	}
}

// An org with old data and nothing recent has fully expired, which is the one
// case a matcher-only delete can express safely.
func TestPruneRemovesAnExpiredOrg(t *testing.T) {
	vm := &fakeVM{recent: 0, old: 12}
	srv := httptest.NewServer(vm.handler(t))
	t.Cleanup(srv.Close)

	if !jobFor(t, srv.URL, "", false).pruneMetrics(context.Background(), orgA, freePlan) {
		t.Fatal("an expired org was not pruned")
	}
	if len(vm.deletes) != 1 {
		t.Fatalf("deletes = %v", vm.deletes)
	}
	// The matcher must be scoped to the org, or the job would erase the tenant
	// next door.
	if got := vm.deletes[0].Get("match[]"); !strings.Contains(got, orgA) {
		t.Errorf("delete matcher = %q, want it scoped to the org", got)
	}
}

// Nothing anywhere is not an expiry.
func TestPruneSkipsAnOrgWithNoData(t *testing.T) {
	vm := &fakeVM{recent: 0, old: 0}
	srv := httptest.NewServer(vm.handler(t))
	t.Cleanup(srv.Close)

	if jobFor(t, srv.URL, "", false).pruneMetrics(context.Background(), orgA, freePlan) {
		t.Error("an org with no data was pruned")
	}
	if len(vm.deletes) != 0 {
		t.Errorf("delete was called: %v", vm.deletes)
	}
}

// The first run of a data-deleting job in a new environment should be readable
// before it is trusted.
func TestDryRunDeletesNothing(t *testing.T) {
	vm := &fakeVM{recent: 0, old: 12}
	srv := httptest.NewServer(vm.handler(t))
	t.Cleanup(srv.Close)

	if jobFor(t, srv.URL, "", true).pruneMetrics(context.Background(), orgA, freePlan) {
		t.Error("dry run reported a deletion")
	}
	if len(vm.deletes) != 0 {
		t.Errorf("dry run called delete: %v", vm.deletes)
	}
}

// Loki is the only backend here that can delete a time range, and the request
// has to be tenant-scoped both by header and by selector.
func TestLogDeleteIsScopedAndTimeBounded(t *testing.T) {
	var (
		gotQuery url.Values
		gotScope string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		gotScope = r.Header.Get("X-Scope-OrgID")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	j := jobFor(t, "", srv.URL, false)
	if !j.deleteLogs(context.Background(), orgA, freePlan) {
		t.Fatal("no delete was filed")
	}
	if gotScope != orgA {
		t.Errorf("X-Scope-OrgID = %q, want the org", gotScope)
	}
	if !strings.Contains(gotQuery.Get("query"), orgA) {
		t.Errorf("selector = %q, want it scoped to the org", gotQuery.Get("query"))
	}
	// The end bound is what makes this a retention delete rather than an erase.
	if gotQuery.Get("end") == "" || gotQuery.Get("end") == "0" {
		t.Errorf("end = %q, want the retention cutoff", gotQuery.Get("end"))
	}
}

// A plan with no retention means unlimited, so there is nothing to enforce.
func TestUnlimitedRetentionDoesNothing(t *testing.T) {
	vm := &fakeVM{recent: 0, old: 12}
	srv := httptest.NewServer(vm.handler(t))
	t.Cleanup(srv.Close)

	j := jobFor(t, srv.URL, srv.URL, false)
	unlimited := plans.Plan{Code: "enterprise"}
	if j.pruneMetrics(context.Background(), orgA, unlimited) {
		t.Error("pruned under unlimited retention")
	}
	if j.deleteLogs(context.Background(), orgA, unlimited) {
		t.Error("filed a log delete under unlimited retention")
	}
}
