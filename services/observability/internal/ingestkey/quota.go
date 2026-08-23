package ingestkey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// bytesPerGB is the decimal gigabyte. Ingest volume is a billing quantity, and
// invoices are read in decimal units, so 10^9 - not 2^30.
const bytesPerGB = 1_000_000_000.0

// Period is the metering window a quota snapshot covers.
type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Quota is a point-in-time view of an org's observability consumption against
// its plan.
//
// Two meters, per design HOW-4 and HOW-6: active host count is primary (it is
// what the plan is priced on and what a customer can predict), monthly ingest
// volume is a secondary guard that catches a small fleet shipping an enormous
// amount of data.
//
// Neither meter blocks ingest. Section 7-1 of the design records the confirmed
// decision: crossing a limit bills as overage on every plan rather than
// rejecting telemetry. Dropping a customer's production traces at the moment
// their traffic spikes is exactly when observability matters most, so the
// limits are billing baselines and the Exceeded flags are signals for billing
// and for the UI - never a reason to answer 429 on the ingest path.
type Quota struct {
	PlanCode string `json:"plan_code,omitempty"`

	ActiveHosts int  `json:"active_hosts"`
	HostLimit   *int `json:"host_limit"`

	IngestBytes   int64    `json:"ingest_bytes"`
	IngestGB      float64  `json:"ingest_gb"`
	IngestLimitGB *float64 `json:"ingest_limit_gb"`

	Period Period `json:"period"`

	HostsExceeded  bool `json:"hosts_exceeded"`
	IngestExceeded bool `json:"ingest_exceeded"`
	// Overage records that consumption is above plan but ingest continues.
	Overage bool `json:"overage"`
}

// quotaSQL reads both meters plus the plan baselines in one round trip.
//
// The host and byte subqueries run under RLS, so they see only the caller org.
// organizations and plans are shared catalogue tables without RLS, hence the
// explicit id predicate; they are LEFT JOINed so an org that predates
// migration 0010 (or a bare dev fixture) still gets real counts with unlimited
// baselines rather than an error.
const quotaSQL = `
SELECT
  (SELECT count(*) FROM observability_hosts
     WHERE last_seen_at > now() - make_interval(secs => $1))                    AS active_hosts,
  (SELECT coalesce(sum(ingested_bytes), 0) FROM observability_usage_rollups
     WHERE period_start >= date_trunc('month', now()))                          AS ingested_bytes,
  p.code,
  p.obs_max_hosts,
  p.obs_max_ingest_gb_month,
  date_trunc('month', now())                                                    AS period_start,
  date_trunc('month', now()) + interval '1 month'                               AS period_end
FROM (SELECT 1) AS anchor
LEFT JOIN organizations o ON o.id = current_setting('app.current_org')::uuid
LEFT JOIN plans p ON p.code = o.plan_code`

// SnapshotTx computes the quota view inside an existing tenant-scoped
// transaction. activeWindow is how recently a host must have reported to count.
func SnapshotTx(ctx context.Context, tx pgx.Tx, activeWindow time.Duration) (Quota, error) {
	if activeWindow <= 0 {
		return Quota{}, errors.New("active host window must be positive")
	}
	var (
		q        Quota
		planCode *string
	)
	err := tx.QueryRow(ctx, quotaSQL, activeWindow.Seconds()).Scan(
		&q.ActiveHosts, &q.IngestBytes, &planCode,
		&q.HostLimit, &q.IngestLimitGB, &q.Period.Start, &q.Period.End,
	)
	if err != nil {
		return Quota{}, fmt.Errorf("read observability quota: %w", err)
	}
	if planCode != nil {
		q.PlanCode = *planCode
	}
	q.IngestGB = float64(q.IngestBytes) / bytesPerGB
	// A nil limit is the Enterprise "unlimited" encoding (migration 0007).
	q.HostsExceeded = q.HostLimit != nil && q.ActiveHosts > *q.HostLimit
	q.IngestExceeded = q.IngestLimitGB != nil && q.IngestGB > *q.IngestLimitGB
	q.Overage = q.HostsExceeded || q.IngestExceeded
	return q, nil
}

// Snapshot opens its own tenant-scoped transaction.
func (s *Store) Snapshot(ctx context.Context, orgID string, activeWindow time.Duration) (Quota, error) {
	var q Quota
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		q, err = SnapshotTx(ctx, tx, activeWindow)
		return err
	})
	if err != nil {
		return Quota{}, err
	}
	return q, nil
}
