//go:build integration

// Metering against a real Postgres. What needs a server: RLS, the additive
// ON CONFLICT accumulation, and the emission ledger that keeps a restart from
// billing twice.
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/observability/internal/plans"
	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/redisx"
	"github.com/klaro/observability/migrations"
)

var runToken = fmt.Sprintf("%d", time.Now().UnixNano())

// freshOrg gives each test its own org: usage rows outlive a run (a month-long
// period, a 15 minute host window), so sharing one would make each run depend
// on the previous.
func freshOrg(t *testing.T, admin *pgxpool.Pool) string {
	t.Helper()
	var id string
	err := admin.QueryRow(context.Background(),
		`INSERT INTO organizations (name, plan_code) VALUES ($1, 'free') RETURNING id::text`,
		"usage-"+runToken).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setup(t *testing.T) (*db.DB, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	adminDSN := os.Getenv("TEST_DATABASE_ADMIN_URL")
	appDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" || appDSN == "" {
		t.Skip("TEST_DATABASE_ADMIN_URL / TEST_DATABASE_URL not set")
	}
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := db.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	d, err := db.New(ctx, db.Options{DSN: appDSN, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.AssertRLSEnforced(ctx); err != nil {
		t.Fatal(err)
	}
	return d, admin
}

// Reports accumulate: the gateway flushes every few seconds and the month's
// total is the sum, so a second report must add rather than replace.
func TestRecordAccumulatesAndRegistersHosts(t *testing.T) {
	d, admin := setup(t)
	store := New(d)
	org := freshOrg(t, admin)
	ctx := context.Background()

	report := Report{
		OrgID: org,
		Signals: map[string]SignalUsage{
			"metrics": {Bytes: 1000, Items: 10},
			"traces":  {Bytes: 2000, Items: 5},
		},
		Hosts: []Host{{Ident: "pod-1", Service: "checkout", Env: "prod"}},
	}
	for i := 0; i < 3; i++ {
		if err := store.Record(ctx, report); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}

	metrics := ingestedBytes(t, d, org, "metrics")
	traces := ingestedBytes(t, d, org, "traces")
	if metrics != 3000 || traces != 6000 {
		t.Errorf("accumulated metrics=%d traces=%d, want 3000 and 6000", metrics, traces)
	}

	// The host registry is what makes the host quota real: before it was
	// written, the active-host count read an empty table and every org looked
	// idle.
	hosts := countHosts(t, d, org)
	if hosts != 1 {
		t.Errorf("hosts = %d, want 1 (repeat reports must upsert)", hosts)
	}
}

func ingestedBytes(t *testing.T, d *db.DB, orgID, signal string) int64 {
	t.Helper()
	var n int64
	err := d.WithOrg(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT coalesce(sum(ingested_bytes), 0) FROM observability_usage_rollups
			  WHERE signal = $1 AND period_start = date_trunc('month', now())`, signal).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func countHosts(t *testing.T, d *db.DB, orgID string) int {
	t.Helper()
	var n int
	err := d.WithOrg(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM observability_hosts`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The billing contract: increments, published once each. A second pass with no
// new usage must publish nothing, because double billing is worse than a gap.
func TestEmitPublishesIncrementsExactlyOnce(t *testing.T) {
	d, admin := setup(t)
	store := New(d)
	org := freshOrg(t, admin)
	ctx := context.Background()

	signal := redisx.NewMemory()
	events, cancel, err := signal.Subscribe(ctx, EmitSubject)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	emitter := NewEmitter(store, plans.New(d), signal, 15*time.Minute)

	// 2 GB and one host.
	if err := store.Record(ctx, Report{
		OrgID:   org,
		Signals: map[string]SignalUsage{"metrics": {Bytes: 2_000_000_000, Items: 1}},
		Hosts:   []Host{{Ident: "pod-1", Service: "checkout"}},
	}); err != nil {
		t.Fatal(err)
	}

	published, err := emitter.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// RunOnce meters every org, so this counts at least this org's two meters.
	if published < 2 {
		t.Fatalf("published %d increments, want at least both meters of this org", published)
	}
	byMeter := drainEvents(t, events, org)
	if got := byMeter[MeterIngestGB]; got < 1.9 || got > 2.1 {
		t.Errorf("%s = %v, want ~2", MeterIngestGB, got)
	}
	if got := byMeter[MeterHosts]; got != 1 {
		t.Errorf("%s = %v, want 1", MeterHosts, got)
	}

	// Nothing changed, so nothing may be published: the ledger is what makes a
	// restart or an overlapping cron harmless.
	// Nothing changed for this org, so nothing may be published for it. Other
	// orgs in the database may still move, so the check is per-org rather than
	// on the total count.
	if _, err := emitter.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if repeat := drainEvents(t, events, org); len(repeat) != 0 {
		t.Fatalf("a second pass re-published %v for an unchanged org", repeat)
	}

	// One more GB must publish exactly the delta, not the new total.
	if err := store.Record(ctx, Report{
		OrgID:   org,
		Signals: map[string]SignalUsage{"metrics": {Bytes: 1_000_000_000}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := emitter.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	delta := drainEvents(t, events, org)
	if got := delta[MeterIngestGB]; got < 0.9 || got > 1.1 {
		t.Errorf("increment = %v, want ~1 (the delta, not the 3 GB total)", got)
	}
}

// drainEvents reads what is buffered and returns the last quantity per meter for
// one org.
//
// Filtering by org matters: RunOnce meters every org in the database, so a
// neighbouring test's fixtures would otherwise be read as this one's result.
func drainEvents(t *testing.T, ch <-chan []byte, orgID string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-ch:
			var e Event
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatal(err)
			}
			if e.OrgID == orgID {
				out[e.Meter] = e.Quantity
			}
			continue
		case <-deadline:
			return out
		default:
			return out
		}
	}
}

// Usage is org-scoped through RLS: one tenant's volume must not appear in
// another's meters.
func TestUsageDoesNotLeakAcrossOrgs(t *testing.T) {
	d, admin := setup(t)
	store := New(d)
	loud := freshOrg(t, admin)
	quiet := freshOrg(t, admin)
	ctx := context.Background()

	if err := store.Record(ctx, Report{
		OrgID:   loud,
		Signals: map[string]SignalUsage{"logs": {Bytes: 5_000_000_000}},
		Hosts:   []Host{{Ident: "pod-loud"}},
	}); err != nil {
		t.Fatal(err)
	}

	if got := ingestedBytes(t, d, quiet, "logs"); got != 0 {
		t.Errorf("the quiet org sees %d bytes of its neighbour", got)
	}
	if got := countHosts(t, d, quiet); got != 0 {
		t.Errorf("the quiet org sees %d hosts of its neighbour", got)
	}
}
