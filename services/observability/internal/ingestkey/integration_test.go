//go:build integration

// Ingest-key lifecycle against a real Postgres. Everything asserted here
// depends on server-side behaviour that cannot be faked: RLS policy evaluation,
// the SECURITY DEFINER resolver of migration 0009, sequence-backed tenant
// assignment, and now() arithmetic for rotation grace.
//
//	TEST_DATABASE_ADMIN_URL  owner/superuser DSN - applies migrations, seeds orgs
//	TEST_DATABASE_URL        app DSN (klaro_obs_app) - NOSUPERUSER, NOBYPASSRLS
//
// Rows are suffixed with a per-run token so repeated runs never collide and the
// suite needs no teardown.
package ingestkey

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
	"github.com/klaro/observability/internal/tenants"
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

	// If this fails the whole suite is meaningless: a role that bypasses RLS
	// would pass every isolation assertion below without isolating anything.
	if err := d.AssertRLSEnforced(ctx); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestIssueResolveAndList(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	key, secret, err := s.Issue(ctx, orgA, tag("prod"), &ScopeLabel{Service: "checkout", Env: "prod"},
		AuditHook(audit.NewPG(d), audit.ActionKeyIssue, nil))
	if err != nil {
		t.Fatal(err)
	}
	if key.Status != StatusActive || key.KeyPrefix == "" {
		t.Fatalf("issued key = %+v", key)
	}
	if key.ScopeLabel == nil || key.ScopeLabel.Service != "checkout" {
		t.Errorf("scope_label round trip = %+v", key.ScopeLabel)
	}

	// The secret is returned once and never stored, so the only way to check it
	// was persisted correctly is to resolve it.
	res, err := s.Resolve(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if res.OrgID != orgA || res.KeyID != key.ID {
		t.Errorf("Resolve = %+v, want org %s key %s", res, orgA, key.ID)
	}
	if !res.Live(time.Now()) {
		t.Error("a freshly issued key is not live")
	}

	// A different secret of the same shape must not resolve to anything.
	other, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(unknown) = %v, want ErrNotFound", err)
	}

	keys, err := s.List(ctx, orgA)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, k := range keys {
		if k.ID == key.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("issued key missing from List (%d keys)", len(keys))
	}

	// Audit trail (design section 3): the issue must be recorded under the org.
	if n := countAudit(t, d, orgA, audit.ActionKeyIssue, key.ID); n != 1 {
		t.Errorf("audit rows for obs.key.issue = %d, want 1", n)
	}
}

func TestDuplicateNameIsRejected(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	name := tag("dup")
	if _, _, err := s.Issue(ctx, orgA, name, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Issue(ctx, orgA, name, nil, nil); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("second Issue = %v, want ErrDuplicateName", err)
	}
	// The constraint is per org, so the other tenant may reuse the name.
	if _, _, err := s.Issue(ctx, orgB, name, nil, nil); err != nil {
		t.Errorf("orgB could not reuse the name: %v", err)
	}
}

