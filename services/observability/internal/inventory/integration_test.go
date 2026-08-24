//go:build integration

// Host inventory against a real Postgres.
//
// The thing worth proving here cannot be faked: List carries no org predicate
// of its own, so its scoping is entirely the RLS policy on observability_hosts
// (migration 0002) evaluated by the server. A unit test with a stub would
// assert the absence of a WHERE clause and prove nothing.
//
//	TEST_DATABASE_ADMIN_URL  owner/superuser DSN - applies migrations, seeds orgs
//	TEST_DATABASE_URL        app DSN (klaro_obs_app) - NOSUPERUSER, NOBYPASSRLS
//
// Rows are suffixed with a per-run token so repeated runs never collide on
// UNIQUE (org_id, host_ident) and the suite needs no teardown.
package inventory

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/migrations"
)

const (
	orgA = "00000000-0000-0000-0000-0000000000aa"
	orgB = "00000000-0000-0000-0000-0000000000bb"
)

var runToken = fmt.Sprintf("%d", time.Now().UnixNano())

func tag(s string) string { return s + "-" + runToken }

func adminPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_ADMIN_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_ADMIN_URL not set")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// setup migrates, seeds two orgs and returns the app-role handle.
func setup(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	admin := adminPool(t)

	if _, err := db.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, org := range []string{orgA, orgB} {
		_, err := admin.Exec(ctx,
			`INSERT INTO organizations (id, name, plan_code) VALUES ($1, $2, 'free')
			 ON CONFLICT (id) DO NOTHING`, org, tag("org-"+org[len(org)-2:]))
		if err != nil {
			t.Fatalf("seed org %s: %v", org, err)
		}
	}

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	d, err := db.New(ctx, db.Options{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.AssertRLSEnforced(ctx); err != nil {
		t.Fatalf("the app role can see through RLS, so nothing below proves anything: %v", err)
	}
	return d
}

// register writes a host into one org's registry through the app role, which is
// the same path the usage report takes.
func register(t *testing.T, d *db.DB, orgID, ident, service string, age time.Duration) {
	t.Helper()
	err := d.WithOrg(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO observability_hosts (org_id, host_ident, service, env, last_seen_at)
			 VALUES (current_setting('app.current_org')::uuid, $1, $2, 'prod',
			         now() - make_interval(secs => $3))
			 ON CONFLICT (org_id, host_ident) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at`,
			ident, service, age.Seconds())
		return err
	})
	if err != nil {
		t.Fatalf("register %s in %s: %v", ident, orgID, err)
	}
}

// stubMetrics returns fixed readings for whichever hosts it was given.
type stubMetrics struct{ byHost map[string]explorer.HostVitals }

func (s stubMetrics) Vitals(context.Context, string, time.Duration) (map[string]explorer.HostVitals, error) {
	return s.byHost, nil
}

// failingMetrics stands in for a VictoriaMetrics that is down.
type failingMetrics struct{}

func (failingMetrics) Vitals(context.Context, string, time.Duration) (map[string]explorer.HostVitals, error) {
	return nil, fmt.Errorf("vmselect unreachable")
}

func identsOf(r Result) []string {
	out := make([]string, 0, len(r.Data))
	for _, h := range r.Data {
		out = append(out, h.HostIdent)
	}
	return out
}

// The listing is scoped by RLS alone. A host registered under orgB must be
// invisible to orgA even though the SQL names no org.
func TestListIsScopedByRLS(t *testing.T) {
	d := setup(t)
	mine, theirs := tag("host-mine"), tag("host-theirs")
	register(t, d, orgA, mine, "checkout-api", 10*time.Second)
	register(t, d, orgB, theirs, "other-org-api", 10*time.Second)

	svc := New(d, nil, 15*time.Minute)
	got, err := svc.List(context.Background(), orgA)
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	for _, ident := range identsOf(got) {
		found[ident] = true
	}
	if !found[mine] {
		t.Errorf("orgA cannot see its own host %q: %v", mine, identsOf(got))
	}
	if found[theirs] {
		t.Fatalf("orgA sees orgB's host %q - RLS is not scoping this listing", theirs)
	}
}

// A host that stopped reporting keeps its row and is marked stale. Dropping it
// would remove it from the screen at the exact moment someone needs to notice
// it, and the active count has to agree with what the quota meters.
func TestListMarksStaleHostsWithoutHidingThem(t *testing.T) {
	d := setup(t)
	fresh, stale := tag("host-fresh"), tag("host-stale")
	register(t, d, orgA, fresh, "checkout-api", 10*time.Second)
	register(t, d, orgA, stale, "batch-runner", 2*time.Hour)

	svc := New(d, nil, 15*time.Minute)
	got, err := svc.List(context.Background(), orgA)
	if err != nil {
		t.Fatal(err)
	}

	status := map[string]string{}
	for _, h := range got.Data {
		status[h.HostIdent] = h.Status
	}
	if status[fresh] != StatusUp {
		t.Errorf("%s status = %q, want %q", fresh, status[fresh], StatusUp)
	}
	if status[stale] != StatusStale {
		t.Errorf("%s status = %q, want %q", stale, status[stale], StatusStale)
	}
	if got.ActiveWindowSec != 900 {
		t.Errorf("active_window_sec = %d, want the configured window echoed back", got.ActiveWindowSec)
	}
	if got.Active >= got.Total {
		t.Errorf("active %d of total %d: the stale host must not be counted active",
			got.Active, got.Total)
	}
}

// The readings are joined in the application, keyed on host_ident. That is the
// equality the whole feature rests on: host_ident here, the `instance` label
// there. A host with no series keeps its row with null readings.
func TestListJoinsReadingsOnHostIdent(t *testing.T) {
	d := setup(t)
	withData, without := tag("host-hot"), tag("host-quiet")
	register(t, d, orgA, withData, "checkout-api", 5*time.Second)
	register(t, d, orgA, without, "catalog-api", 5*time.Second)

	cpu, mem := 91.5, 40.0
	svc := New(d, stubMetrics{byHost: map[string]explorer.HostVitals{
		withData: {CPUPct: &cpu, MemPct: &mem},
	}}, 15*time.Minute)

	got, err := svc.List(context.Background(), orgA)
	if err != nil {
		t.Fatal(err)
	}
	if !got.MetricsAvailable {
		t.Error("metrics_available must be true when the readings were fetched")
	}

	byIdent := map[string]Host{}
	for _, h := range got.Data {
		byIdent[h.HostIdent] = h
	}
	if v := byIdent[withData].CPUPct; v == nil || *v != cpu {
		t.Errorf("%s cpu = %v, want %v", withData, v, cpu)
	}
	if v := byIdent[without].CPUPct; v != nil {
		t.Errorf("%s cpu = %v, want nil for a host with no series", without, v)
	}
	// Busiest first, so the table and the hostmap agree on what matters.
	if len(got.Data) > 0 && got.Data[0].HostIdent != withData {
		t.Errorf("first row = %q, want the busiest host %q", got.Data[0].HostIdent, withData)
	}
}

// A metrics backend that is down degrades the answer rather than failing it:
// the registry half is the more important one, and metrics_available carries
// the fact so the screen can say why the gauges are empty.
func TestListSurvivesAMetricsBackendOutage(t *testing.T) {
	d := setup(t)
	ident := tag("host-degraded")
	register(t, d, orgA, ident, "checkout-api", 5*time.Second)

	svc := New(d, failingMetrics{}, 15*time.Minute)
	got, err := svc.List(context.Background(), orgA)
	if err != nil {
		t.Fatalf("a metrics outage must not fail the inventory: %v", err)
	}
	if got.MetricsAvailable {
		t.Error("metrics_available must be false when the readings could not be fetched")
	}
	found := false
	for _, h := range got.Data {
		if h.HostIdent == ident {
			found = true
			if h.CPUPct != nil {
				t.Errorf("cpu = %v, want nil", h.CPUPct)
			}
		}
	}
	if !found {
		t.Errorf("%q missing; the registry half must still answer", ident)
	}
}
