package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/load-test/internal/model"
)

func (s *Store) CreateReport(ctx context.Context, q Querier, r *model.Report) error {
	if r.Status == "" {
		r.Status = model.ReportGenerating
	}
	return q.QueryRow(ctx,
		`INSERT INTO reports
		   (org_id, project_id, load_test_id, scan_id, performance_score, security_score, ai_summary, status)
		 VALUES (current_setting('app.current_org',true)::uuid,$1,$2,$3,$4,$5,$6,$7) RETURNING id, created_at`,
		r.ProjectID, r.LoadTestID, r.ScanID, r.PerformanceScore, r.SecurityScore,
		r.AISummary, r.Status,
	).Scan(&r.ID, &r.CreatedAt)
}

func (s *Store) GetReport(ctx context.Context, q Querier, id string) (*model.Report, error) {
	var r model.Report
	err := q.QueryRow(ctx,
		`SELECT id, project_id, load_test_id, scan_id, performance_score, security_score,
		        ai_summary, pdf_url, status, created_at
		 FROM reports WHERE id=$1`, id,
	).Scan(&r.ID, &r.ProjectID, &r.LoadTestID, &r.ScanID, &r.PerformanceScore,
		&r.SecurityScore, &r.AISummary, &r.PDFURL, &r.Status, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ListReports(ctx context.Context, q Querier, projectID string) ([]model.Report, error) {
	rows, err := q.Query(ctx,
		`SELECT id, load_test_id, scan_id, performance_score, security_score,
		        ai_summary, pdf_url, status, created_at
		 FROM reports WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Report
	for rows.Next() {
		var r model.Report
		if err := rows.Scan(&r.ID, &r.LoadTestID, &r.ScanID, &r.PerformanceScore,
			&r.SecurityScore, &r.AISummary, &r.PDFURL, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateReport writes computed scores, the mock summary, and the final status.
func (s *Store) UpdateReport(ctx context.Context, q Querier, id string, perfScore, secScore int, aiSummary string, status model.ReportStatus) error {
	tag, err := q.Exec(ctx,
		`UPDATE reports SET performance_score=$2, security_score=$3, ai_summary=$4, status=$5
		 WHERE id=$1`, id, perfScore, secScore, aiSummary, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateShare mints a unique slug and stores the share row (org_id from session).
func (s *Store) CreateShare(ctx context.Context, q Querier, reportID string, passwordHash *string, expiresAt *time.Time) (*model.ReportShare, error) {
	sh := &model.ReportShare{ReportID: reportID, PasswordHash: passwordHash, ExpiresAt: expiresAt}
	// Retry a few times on the (astronomically unlikely) slug collision.
	var err error
	for i := 0; i < 5; i++ {
		sh.Slug = model.NewSlug()
		err = q.QueryRow(ctx,
			`INSERT INTO report_shares (org_id, report_id, slug, password_hash, expires_at)
			 VALUES (current_setting('app.current_org',true)::uuid,$1,$2,$3,$4) RETURNING id, created_at`,
			reportID, sh.Slug, passwordHash, expiresAt,
		).Scan(&sh.ID, &sh.CreatedAt)
		if err == nil {
			return sh, nil
		}
	}
	return nil, err
}

// GetShareBySlug looks up a share by its public slug. Runs on the sys
// (BYPASSRLS) pool because GET /shared/:slug is public and pre-org-scope (D-10).
func (s *Store) GetShareBySlug(ctx context.Context, slug string) (*model.ReportShare, error) {
	var sh model.ReportShare
	err := s.sys.QueryRow(ctx,
		`SELECT id, report_id, slug, password_hash, expires_at, revoked_at, created_at
		 FROM report_shares WHERE slug=$1`, slug,
	).Scan(&sh.ID, &sh.ReportID, &sh.Slug, &sh.PasswordHash, &sh.ExpiresAt, &sh.RevokedAt, &sh.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sh, nil
}

// GetReportSys reads a report on the sys pool (public share flow, pre-org-scope).
func (s *Store) GetReportSys(ctx context.Context, id string) (*model.Report, error) {
	return s.GetReport(ctx, s.sys, id)
}

// RevokeShare marks a share revoked, scoped to its report.
func (s *Store) RevokeShare(ctx context.Context, q Querier, reportID, shareID string) error {
	tag, err := q.Exec(ctx,
		`UPDATE report_shares SET revoked_at=now()
		 WHERE id=$1 AND report_id=$2 AND revoked_at IS NULL`, shareID, reportID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SeverityCounts tallies findings of a scan by severity (for report security score).
func (s *Store) SeverityCounts(ctx context.Context, q Querier, scanID string) (map[model.Severity]int, error) {
	rows, err := q.Query(ctx,
		`SELECT severity, count(*) FROM scan_findings
		 WHERE scan_id=$1 AND status='open' GROUP BY severity`, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[model.Severity]int{}
	for rows.Next() {
		var sev model.Severity
		var n int
		if err := rows.Scan(&sev, &n); err != nil {
			return nil, err
		}
		out[sev] = n
	}
	return out, rows.Err()
}