// countAudit reads audit_logs inside the org scope, which also proves the audit
// row landed under the same tenant as the key it describes.
func countAudit(t *testing.T, d *db.DB, orgID, action, resourceID string) int {
	t.Helper()
	var n int
	err := d.WithOrg(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_logs WHERE action = $1 AND resource_id = $2`,
			action, resourceID).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Cross-tenant isolation, the invariant that matters most here: one org must not
// be able to see, rotate or revoke the key of another. RLS is what enforces it,
// so this has to run against a real server.
func TestKeysAreInvisibleAcrossOrgs(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	key, secret, err := s.Issue(ctx, orgA, tag("private"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, k := range mustList(t, s, orgB) {
		if k.ID == key.ID {
			t.Fatal("orgB can see the key of orgA")
		}
	}
	if _, err := s.Revoke(ctx, orgB, key.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("orgB revoking the key of orgA = %v, want ErrNotFound", err)
	}
	if _, _, _, err := s.Rotate(ctx, orgB, key.ID, time.Minute, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("orgB rotating the key of orgA = %v, want ErrNotFound", err)
	}

	// The attempts above must not have damaged the key.
	res, err := s.Resolve(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Live(time.Now()) {
		t.Error("the key of orgA stopped working after orgB touched it")
	}
}

func mustList(t *testing.T, s *Store, orgID string) []Key {
	t.Helper()
	keys, err := s.List(context.Background(), orgID)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// Rotation must leave both keys usable during grace, and only the old one dead
// after it.
func TestRotationGraceWindow(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	name := tag("rotate-me")
	old, oldSecret, err := s.Issue(ctx, orgA, name, &ScopeLabel{Env: "prod"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	fresh, freshSecret, graceUntil, err := s.Rotate(ctx, orgA, old.ID, time.Hour,
		AuditHook(audit.NewPG(d), audit.ActionKeyRotate, nil))
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == old.ID {
		t.Fatal("Rotate returned the same key")
	}
	if fresh.Name != name {
		t.Errorf("replacement name = %q, want the original %q", fresh.Name, name)
	}
	if fresh.RotatedFromID == nil || *fresh.RotatedFromID != old.ID {
		t.Errorf("rotated_from_id = %v, want %s", fresh.RotatedFromID, old.ID)
	}
	if fresh.ScopeLabel == nil || fresh.ScopeLabel.Env != "prod" {
		t.Errorf("scope label was not carried over: %+v", fresh.ScopeLabel)
	}
	if !graceUntil.After(time.Now()) {
		t.Errorf("grace_until = %v, already in the past", graceUntil)
	}

	// Both secrets work during grace - that is the whole point of rotating
	// rather than revoking.
	for label, secret := range map[string]string{"old": oldSecret, "new": freshSecret} {
		res, err := s.Resolve(ctx, secret)
		if err != nil {
			t.Fatalf("%s key: %v", label, err)
		}
		if !res.Live(time.Now()) {
			t.Errorf("%s key is not live during grace: %+v", label, res)
		}
	}

	if n := countAudit(t, d, orgA, audit.ActionKeyRotate, fresh.ID); n != 1 {
		t.Errorf("audit rows for obs.key.rotate = %d, want 1", n)
	}
}

// A zero grace makes the old key expire immediately, which lets the expiry path
// be tested without waiting.
func TestGraceExpiryRetiresTheOldKey(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	old, oldSecret, err := s.Issue(ctx, orgA, tag("cutover"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Rotate(ctx, orgA, old.ID, 0, nil); err != nil {
		t.Fatal(err)
	}

	res, err := s.Resolve(ctx, oldSecret)
	if err != nil {
		t.Fatal(err)
	}
	if res.Live(time.Now()) {
		t.Fatal("a key whose grace already elapsed is still live")
	}
	if !res.GraceElapsed(time.Now()) {
		t.Fatalf("GraceElapsed = false for %+v", res)
	}

	// The Authorizer retires it so the list view agrees with what ingest decided.
	if err := d.WithOrg(ctx, orgA, func(ctx context.Context, tx pgx.Tx) error {
		return ExpireGracedTx(ctx, tx, old.ID)
	}); err != nil {
		t.Fatal(err)
	}
	res, err = s.Resolve(ctx, oldSecret)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusRevoked {
		t.Errorf("status after expiry = %q, want revoked", res.Status)
	}
}

func TestRevokeIsImmediateAndIdempotent(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()

	key, secret, err := s.Issue(ctx, orgA, tag("burn"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, orgA, key.ID, AuditHook(audit.NewPG(d), audit.ActionKeyRevoke, nil)); err != nil {
		t.Fatal(err)
	}

	res, err := s.Resolve(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if res.Live(time.Now()) {
		t.Error("a revoked key is still live")
	}

	// A retried DELETE must not turn into an error.
	again, err := s.Revoke(ctx, orgA, key.ID, nil)
	if err != nil {
		t.Fatalf("second Revoke = %v, want success", err)
	}
	if again.Status != StatusRevoked {
		t.Errorf("status = %q", again.Status)
	}
	// Rotating a dead key is a conflict, not a silent re-issue.
	if _, _, _, err := s.Rotate(ctx, orgA, key.ID, time.Minute, nil); !errors.Is(err, ErrRevoked) {
		t.Errorf("Rotate on a revoked key = %v, want ErrRevoked", err)
	}
	if n := countAudit(t, d, orgA, audit.ActionKeyRevoke, key.ID); n != 1 {
		t.Errorf("audit rows for obs.key.revoke = %d, want 1", n)
	}
}

// The Collector fast path end to end: a secret becomes an org plus the tenant
// identities each backend needs.
func TestAuthorizeReturnsRoutingIdentity(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	mapper := tenants.NewPGMapper(d)
	a := NewAuthorizer(d, s, mapper, AuthorizerOptions{
		ActiveHostWindow: 15 * time.Minute,
		TouchWindow:      time.Nanosecond, // always touch, so last_used_at is observable
		CacheTTL:         30 * time.Second,
	})
	ctx := context.Background()

	_, secret, err := s.Issue(ctx, orgA, tag("collector"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	grant, err := a.Authorize(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if grant.OrgID != orgA {
		t.Errorf("grant org = %s, want %s", grant.OrgID, orgA)
	}
	// AccountID 0 is the VictoriaMetrics default account; handing it out would
	// merge a tenant into the shared bucket.
	if grant.VMAccountID == 0 {
		t.Error("grant carries AccountID 0")
	}
	if grant.VMTenantPath != tenants.VMTenantPath(grant.VMAccountID) {
		t.Errorf("tenant path = %q, inconsistent with account %d", grant.VMTenantPath, grant.VMAccountID)
	}
	if grant.ScopeOrgID != orgA {
		t.Errorf("X-Scope-OrgID = %q, want the org uuid", grant.ScopeOrgID)
	}
	if grant.CacheTTLSec != 30 {
		t.Errorf("cache_ttl_sec = %d, want 30", grant.CacheTTLSec)
	}

	// Assignment is durable and immutable: asking again yields the same tenant.
	again, err := a.Authorize(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if again.VMAccountID != grant.VMAccountID {
		t.Errorf("AccountID moved from %d to %d", grant.VMAccountID, again.VMAccountID)
	}

	// last_used_at is what tells an operator a key is still in service.
	keys := mustList(t, s, orgA)
	var touched bool
	for _, k := range keys {
		if k.ID == grant.KeyID && k.LastUsedAt != nil {
			touched = true
		}
	}
	if !touched {
		t.Error("last_used_at was not recorded")
	}
}

// Two orgs must never share a VictoriaMetrics AccountID: inside VM that would
// merge their metrics, and no Postgres policy can see it happen.
func TestOrgsGetDistinctVMTenants(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	a := NewAuthorizer(d, s, tenants.NewPGMapper(d), AuthorizerOptions{})
	ctx := context.Background()

	grants := map[string]uint32{}
	for _, org := range []string{orgA, orgB} {
		_, secret, err := s.Issue(ctx, org, tag("tenant-probe"), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		g, err := a.Authorize(ctx, secret)
		if err != nil {
			t.Fatal(err)
		}
		grants[org] = g.VMAccountID
	}
	if grants[orgA] == grants[orgB] {
		t.Fatalf("orgA and orgB share AccountID %d", grants[orgA])
	}
}

// A revoked key must stop authorizing at once, without waiting for any sweeper.
func TestAuthorizeRefusesRevokedKey(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	a := NewAuthorizer(d, s, nil, AuthorizerOptions{})
	ctx := context.Background()

	key, secret, err := s.Issue(ctx, orgA, tag("doomed"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, orgA, key.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authorize(ctx, secret); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Authorize(revoked) = %v, want ErrUnauthorized", err)
	}
}

// Authorizing a key whose grace has closed must both refuse it and retire the
// row, so ingest and the keys endpoint never disagree.
func TestAuthorizeRetiresExpiredGrace(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	a := NewAuthorizer(d, s, nil, AuthorizerOptions{})
	ctx := context.Background()

	old, oldSecret, err := s.Issue(ctx, orgA, tag("expired-grace"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Rotate(ctx, orgA, old.ID, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authorize(ctx, oldSecret); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authorize(expired) = %v, want ErrUnauthorized", err)
	}

	res, err := s.Resolve(ctx, oldSecret)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusRevoked {
		t.Errorf("status = %q, want the row retired to revoked", res.Status)
	}
}

// freshOrg creates an org nothing else has touched.
//
// The quota tests count rows that outlive a run (hosts stay "active" for the
// whole window, usage rollups for the whole month), so sharing orgA between
// them would make each run depend on the previous one. A new org per test makes
// the counts exact instead of "at least".
func freshOrg(t *testing.T) string {
	t.Helper()
	var id string
	err := adminPool(t).QueryRow(context.Background(),
		`INSERT INTO organizations (name, plan_code) VALUES ($1, 'free') RETURNING id::text`,
		tag("quota-org")).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedHosts registers n hosts as having just reported.
func seedHosts(t *testing.T, d *db.DB, orgID string, n int) {
	t.Helper()
	err := d.WithOrg(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		for i := 0; i < n; i++ {
			_, err := tx.Exec(ctx,
				`INSERT INTO observability_hosts (org_id, host_ident, service, env)
				 VALUES (current_setting('app.current_org')::uuid, $1, 'api', 'prod')
				 ON CONFLICT (org_id, host_ident) DO UPDATE SET last_seen_at = now()`,
				fmt.Sprintf("%s-host-%d", runToken, i))
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// seedIngest records bytes against the current month for one signal.
func seedIngest(t *testing.T, d *db.DB, orgID string, bytes int64) {
	t.Helper()
	err := d.WithOrg(context.Background(), orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO observability_usage_rollups
			   (org_id, period_start, period_end, signal, ingested_bytes)
			 VALUES (current_setting('app.current_org')::uuid,
			         date_trunc('month', now()),
			         date_trunc('month', now()) + interval '1 month',
			         'metrics', $1)
			 ON CONFLICT (org_id, period_start, signal)
			 DO UPDATE SET ingested_bytes = $1`, bytes)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Quota is metered on two axes (design HOW-4/HOW-6): active hosts as the primary
// meter, monthly ingest volume as the secondary guard.
func TestQuotaSnapshotCountsBothMeters(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()
	org := freshOrg(t)

	seedHosts(t, d, org, 3)
	seedIngest(t, d, org, 2_000_000_000) // 2 GB

	q, err := s.Snapshot(ctx, org, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActiveHosts != 3 {
		t.Errorf("active_hosts = %d, want 3", q.ActiveHosts)
	}
	if q.IngestGB < 1.9 || q.IngestGB > 2.1 {
		t.Errorf("ingest_gb = %v, want ~2", q.IngestGB)
	}
	// Seeded orgs are on the free plan (migration 0007: 5 hosts, 10 GB).
	if q.PlanCode != "free" {
		t.Errorf("plan_code = %q, want free", q.PlanCode)
	}
	if q.HostLimit == nil || *q.HostLimit != 5 {
		t.Errorf("host_limit = %v, want 5", q.HostLimit)
	}
	if q.Overage {
		t.Errorf("3 hosts and 2 GB should sit inside the free plan: %+v", q)
	}
	if q.Period.End.Before(q.Period.Start) {
		t.Errorf("period = %v..%v", q.Period.Start, q.Period.End)
	}

	// A host that has not reported inside the window stops counting.
	tight, err := s.Snapshot(ctx, org, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if tight.ActiveHosts != 0 {
		t.Errorf("active_hosts with a 1ns window = %d, want 0", tight.ActiveHosts)
	}
}

// The confirmed policy (design section 7-1) is overage billing, not blocking:
// crossing a limit must flag the overage and keep accepting telemetry.
func TestOverQuotaStillAuthorizes(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	a := NewAuthorizer(d, s, tenants.NewPGMapper(d), AuthorizerOptions{ActiveHostWindow: 15 * time.Minute})
	ctx := context.Background()
	org := freshOrg(t)

	seedHosts(t, d, org, 9)               // the free plan allows 5
	seedIngest(t, d, org, 42_000_000_000) // the free plan allows 10 GB

	_, secret, err := s.Issue(ctx, org, tag("over-quota"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := a.Authorize(ctx, secret)
	if err != nil {
		t.Fatalf("an over-quota org was refused: %v", err)
	}
	if !grant.Quota.HostsExceeded {
		t.Errorf("hosts_exceeded = false with %d hosts against limit %v",
			grant.Quota.ActiveHosts, grant.Quota.HostLimit)
	}
	if !grant.Quota.IngestExceeded {
		t.Errorf("ingest_exceeded = false with %v GB against limit %v",
			grant.Quota.IngestGB, grant.Quota.IngestLimitGB)
	}
	if !grant.Quota.Overage {
		t.Error("overage = false while both meters are exceeded")
	}
}

// Quota is org-scoped through RLS: the hosts and bytes of one tenant must not
// show up in the snapshot of another.
func TestQuotaDoesNotLeakAcrossOrgs(t *testing.T) {
	d := setup(t)
	s := NewStore(d)
	ctx := context.Background()
	loud, quiet := freshOrg(t), freshOrg(t)

	seedHosts(t, d, loud, 4)
	seedIngest(t, d, loud, 5_000_000_000)

	q, err := s.Snapshot(ctx, quiet, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActiveHosts != 0 {
		t.Errorf("the quiet org sees %d hosts of its neighbour", q.ActiveHosts)
	}
	if q.IngestBytes != 0 {
		t.Errorf("the quiet org sees %d ingested bytes of its neighbour", q.IngestBytes)
	}
}
