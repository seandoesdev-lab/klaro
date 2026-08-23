// Package db owns the pgx pool and the tenant-scoped transaction helper.
//
// Every query that touches an org-scoped table must run inside WithOrg. RLS
// policies compare org_id against current_setting('app.current_org'), so a
// query issued outside WithOrg either errors (unset GUC) or - worse, if some
// other code path left a value behind - reads the wrong tenant. Making the
// transaction the only way to get a queryable handle keeps that from happening.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidOrgID is returned when an org identifier is not a UUID.
var ErrInvalidOrgID = errors.New("invalid org id")

// ErrRLSBypassed is returned by AssertRLSEnforced when the connected role can
// see through row-level security.
var ErrRLSBypassed = errors.New("database role bypasses row-level security")

// DB wraps a pgx pool.
type DB struct{ pool *pgxpool.Pool }

// Options configures the pool.
type Options struct {
	DSN      string
	MaxConns int32
}

// New opens the pool and verifies connectivity.
func New(ctx context.Context, opts Options) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// FromPool adapts an existing pool (tests, migration tooling).
func FromPool(p *pgxpool.Pool) *DB { return &DB{pool: p} }

// Pool exposes the raw pool for schema work that is deliberately not
// org-scoped (migrations, health checks). Do not use it for tenant data.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Close releases the pool.
func (d *DB) Close() {
	if d.pool != nil {
		d.pool.Close()
	}
}

// Ping checks liveness.
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

// AssertRLSEnforced fails when the pool is connected as a superuser or a
// BYPASSRLS role. Postgres exempts both from FORCE ROW LEVEL SECURITY, so
// running obsplane as one turns every isolation policy into a no-op while all
// the SQL still looks correct. Boot must refuse to start in that state.
func (d *DB) AssertRLSEnforced(ctx context.Context) error {
	var bypass bool
	err := d.pool.QueryRow(ctx,
		`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&bypass)
	if err != nil {
		return fmt.Errorf("inspect current role: %w", err)
	}
	if bypass {
		return fmt.Errorf("%w: connect obsplane as a NOSUPERUSER NOBYPASSRLS role", ErrRLSBypassed)
	}
	return nil
}

// WithOrg runs fn inside a transaction scoped to orgID.
//
// The org is applied with set_config(..., is_local => true) rather than a
// literal SET LOCAL because SET does not accept bind parameters; set_config
// does, so the org id can never be spliced into SQL text. is_local scopes it to
// this transaction, so a pooled connection cannot leak the setting to the next
// request that borrows it.
func (d *DB) WithOrg(ctx context.Context, orgID string, fn func(context.Context, pgx.Tx) error) error {
	if !ValidOrgID(orgID) {
		return fmt.Errorf("%w: %q", ErrInvalidOrgID, orgID)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit

	if err := SetLocalOrg(ctx, tx, orgID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// SetLocalOrg applies app.current_org to an existing transaction.
func SetLocalOrg(ctx context.Context, tx pgx.Tx, orgID string) error {
	if !ValidOrgID(orgID) {
		return fmt.Errorf("%w: %q", ErrInvalidOrgID, orgID)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_org', $1, true)`, orgID); err != nil {
		return fmt.Errorf("set app.current_org: %w", err)
	}
	return nil
}

// CurrentOrg reads back the GUC. Used by the isolation tests to prove the
// setting is transaction-local.
func CurrentOrg(ctx context.Context, tx pgx.Tx) (string, error) {
	var v string
	// current_setting(..., missing_ok => true) yields '' rather than erroring.
	err := tx.QueryRow(ctx, `SELECT current_setting('app.current_org', true)`).Scan(&v)
	return v, err
}

// ValidOrgID reports whether s is a canonical 8-4-4-4-12 hex UUID.
//
// Hand-rolled instead of pulling in a UUID module: this is the only place the
// service needs to parse one, and the format is fixed.
func ValidOrgID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
