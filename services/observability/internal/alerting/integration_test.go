//go:build integration

// Rule and event lifecycle against a real Postgres. What needs a server: RLS
// policy evaluation, the partial unique index that makes re-sent alerts free,
// and ON CONFLICT semantics.
//
//	TEST_DATABASE_ADMIN_URL  owner DSN - migrations and org seeding
//	TEST_DATABASE_URL        app DSN (klaro_obs_app) - NOSUPERUSER, NOBYPASSRLS
package alerting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/observability/internal/platform/audit"
	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/migrations"
)

const orgB = "00000000-0000-0000-0000-0000000000bb"

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

func namedInput(name string) Input {
	in := validInput()
	in.Name = ptr(name)
	return in
}

func TestRuleLifecycle(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	created, err := s.Create(ctx, orgA, namedInput(tag("latency")),
		AuditHook(audit.NewPG(d), audit.ActionRuleCreate, nil))
	if err != nil {
		t.Fatal(err)
	}
	if created.Query == "" || created.Signal != SignalMetric {
		t.Fatalf("created = %+v", created)
	}

	// The rendered query is stored, not the spec alone: vmalert reads the query.
	fetched, err := s.Get(ctx, orgA, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Query != created.Query || fetched.QuerySpec.Metric != "http_server_duration" {
		t.Errorf("round trip lost data: %+v", fetched)
	}

	// A spec change must re-render the query, or the rule would evaluate the old
	// expression while showing the new spec.
	updated, err := s.Update(ctx, orgA, created.ID,
		Input{QuerySpec: &QuerySpec{Metric: "db_client_duration", Agg: "p99", StepSec: 120}},
		AuditHook(audit.NewPG(d), audit.ActionRuleUpdate, nil))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Query == created.Query {
		t.Error("the query was not re-rendered after a spec change")
	}
	if !containsAll(updated.Query, "db_client_duration", "0.99", orgA) {
		t.Errorf("re-rendered query = %s", updated.Query)
	}

	if err := s.Remove(ctx, orgA, created.ID,
		AuditHook(audit.NewPG(d), audit.ActionRuleDelete, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, orgA, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after removal Get = %v, want ErrNotFound", err)
	}

	if n := countAudit(t, d, orgA, audit.ActionRuleCreate); n < 1 {
		t.Error("no audit row for obs.rule.create")
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		found := false
		for i := 0; i+len(p) <= len(s); i++ {
			if s[i:i+len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func countAudit(t *testing.T, d *db.DB, orgID, action string) int {
	t.Helper()
	var n int
	err := d.WithOrg(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_logs WHERE action = $1`, action).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDuplicateRuleNameIsRejectedPerOrg(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()
	name := tag("dup")

	if _, err := s.Create(ctx, orgA, namedInput(name), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, orgA, namedInput(name), nil); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("second create = %v, want ErrDuplicateName", err)
	}
	// The constraint is per org, so the neighbour may reuse the name.
	if _, err := s.Create(ctx, orgB, namedInput(name), nil); err != nil {
		t.Errorf("orgB could not reuse the name: %v", err)
	}
}

// The invariant that matters: one org must not see, change or delete another's
// rules. RLS is what enforces it, so it needs a real server.
func TestRulesAreInvisibleAcrossOrgs(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	mine, err := s.Create(ctx, orgA, namedInput(tag("private")), nil)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.List(ctx, orgB)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range theirs {
		if r.ID == mine.ID {
			t.Fatal("orgB can see a rule of orgA")
		}
	}
	if _, err := s.Get(ctx, orgB, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant Get = %v, want ErrNotFound", err)
	}
	if err := s.Remove(ctx, orgB, mine.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant Remove = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, orgA, mine.ID); err != nil {
		t.Errorf("the rule was damaged by the attempts: %v", err)
	}
}

// vmalert re-sends a firing alert every resend interval. Without dedup the
// history would grow a row a minute for as long as the problem lasted, and be
// unreadable exactly when someone needed to read it.
func TestFiringIsRecordedOnceAndResolvedOnce(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	rule, err := s.Create(ctx, orgA, namedInput(tag("flapper")), nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"service_name": "checkout", "severity": "warning"}
	value := 0.9

	first, created, err := s.OpenFiring(ctx, orgA, rule.ID, &value, labels, rule.Channels)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("the first firing was not recorded")
	}
	if first.State != StateFiring || first.Value == nil || *first.Value != 0.9 {
		t.Errorf("event = %+v", first)
	}

	// Three re-sends must all be no-ops that return the same open event.
	for i := 0; i < 3; i++ {
		again, created, err := s.OpenFiring(ctx, orgA, rule.ID, &value, labels, rule.Channels)
		if err != nil {
			t.Fatal(err)
		}
		if created {
			t.Fatalf("re-send %d opened a second event", i)
		}
		if again.ID != first.ID {
			t.Errorf("re-send returned a different event: %s vs %s", again.ID, first.ID)
		}
	}

	// A different label set is a different episode.
	other := map[string]string{"service_name": "billing"}
	if _, created, err := s.OpenFiring(ctx, orgA, rule.ID, &value, other, nil); err != nil || !created {
		t.Fatalf("a different label set did not open its own event (created=%v err=%v)", created, err)
	}

	resolved, changed, err := s.Resolve(ctx, orgA, rule.ID, labels)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || resolved.State != StateResolved || resolved.ResolvedAt == nil {
		t.Fatalf("resolve = %+v changed=%v", resolved, changed)
	}
	// Resolving twice must be quiet: vmalert can resolve an alert this process
	// never saw fire, and a restart makes that ordinary.
	if _, changed, err := s.Resolve(ctx, orgA, rule.ID, labels); err != nil || changed {
		t.Errorf("second resolve changed=%v err=%v, want no change", changed, err)
	}

	// After resolution the same labels may fire again as a fresh episode.
	if _, created, err := s.OpenFiring(ctx, orgA, rule.ID, &value, labels, nil); err != nil || !created {
		t.Fatalf("could not re-fire after resolution (created=%v err=%v)", created, err)
	}
}

func TestListEventsFiltersAndScopes(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	rule, err := s.Create(ctx, orgA, namedInput(tag("listable")), nil)
	if err != nil {
		t.Fatal(err)
	}
	value := 1.0
	if _, _, err := s.OpenFiring(ctx, orgA, rule.ID, &value,
		map[string]string{"k": tag("v")}, nil); err != nil {
		t.Fatal(err)
	}

	firing, err := s.ListEvents(ctx, orgA, EventFilter{State: StateFiring, RuleID: rule.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(firing) == 0 {
		t.Fatal("the firing event was not listed")
	}

	resolved, err := s.ListEvents(ctx, orgA, EventFilter{State: StateResolved, RuleID: rule.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 0 {
		t.Errorf("resolved filter returned %d events", len(resolved))
	}

	// Another org must not see the history, even filtered by the rule id.
	across, err := s.ListEvents(ctx, orgB, EventFilter{RuleID: rule.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(across) != 0 {
		t.Errorf("orgB saw %d events of orgA", len(across))
	}

	if _, err := s.ListEvents(ctx, orgA, EventFilter{State: "sideways"}); !errors.Is(err, ErrInvalidRule) {
		t.Error("an unknown state filter was accepted")
	}
}
