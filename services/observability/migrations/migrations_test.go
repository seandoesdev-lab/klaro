package migrations

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// orgScopedTables are every table the observability schema creates that carries
// org_id. Each one must be locked down identically - a table that ships with
// ENABLE but not FORCE, or without a WITH CHECK clause, is a silent hole.
var orgScopedTables = []string{
	"observability_keys",
	"observability_hosts",
	"observability_usage_rollups",
	"observability_tenants",
	"observability_usage_emissions",
	"alert_rules",
	"alert_events",
	"dashboards",
	"audit_logs",
}

func allSQL(t *testing.T) string {
	t.Helper()
	names, err := fs.Glob(FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, n := range names {
		body, err := fs.ReadFile(FS, n)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(body)
		b.WriteString("\n")
	}
	return b.String()
}

func TestExpectedMigrationsArePresent(t *testing.T) {
	want := []string{
		"0000_shared_prereq.sql",
		"0001_observability_keys.sql",
		"0002_observability_hosts.sql",
		"0003_observability_usage_rollups.sql",
		"0004_alert_rules.sql",
		"0005_alert_events.sql",
		"0006_dashboards.sql",
		"0007_plans_observability.sql",
		"0008_observability_tenants.sql",
		"0009_observability_key_rotation.sql",
		"0010_organizations_plan.sql",
		"0011_alerting_and_usage.sql",
	}
	got, err := fs.Glob(FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("embedded migrations = %v\nwant %v", got, want)
	}
}

// The RLS invariant, checked statically so a new migration cannot forget it.
func TestEveryOrgScopedTableForcesRLS(t *testing.T) {
	sql := allSQL(t)
	for _, table := range orgScopedTables {
		t.Run(table, func(t *testing.T) {
			required := []string{
				fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY;", table),
				// FORCE is what makes the policy apply to the table owner too.
				fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY;", table),
				fmt.Sprintf("CREATE POLICY %s_isolation ON %s", table, table),
			}
			for _, r := range required {
				if !strings.Contains(sql, r) {
					t.Errorf("missing: %s", r)
				}
			}
		})
	}
}

func TestPoliciesGuardBothReadAndWrite(t *testing.T) {
	sql := allSQL(t)
	// USING filters reads; WITH CHECK stops a row being written under another
	// org_id. One without the other leaves half the table open.
	if got := strings.Count(sql, "USING (org_id = current_setting('app.current_org')::uuid)"); got != len(orgScopedTables) {
		t.Errorf("USING clauses = %d, want %d", got, len(orgScopedTables))
	}
	if got := strings.Count(sql, "WITH CHECK (org_id = current_setting('app.current_org')::uuid)"); got != len(orgScopedTables) {
		t.Errorf("WITH CHECK clauses = %d, want %d", got, len(orgScopedTables))
	}
}

func TestOrgScopedTablesDeclareOrgID(t *testing.T) {
	sql := allSQL(t)
	for _, table := range orgScopedTables {
		if !strings.Contains(sql, "CREATE TABLE "+table) && !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("%s is never created", table)
		}
	}
	if got := strings.Count(sql, "org_id       uuid NOT NULL REFERENCES organizations(id)"); got == 0 {
		t.Error("org_id columns should be NOT NULL and reference organizations(id)")
	}
}

// Time-series bodies belong in VictoriaMetrics / Tempo / Loki. Postgres holds
// keys, hosts, rules, events, dashboards and rollups - references and metadata.
func TestNoTimeSeriesTablesInPostgres(t *testing.T) {
	sql := strings.ToLower(allSQL(t))
	for _, forbidden := range []string{
		"create table apm_spans",
		"create table apm_logs",
		"create table observability_metrics",
		"create table observability_spans",
		"create table observability_logs",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("%q violates the time-series-outside-the-RDB invariant", forbidden)
		}
	}
}

// The application role must not be able to see through RLS.
func TestAppRoleIsNotSuperuserAndNotBypassRLS(t *testing.T) {
	body, err := fs.ReadFile(FS, "0000_shared_prereq.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{"NOSUPERUSER", "NOBYPASSRLS"} {
		if !strings.Contains(s, want) {
			t.Errorf("app role must be created %s", want)
		}
	}
}
