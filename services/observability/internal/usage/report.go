// Package usage meters what an org sends and publishes the billing signal
// (BILL-03, design HOW-6 and section 4.5).
//
// The gateway is the only place that sees all three signals - metrics also
// reach the control plane as a live replica, but traces and logs go straight to
// storage - so volume is counted there and reported here. Counting only what
// the control plane happens to see would undercount two signals out of three.
//
// Crossing a quota does not block anything. The confirmed policy (design
// section 7-1) is overage billing on every plan: this package measures, and the
// measurement is what turns an overage into an invoice line rather than a
// dropped trace.
package usage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/platform/db"
)

// Signals that can be metered. They match the CHECK on
// observability_usage_rollups.
var Signals = map[string]bool{"metrics": true, "traces": true, "logs": true}

// ErrInvalidReport is returned for a report that cannot be attributed.
var ErrInvalidReport = errors.New("invalid usage report")

// maxHostsPerReport bounds one report. A gateway flush covers a short window,
// so a report naming tens of thousands of hosts is a bug or an attack, not a
// customer.
const maxHostsPerReport = 5000

// SignalUsage is the volume counted for one signal.
type SignalUsage struct {
	Bytes int64 `json:"bytes"`
	Items int64 `json:"items"`
}

// Host is one reporting instance, keyed by the normalised service.instance.id
// the SDK sets (design HOW-4).
type Host struct {
	Ident   string `json:"host_ident"`
	Service string `json:"service,omitempty"`
	Env     string `json:"env,omitempty"`
}

// Report is what the gateway posts to /internal/usage.
type Report struct {
	OrgID   string                 `json:"org_id"`
	Signals map[string]SignalUsage `json:"signals"`
	Hosts   []Host                 `json:"hosts,omitempty"`
}

// Validate checks a report can be attributed and stored.
func (r Report) Validate() error {
	if !db.ValidOrgID(r.OrgID) {
		return fmt.Errorf("%w: org_id must be a uuid", ErrInvalidReport)
	}
	for signal := range r.Signals {
		if !Signals[signal] {
			return fmt.Errorf("%w: unknown signal %q", ErrInvalidReport, signal)
		}
	}
	if len(r.Hosts) > maxHostsPerReport {
		return fmt.Errorf("%w: at most %d hosts per report", ErrInvalidReport, maxHostsPerReport)
	}
	for _, h := range r.Hosts {
		if h.Ident == "" || len(h.Ident) > 512 {
			return fmt.Errorf("%w: host_ident is required and must be at most 512 characters", ErrInvalidReport)
		}
	}
	return nil
}

// Store accumulates usage in Postgres.
type Store struct{ db *db.DB }

// New builds a Store.
func New(d *db.DB) *Store { return &Store{db: d} }

// accumulateSQL adds volume to the current month's row for one signal.
//
// The period is derived in SQL from now() rather than passed in, so a gateway
// with a skewed clock cannot file usage against the wrong month.
const accumulateSQL = `
INSERT INTO observability_usage_rollups
  (org_id, period_start, period_end, signal, ingested_bytes, series_count)
VALUES (current_setting('app.current_org')::uuid,
        date_trunc('month', now()),
        date_trunc('month', now()) + interval '1 month',
        $1, $2, $3)
ON CONFLICT (org_id, period_start, signal) DO UPDATE
SET ingested_bytes = observability_usage_rollups.ingested_bytes + EXCLUDED.ingested_bytes,
    series_count   = coalesce(observability_usage_rollups.series_count, 0) + EXCLUDED.series_count`

// upsertHostSQL registers a host as having just reported.
//
// This is what makes the host quota real: before it existed, the active-host
// count read an empty table and every org looked idle.
const upsertHostSQL = `
INSERT INTO observability_hosts (org_id, host_ident, service, env)
VALUES (current_setting('app.current_org')::uuid, $1, nullif($2, ''), nullif($3, ''))
ON CONFLICT (org_id, host_ident) DO UPDATE
SET last_seen_at = now(),
    service = coalesce(nullif(EXCLUDED.service, ''), observability_hosts.service),
    env     = coalesce(nullif(EXCLUDED.env, ''), observability_hosts.env)`

// Record applies one report: volume onto the month's rollup, hosts onto the
// registry. Both happen in one transaction so a partial report cannot bill for
// bytes it did not attribute to a host, or vice versa.
func (s *Store) Record(ctx context.Context, r Report) error {
	if err := r.Validate(); err != nil {
		return err
	}
	return s.db.WithOrg(ctx, r.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		for signal, usage := range r.Signals {
			if usage.Bytes == 0 && usage.Items == 0 {
				continue
			}
			if _, err := tx.Exec(ctx, accumulateSQL, signal, usage.Bytes, usage.Items); err != nil {
				return fmt.Errorf("accumulate %s usage: %w", signal, err)
			}
		}
		for _, h := range r.Hosts {
			if _, err := tx.Exec(ctx, upsertHostSQL, h.Ident, h.Service, h.Env); err != nil {
				return fmt.Errorf("register host: %w", err)
			}
		}
		return nil
	})
}

// Period is a metering window.
type Period struct {
	Start time.Time
	End   time.Time
}

// DB exposes the handle the Emitter needs. It stays inside the package boundary
// rather than widening what callers outside it can reach.
func (s *Store) DB() *db.DB { return s.db }
