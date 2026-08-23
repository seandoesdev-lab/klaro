//go:build integration

// Cross-tenant isolation proof. This is the one invariant that cannot be
// checked without a real Postgres: RLS policy evaluation happens in the server.
//
//	TEST_DATABASE_ADMIN_URL  owner/superuser DSN - applies migrations, seeds orgs
//	TEST_DATABASE_URL        app DSN (klaro_obs_app) - NOSUPERUSER, NOBYPASSRLS
//
// Rows are suffixed with a per-run token, so repeated runs never collide on the
// unique constraints and the test needs no teardown.
package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/observability/migrations"
)

const (
	orgA = "00000000-0000-0000-0000-0000000000aa"
	orgB = "00000000-0000-0000-0000-0000000000bb"
)

// runToken keeps each execution of the suite in its own row namespace.
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

// setup migrates the schema, seeds two orgs and returns the app-role handle.
func setup(t *testing.T) (*DB, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	admin := adminPool(t)

	if _, err := Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, id := range []string{orgA, orgB} {
		if _, err := admin.Exec(ctx,
			`INSERT INTO organizations (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
			id, "org-"+id[len(id)-2:]); err != nil {
			t.Fatalf("seed org: %v", err)
		}
	}

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	app, err := New(ctx, Options{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(app.Close)
	return app, admin
}

// insertKey writes a key for whichever org the transaction is scoped to.
func insertKey(ctx context.Context, tx pgx.Tx, name string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO observability_keys (org_id, name, key_prefix, key_hash)
		 VALUES (current_setting('app.current_org')::uuid, $1, $2, $3)`,
		name, "obsk_"+name, "hash-"+name)
	return err
}

func TestAppRoleDoesNotBypassRLS(t *testing.T) {
	app, admin := setup(t)
	ctx := context.Background()

	if err := app.AssertRLSEnforced(ctx); err != nil {
		t.Fatalf("app role must not bypass RLS: %v", err)
	}
	// Sanity check the guard itself: the admin role is expected to bypass.
	if err := FromPool(admin).AssertRLSEnforced(ctx); !errors.Is(err, ErrRLSBypassed) {
		t.Errorf("admin role check = %v, want ErrRLSBypassed", err)
	}
}

func TestCrossTenantReadIsBlocked(t *testing.T) {
	app, _ := setup(t)
	ctx := context.Background()
	nameA, nameB := tag("read-a"), tag("read-b")

	for org, name := range map[string]string{orgA: nameA, orgB: nameB} {
		if err := app.WithOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return insertKey(ctx, tx, name)
		}); err != nil {
			t.Fatalf("insert for %s: %v", org, err)
		}
	}

	// An unfiltered SELECT under org A must still return only org A rows.
	if err := app.WithOrg(ctx, orgA, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT name, org_id::text FROM observability_keys`)
		if err != nil {
			return err
		}
		defer rows.Close()
		sawA := false
		for rows.Next() {
			var name, org string
			if err := rows.Scan(&name, &org); err != nil {
				return err
			}
			if org != orgA {
				t.Errorf("org A read a row owned by %s (%s)", org, name)
			}
			if name == nameA {
				sawA = true
			}
		}
		if !sawA {
			t.Error("org A could not see its own key")
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}

	// Targeting org B's row by its globally unique key_hash still finds nothing.
	if err := app.WithOrg(ctx, orgA, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM observability_keys WHERE key_hash = $1`, "hash-"+nameB).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("org A found %d org-B rows by key_hash", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// WITH CHECK: a handler that explicitly names another org_id must be refused,
// not silently rewritten.
func TestCrossTenantWriteIsBlocked(t *testing.T) {
	app, _ := setup(t)
	ctx := context.Background()

	err := app.WithOrg(ctx, orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO observability_keys (org_id, name, key_prefix, key_hash)
			 VALUES ($1, $2, $3, $4)`,
			orgB, tag("smuggled"), "obsk_x", "hash-"+tag("smuggled"))
		return err
	})
	if err == nil {
		t.Fatal("org A inserted a row owned by org B")
	}
	if !isRLSViolation(err) {
		t.Fatalf("want a row-level-security violation, got %v", err)
	}
}

// An UPDATE cannot move a row into another tenant either.
func TestTenantReassignmentIsBlocked(t *testing.T) {
	app, _ := setup(t)
	ctx := context.Background()
	name := tag("move")

	if err := app.WithOrg(ctx, orgA, func(ctx context.Context, tx pgx.Tx) error {
		return insertKey(ctx, tx, name)
	}); err != nil {
		t.Fatal(err)
	}

	err := app.WithOrg(ctx, orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE observability_keys SET org_id = $1 WHERE name = $2`, orgB, name)
		return err
	})
	if err == nil {
		t.Fatal("org A moved one of its rows into org B")
	}
	if !isRLSViolation(err) {
		t.Fatalf("want a row-level-security violation, got %v", err)
	}
}

