//go:build integration

// Dashboard persistence against a real Postgres. What needs a server: RLS policy
// evaluation, the per-org unique name, and whether the panel document survives a
// JSONB round trip unchanged.
package dashboards

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/platform/audit"
	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/migrations"
)

const (
	orgA = "00000000-0000-0000-0000-0000000000aa"
	orgB = "00000000-0000-0000-0000-0000000000bb"
)

var runToken = fmt.Sprintf("%d", time.Now().UnixNano())

func tag(s string) string { return s + "-" + runToken }

func setup(t *testing.T) *db.DB {
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
	for _, org := range []string{orgA, orgB} {
		if _, err := admin.Exec(ctx,
			`INSERT INTO organizations (id, name, plan_code) VALUES ($1, $2, 'free')
			 ON CONFLICT (id) DO NOTHING`, org, tag("org")); err != nil {
			t.Fatal(err)
		}
	}
	d, err := db.New(ctx, db.Options{DSN: appDSN, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.AssertRLSEnforced(ctx); err != nil {
		t.Fatal(err)
	}
	return d
}

func ptr[T any](v T) *T { return &v }

func fullSpec() Spec {
	return Spec{
		RangeSec: 3600, RefreshSec: 30,
		Panels: []Panel{
			{ID: "latency", Title: "p95", Type: "timeseries", Layout: Layout{W: 6, H: 4},
				Query: PanelQuery{Signal: SignalMetrics, Metric: "http_server_duration",
					Agg: "p95", StepSec: 60,
					Filters: []explorer.Matcher{{Label: "service_name", Value: "checkout"}}}},
			{ID: "slow", Title: "Slow traces", Type: "traces", Layout: Layout{X: 6, W: 6, H: 4},
				Query: PanelQuery{Signal: SignalTraces, Service: "checkout", MinDurationMS: 3000}},
		},
	}
}

func countAudit(t *testing.T, d *db.DB, orgID, action string) int {
	t.Helper()
	var n int
	err := d.WithOrg(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = $1`, action).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDashboardLifecycle(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()
	spec := fullSpec()

	created, err := s.Create(ctx, orgA, Input{
		Name: ptr(tag("checkout")), Description: ptr("the one on the wall"), Spec: &spec,
	}, AuditHook(audit.NewPG(d), audit.ActionDashboardCreate, nil))
	if err != nil {
		t.Fatal(err)
	}

	// The panel document has to survive JSONB unchanged: it is the dashboard.
	got, err := s.Get(ctx, orgA, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.Panels) != 2 || got.Spec.RangeSec != 3600 || got.Spec.RefreshSec != 30 {
		t.Fatalf("spec did not round trip: %+v", got.Spec)
	}
	p := got.Spec.Panels[0]
	if p.Query.Agg != "p95" || p.Query.StepSec != 60 || len(p.Query.Filters) != 1 {
		t.Errorf("metrics panel = %+v", p.Query)
	}
	if p.Query.Filters[0].Label != "service_name" || p.Query.Filters[0].Value != "checkout" {
		t.Errorf("filter did not round trip: %+v", p.Query.Filters[0])
	}
	if got.Spec.Panels[1].Query.MinDurationMS != 3000 {
		t.Errorf("traces panel = %+v", got.Spec.Panels[1].Query)
	}
	if got.Description != "the one on the wall" {
		t.Errorf("description = %q", got.Description)
	}

	// A partial update keeps what it does not mention.
	updated, err := s.Update(ctx, orgA, created.ID, Input{Description: ptr("renamed section")},
		AuditHook(audit.NewPG(d), audit.ActionDashboardUpdate, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Spec.Panels) != 2 || updated.Name != created.Name {
		t.Errorf("a description-only PATCH changed more: %+v", updated)
	}

	if err := s.Remove(ctx, orgA, created.ID,
		AuditHook(audit.NewPG(d), audit.ActionDashboardDelete, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, orgA, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after removal Get = %v, want ErrNotFound", err)
	}
	if n := countAudit(t, d, orgA, audit.ActionDashboardCreate); n < 1 {
		t.Error("no audit row for obs.dashboard.create")
	}
}

// A PATCH that would leave an invalid document must be refused, not stored.
func TestUpdateRevalidatesTheMergedSpec(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()
	spec := fullSpec()

	created, err := s.Create(ctx, orgA, Input{Name: ptr(tag("revalidate")), Spec: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}

	bad := Spec{Panels: []Panel{{ID: "p1", Type: "timeseries", Layout: Layout{W: 6, H: 4},
		Query: PanelQuery{Signal: SignalMetrics, Metric: "cpu",
			Filters: []explorer.Matcher{{Label: explorer.OrgLabel, Value: orgB}}}}}}
	if _, err := s.Update(ctx, orgA, created.ID, Input{Spec: &bad}, nil); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("err = %v, want ErrInvalidSpec", err)
	}

	// The stored document must be untouched.
	after, err := s.Get(ctx, orgA, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Spec.Panels) != 2 {
		t.Errorf("the rejected PATCH still changed the stored spec: %+v", after.Spec)
	}
}

func TestDuplicateNameIsRejectedPerOrg(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()
	name := tag("dup")
	spec := Spec{}

	if _, err := s.Create(ctx, orgA, Input{Name: &name, Spec: &spec}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, orgA, Input{Name: &name, Spec: &spec}, nil); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("second create = %v, want ErrDuplicateName", err)
	}
	// The constraint is per org, so the neighbour may reuse the name.
	if _, err := s.Create(ctx, orgB, Input{Name: &name, Spec: &spec}, nil); err != nil {
		t.Errorf("orgB could not reuse the name: %v", err)
	}
}

// One org must not see, change or delete another's dashboards. RLS enforces it,
// so it needs a real server.
func TestDashboardsAreInvisibleAcrossOrgs(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()
	spec := fullSpec()

	mine, err := s.Create(ctx, orgA, Input{Name: ptr(tag("private")), Spec: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.List(ctx, orgB)
	if err != nil {
		t.Fatal(err)
	}
	for _, dash := range theirs {
		if dash.ID == mine.ID {
			t.Fatal("orgB can see a dashboard of orgA")
		}
	}
	if _, err := s.Get(ctx, orgB, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant Get = %v, want ErrNotFound", err)
	}
	if _, err := s.Update(ctx, orgB, mine.ID, Input{Name: ptr("stolen")}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant Update = %v, want ErrNotFound", err)
	}
	if err := s.Remove(ctx, orgB, mine.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant Remove = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, orgA, mine.ID); err != nil {
		t.Errorf("the dashboard was damaged by the attempts: %v", err)
	}
}
