package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/load-test/internal/model"
)

var ErrIllegalTransition = errors.New("illegal status transition")
var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")
var ErrRevoked = errors.New("credential revoked")
var ErrExpired = errors.New("credential expired")

// isUniqueViolation reports whether err is a Postgres unique_violation (23505),
// used to map concurrent duplicate inserts to ErrConflict (409) instead of 500.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx. Org 스코프 쿼리는
// app.current_org 가 설정된 tx 를 주입받아 RLS 를 관통한다.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store 는 두 개의 커넥션 풀을 보유한다:
//
//	app : klaro_app  롤 (NOBYPASSRLS)  → RLS 강제. org 스코프 tx 전용.
//	sys : klaro_system 롤 (BYPASSRLS)  → 인증 전/공유/ingest 부트스트랩 전용.
type Store struct {
	app *pgxpool.Pool
	sys *pgxpool.Pool
}

// New 는 app/sys 두 DSN 으로 각각 풀을 연다. sysDSN 이 비면 appDSN 을 재사용한다
// (RLS 롤 미구성 로컬 환경 하위호환 — 이 경우 RLS 강제는 되지 않음).
func New(ctx context.Context, appDSN, sysDSN string) (*Store, error) {
	app, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		return nil, err
	}
	if sysDSN == "" {
		sysDSN = appDSN
	}
	sys, err := pgxpool.New(ctx, sysDSN)
	if err != nil {
		app.Close()
		return nil, err
	}
	return &Store{app: app, sys: sys}, nil
}

func (s *Store) Close() {
	s.app.Close()
	s.sys.Close()
}

// Sys 는 BYPASSRLS 부트스트랩 Querier 를 반환한다 (D-10).
func (s *Store) Sys() Querier { return s.sys }

// App 은 RLS 강제 풀을 반환한다 (org 스코프 tx 는 BeginOrg/RunInOrg 로 얻는다).
func (s *Store) App() Querier { return s.app }

// BeginOrg 는 app 풀에서 tx 를 시작하고 app.current_org 를 트랜잭션 로컬로 건다 (D-11).
// 호출자는 Commit/Rollback 책임을 진다 (HTTP tenancyTx 미들웨어가 사용).
func (s *Store) BeginOrg(ctx context.Context, orgID string) (pgx.Tx, error) {
	tx, err := s.app.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.current_org',$1,true)`, orgID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// RunInOrg 는 org 스코프 tx 안에서 fn 을 실행하고 자동 커밋/롤백한다 (워커·비-HTTP 경로용, D-11).
func (s *Store) RunInOrg(ctx context.Context, orgID string, fn func(pgx.Tx) error) error {
	tx, err := s.BeginOrg(ctx, orgID)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateLoadTest(ctx context.Context, q Querier, lt *model.LoadTest) error {
	scn, err := json.Marshal(lt.Scenario)
	if err != nil {
		return err
	}
	return q.QueryRow(ctx,
		`INSERT INTO load_tests (org_id, project_id, target_url, scenario, vu, duration_sec, status)
		 VALUES (current_setting('app.current_org',true)::uuid,$1,$2,$3,$4,$5,$6) RETURNING id, created_at`,
		lt.ProjectID, lt.TargetURL, scn, lt.VU, lt.DurationSec, lt.Status,
	).Scan(&lt.ID, &lt.CreatedAt)
}

func (s *Store) GetLoadTest(ctx context.Context, q Querier, id string) (*model.LoadTest, error) {
	var lt model.LoadTest
	var scn []byte
	err := q.QueryRow(ctx,
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

func (s *Store) ListLoadTests(ctx context.Context, q Querier, projectID string) ([]model.LoadTest, error) {
	rows, err := q.Query(ctx,
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
func (s *Store) UpdateStatus(ctx context.Context, q Querier, id string, to model.Status, abortedReason *string) error {
	var cur model.Status
	if err := q.QueryRow(ctx, `SELECT status FROM load_tests WHERE id=$1`, id).Scan(&cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !model.CanTransition(cur, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, cur, to)
	}
	_, err := q.Exec(ctx,
		`UPDATE load_tests SET status=$2, aborted_reason=COALESCE($3, aborted_reason) WHERE id=$1`,
		id, to, abortedReason)
	return err
}

func (s *Store) MarkStarted(ctx context.Context, q Querier, id string) error {
	_, err := q.Exec(ctx, `UPDATE load_tests SET started_at=now() WHERE id=$1 AND started_at IS NULL`, id)
	return err
}

func (s *Store) MarkFinished(ctx context.Context, q Querier, id string) error {
	_, err := q.Exec(ctx, `UPDATE load_tests SET finished_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) SaveResult(ctx context.Context, q Querier, loadTestID string, r model.LoadTestResult) error {
	_, err := q.Exec(ctx,
		`INSERT INTO load_test_results
		   (org_id, load_test_id, rps_avg, latency_p50, latency_p95, latency_p99,
		    error_rate, max_vu_before_degradation, bottleneck_endpoint, metrics_ref)
		 VALUES (current_setting('app.current_org',true)::uuid,$1,$2,$3,$4,$5,$6,$7,$8,$9)
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

func (s *Store) GetResult(ctx context.Context, q Querier, loadTestID string) (*model.LoadTestResult, error) {
	var r model.LoadTestResult
	var bottleneck, ref *string
	err := q.QueryRow(ctx,
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

func (s *Store) CreateDomain(ctx context.Context, q Querier, d *model.VerifiedDomain) error {
	return q.QueryRow(ctx,
		`INSERT INTO verified_domains (org_id, project_id, domain, method, token, status)
		 VALUES (current_setting('app.current_org',true)::uuid,$1,$2,$3,$4,'pending') RETURNING id, status`,
		d.ProjectID, d.Domain, d.Method, d.Token).Scan(&d.ID, &d.Status)
}

func (s *Store) GetDomain(ctx context.Context, q Querier, id string) (*model.VerifiedDomain, error) {
	var d model.VerifiedDomain
	err := q.QueryRow(ctx,
		`SELECT id, project_id, domain, method, token, status, verified_at
		 FROM verified_domains WHERE id=$1`, id,
	).Scan(&d.ID, &d.ProjectID, &d.Domain, &d.Method, &d.Token, &d.Status, &d.VerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

func (s *Store) ListDomains(ctx context.Context, q Querier, projectID string) ([]model.VerifiedDomain, error) {
	rows, err := q.Query(ctx,
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

func (s *Store) MarkDomainVerified(ctx context.Context, q Querier, id string) error {
	_, err := q.Exec(ctx,
		`UPDATE verified_domains SET status='verified', verified_at=now() WHERE id=$1`, id)
	return err
}

// IsDomainVerified checks whether host (as parsed from a target URL) is a
// verified domain for the project.
func (s *Store) IsDomainVerified(ctx context.Context, q Querier, projectID, host string) (bool, error) {
	var n int
	err := q.QueryRow(ctx,
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
