package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/load-test/internal/model"
)

var ErrIllegalTransition = errors.New("illegal status transition")
var ErrNotFound = errors.New("not found")

type Store struct{ pool *pgxpool.Pool }

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) CreateLoadTest(ctx context.Context, lt *model.LoadTest) error {
	scn, err := json.Marshal(lt.Scenario)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO load_tests (project_id, target_url, scenario, vu, duration_sec, status)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, created_at`,
		lt.ProjectID, lt.TargetURL, scn, lt.VU, lt.DurationSec, lt.Status,
	).Scan(&lt.ID, &lt.CreatedAt)
}

func (s *Store) GetLoadTest(ctx context.Context, id string) (*model.LoadTest, error) {
	var lt model.LoadTest
	var scn []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, target_url, scenario, vu, duration_sec, status,
		        aborted_reason, started_at, finished_at, created_at
		 FROM load_tests WHERE id=$1`, id,
	).Scan(&lt.ID, &lt.ProjectID, &lt.TargetURL, &scn, &lt.VU, &lt.DurationSec,
		&lt.Status, &lt.AbortedReason, &lt.StartedAt, &lt.FinishedAt, &lt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(scn, &lt.Scenario); err != nil {
		return nil, err
	}
	return &lt, nil
}

func (s *Store) ListLoadTests(ctx context.Context, projectID string) ([]model.LoadTest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, target_url, vu, duration_sec, status, created_at
		 FROM load_tests WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.LoadTest
	for rows.Next() {
		var lt model.LoadTest
		if err := rows.Scan(&lt.ID, &lt.TargetURL, &lt.VU, &lt.DurationSec, &lt.Status, &lt.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, lt)
	}
	return out, rows.Err()
}

// UpdateStatus enforces the state machine before writing.
func (s *Store) UpdateStatus(ctx context.Context, id string, to model.Status, abortedReason *string) error {
	var cur model.Status
	if err := s.pool.QueryRow(ctx, `SELECT status FROM load_tests WHERE id=$1`, id).Scan(&cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !model.CanTransition(cur, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, cur, to)
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE load_tests SET status=$2, aborted_reason=COALESCE($3, aborted_reason) WHERE id=$1`,
		id, to, abortedReason)
	return err
}

func (s *Store) MarkStarted(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE load_tests SET started_at=now() WHERE id=$1 AND started_at IS NULL`, id)
	return err
}

func (s *Store) MarkFinished(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE load_tests SET finished_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) SaveResult(ctx context.Context, loadTestID string, r model.LoadTestResult) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO load_test_results
		   (load_test_id, rps_avg, latency_p50, latency_p95, latency_p99,
		    error_rate, max_vu_before_degradation, bottleneck_endpoint, metrics_ref)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (load_test_id) DO UPDATE SET
		   rps_avg=EXCLUDED.rps_avg, latency_p50=EXCLUDED.latency_p50,
		   latency_p95=EXCLUDED.latency_p95, latency_p99=EXCLUDED.latency_p99,
		   error_rate=EXCLUDED.error_rate,
		   max_vu_before_degradation=EXCLUDED.max_vu_before_degradation,
		   bottleneck_endpoint=EXCLUDED.bottleneck_endpoint`,
		loadTestID, r.RPSAvg, r.LatencyP50, r.LatencyP95, r.LatencyP99,
		r.ErrorRate, r.MaxVUBeforeDegradation, nullStr(r.BottleneckEndpoint), nullStr(r.MetricsRef))
	return err
}

func (s *Store) GetResult(ctx context.Context, loadTestID string) (*model.LoadTestResult, error) {
	var r model.LoadTestResult
	var bottleneck, ref *string
	err := s.pool.QueryRow(ctx,
		`SELECT rps_avg, latency_p50, latency_p95, latency_p99, error_rate,
		        max_vu_before_degradation, bottleneck_endpoint, metrics_ref
		 FROM load_test_results WHERE load_test_id=$1`, loadTestID,
	).Scan(&r.RPSAvg, &r.LatencyP50, &r.LatencyP95, &r.LatencyP99, &r.ErrorRate,
		&r.MaxVUBeforeDegradation, &bottleneck, &ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if bottleneck != nil {
		r.BottleneckEndpoint = *bottleneck
	}
	if ref != nil {
		r.MetricsRef = *ref
	}
	return &r, nil
}

func (s *Store) CreateDomain(ctx context.Context, d *model.VerifiedDomain) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO verified_domains (project_id, domain, method, token, status)
		 VALUES ($1,$2,$3,$4,'pending') RETURNING id, status`,
		d.ProjectID, d.Domain, d.Method, d.Token).Scan(&d.ID, &d.Status)
}

func (s *Store) GetDomain(ctx context.Context, id string) (*model.VerifiedDomain, error) {
	var d model.VerifiedDomain
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, domain, method, token, status, verified_at
		 FROM verified_domains WHERE id=$1`, id,
	).Scan(&d.ID, &d.ProjectID, &d.Domain, &d.Method, &d.Token, &d.Status, &d.VerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

func (s *Store) ListDomains(ctx context.Context, projectID string) ([]model.VerifiedDomain, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, domain, method, status, verified_at FROM verified_domains WHERE project_id=$1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.VerifiedDomain
	for rows.Next() {
		var d model.VerifiedDomain
		if err := rows.Scan(&d.ID, &d.Domain, &d.Method, &d.Status, &d.VerifiedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) MarkDomainVerified(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE verified_domains SET status='verified', verified_at=now() WHERE id=$1`, id)
	return err
}

// IsDomainVerified checks whether host (as parsed from a target URL) is a
// verified domain for the project.
func (s *Store) IsDomainVerified(ctx context.Context, projectID, host string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM verified_domains
		 WHERE project_id=$1 AND domain=$2 AND status='verified'`, projectID, host).Scan(&n)
	return n > 0, err
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// HostFromURL extracts the hostname (no port) from a target URL.
func HostFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host in url %q", raw)
	}
	return u.Hostname(), nil
}
