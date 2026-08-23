// Package plans reads the per-org plan limits the observability platform
// enforces: how long each signal is kept, and what the quota baselines are.
//
// It exists so retention, the Explorer's rollup fallback and usage metering all
// read the same row the same way. Three copies of "join organizations to plans"
// would eventually disagree, and the disagreement would show up as a customer
// being able to query data the retention job already deleted.
package plans

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/platform/db"
)

// ErrNoPlan is returned for an org with no plan row to join.
var ErrNoPlan = errors.New("org has no plan")

// Plan is the observability slice of a billing plan (migration 0007).
type Plan struct {
	Code string

	// Retention per signal. Zero means "unset", which is treated as unlimited
	// rather than "delete everything" - a missing plan row must never be an
	// instruction to erase a customer's data.
	MetricsRetention time.Duration
	TracesRetention  time.Duration
	LogsRetention    time.Duration
	// RollupRetention is how long the downsampled metric series are kept. It is
	// deliberately much longer than MetricsRetention (design HOW-10): the point
	// of a rollup is to outlive the raw data it summarises.
	RollupRetention time.Duration

	// Quota baselines. nil means unlimited (the Enterprise encoding).
	MaxHosts       *int
	MaxIngestGBMon *float64
}

// Retention returns the window for one signal name.
func (p Plan) Retention(signal string) time.Duration {
	switch signal {
	case "metrics":
		return p.MetricsRetention
	case "traces":
		return p.TracesRetention
	case "logs":
		return p.LogsRetention
	default:
		return 0
	}
}

// Store reads plans for an org.
type Store struct{ db *db.DB }

// New builds a Store.
func New(d *db.DB) *Store { return &Store{db: d} }

// planSQL joins the caller org to its plan. organizations and plans are shared
// catalogue tables without RLS, hence the explicit org predicate.
const planSQL = `
SELECT p.code,
       p.obs_metrics_retention_days,
       p.obs_traces_retention_days,
       p.obs_logs_retention_days,
       p.obs_metrics_rollup_retention_days,
       p.obs_max_hosts,
       p.obs_max_ingest_gb_month
FROM organizations o
JOIN plans p ON p.code = o.plan_code
WHERE o.id = $1`

// Get reads the plan for orgID.
func (s *Store) Get(ctx context.Context, orgID string) (Plan, error) {
	if !db.ValidOrgID(orgID) {
		return Plan{}, fmt.Errorf("%w: %q", db.ErrInvalidOrgID, orgID)
	}
	var (
		p                       Plan
		metricsDays, tracesDays int
		logsDays                int
		rollupDays              *int
	)
	err := s.db.Pool().QueryRow(ctx, planSQL, orgID).Scan(
		&p.Code, &metricsDays, &tracesDays, &logsDays, &rollupDays,
		&p.MaxHosts, &p.MaxIngestGBMon,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, fmt.Errorf("%w: %s", ErrNoPlan, orgID)
	}
	if err != nil {
		return Plan{}, fmt.Errorf("read plan: %w", err)
	}
	p.MetricsRetention = days(metricsDays)
	p.TracesRetention = days(tracesDays)
	p.LogsRetention = days(logsDays)
	if rollupDays != nil {
		p.RollupRetention = days(*rollupDays)
	}
	return p, nil
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// orgsSQL lists every org with a plan. Background jobs need this: they are not
// serving a request, so there is no caller org to scope to.
const orgsSQL = `SELECT o.id::text FROM organizations o JOIN plans p ON p.code = o.plan_code ORDER BY o.id`

// Orgs lists the orgs a background job should visit.
//
// This reads the shared catalogue, not an org-scoped table, so it runs outside
// db.WithOrg on purpose. Everything the job then does per org goes back through
// WithOrg, so RLS still governs every row it touches.
func (s *Store) Orgs(ctx context.Context) ([]string, error) {
	rows, err := s.db.Pool().Query(ctx, orgsSQL)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