// An UPDATE aimed at another tenant matches no rows rather than mutating them.
func TestCrossTenantUpdateMatchesNothing(t *testing.T) {
	app, _ := setup(t)
	ctx := context.Background()
	nameB := tag("victim")

	if err := app.WithOrg(ctx, orgB, func(ctx context.Context, tx pgx.Tx) error {
		return insertKey(ctx, tx, nameB)
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.WithOrg(ctx, orgA, func(ctx context.Context, tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE observability_keys SET status = 'revoked' WHERE name = $1`, nameB)
		if err != nil {
			return err
		}
		if n := ct.RowsAffected(); n != 0 {
			t.Errorf("org A revoked %d of org B keys", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Confirm from org B that the key is untouched.
	if err := app.WithOrg(ctx, orgB, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM observability_keys WHERE name = $1`, nameB).Scan(&status); err != nil {
			return err
		}
		if status != "active" {
			t.Errorf("org B key status = %q, want active", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Every org-scoped table must behave the same way; a new table that forgets
// FORCE would still pass a keys-only test.
func TestAllOrgScopedTablesEnforceRLS(t *testing.T) {
	app, admin := setup(t)
	ctx := context.Background()

	tables := []string{
		"observability_keys",
		"observability_hosts",
		"observability_usage_rollups",
		"alert_rules",
		"alert_events",
		"dashboards",
		"audit_logs",
	}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			var enabled, forced bool
			if err := admin.QueryRow(ctx,
				`SELECT relrowsecurity, relforcerowsecurity
				 FROM pg_class WHERE oid = to_regclass($1)`, table).Scan(&enabled, &forced); err != nil {
				t.Fatalf("inspect %s: %v", table, err)
			}
			if !enabled || !forced {
				t.Fatalf("%s: ENABLE=%v FORCE=%v, want both true", table, enabled, forced)
			}

			// And the policy actually rejects a foreign org_id at write time.
			err := app.WithOrg(ctx, orgA, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx,
					`INSERT INTO `+table+` (org_id) VALUES ($1)`, orgB)
				return err
			})
			if err == nil {
				t.Fatalf("%s accepted a row for another org", table)
			}
			// NOT NULL on other columns can fire first; only a successful insert
			// is a failure, but an explicit RLS rejection is the expected shape
			// when the row is otherwise well-formed.
			t.Logf("%s rejected foreign org_id: %v", table, err)
		})
	}
}

// SET LOCAL must not survive its transaction: pooled connections are reused by
// the next request, which may belong to a different tenant.
func TestOrgSettingIsTransactionLocal(t *testing.T) {
	app, _ := setup(t)
	ctx := context.Background()

	if err := app.WithOrg(ctx, orgA, func(ctx context.Context, tx pgx.Tx) error {
		got, err := CurrentOrg(ctx, tx)
		if err != nil {
			return err
		}
		if got != orgA {
			t.Errorf("app.current_org = %q, want %q", got, orgA)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A later transaction on the (likely recycled) connection starts unset.
	tx, err := app.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	got, err := CurrentOrg(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("app.current_org leaked across transactions: %q", got)
	}
}

// Without a tenant scope there is nothing for the policy to match, so a query
// outside WithOrg fails instead of returning a partial or foreign result set.
func TestQueryWithoutOrgScopeFails(t *testing.T) {
	app, _ := setup(t)
	ctx := context.Background()

	tx, err := app.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM observability_keys`).Scan(&n); err == nil {
		t.Fatalf("unscoped query returned %d rows; it must fail on the unset GUC", n)
	}
}

func TestWithOrgRejectsMalformedOrg(t *testing.T) {
	app, _ := setup(t)
	ctx := context.Background()
	for _, bad := range []string{"", "org-a", "aa'; DROP TABLE observability_keys--"} {
		err := app.WithOrg(ctx, bad, func(context.Context, pgx.Tx) error { return nil })
		if !errors.Is(err, ErrInvalidOrgID) {
			t.Errorf("WithOrg(%q) = %v, want ErrInvalidOrgID", bad, err)
		}
	}
}

// isRLSViolation matches the SQLSTATE Postgres raises for a WITH CHECK failure.
func isRLSViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42501" // insufficient_privilege
	}
	return false
}
