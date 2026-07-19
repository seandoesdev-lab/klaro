package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/load-test/internal/model"
)

func (s *Store) CreateScan(ctx context.Context, sc *model.Scan) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO scans (project_id, type, trigger, target_url, pr_number, status)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, status, created_at`,
		sc.ProjectID, sc.Type, sc.Trigger, sc.TargetURL, sc.PRNumber, sc.Status,
	).Scan(&sc.ID, &sc.Status, &sc.CreatedAt)
}

func (s *Store) GetScan(ctx context.Context, id string) (*model.Scan, error) {
	var sc model.Scan
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, type, trigger, target_url, pr_number, status,
		        score, started_at, finished_at, created_at
		 FROM scans WHERE id=$1`, id,
	).Scan(&sc.ID, &sc.ProjectID, &sc.Type, &sc.Trigger, &sc.TargetURL, &sc.PRNumber,
		&sc.Status, &sc.Score, &sc.StartedAt, &sc.FinishedAt, &sc.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

func (s *Store) ListScans(ctx context.Context, projectID string) ([]model.Scan, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, type, trigger, target_url, pr_number, status, score,
		        started_at, finished_at, created_at
		 FROM scans WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Scan
	for rows.Next() {
		var sc model.Scan
		if err := rows.Scan(&sc.ID, &sc.Type, &sc.Trigger, &sc.TargetURL, &sc.PRNumber,
			&sc.Status, &sc.Score, &sc.StartedAt, &sc.FinishedAt, &sc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// UpdateScanStatus enforces the scan state machine, stamps started/finished
// timestamps, and optionally records the computed score.
func (s *Store) UpdateScanStatus(ctx context.Context, id string, to model.ScanStatus, score *int) error {
	var cur model.ScanStatus
	if err := s.pool.QueryRow(ctx, `SELECT status FROM scans WHERE id=$1`, id).Scan(&cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !model.CanScanTransition(cur, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, cur, to)
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE scans SET
		   status=$2,
		   score=COALESCE($3, score),
		   started_at=CASE WHEN $2='running' THEN now() ELSE started_at END,
		   finished_at=CASE WHEN $2 IN ('completed','failed') THEN now() ELSE finished_at END
		 WHERE id=$1`,
		id, to, score)
	return err
}

// SaveFindings inserts all findings for a scan in one pass.
func (s *Store) SaveFindings(ctx context.Context, findings []model.ScanFinding) error {
	for i := range findings {
		f := &findings[i]
		err := s.pool.QueryRow(ctx,
			`INSERT INTO scan_findings
			   (scan_id, rule_id, severity, title, file_path, line, finding_hash, status, ignore_reason)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id, created_at`,
			f.ScanID, f.RuleID, f.Severity, f.Title, f.FilePath, f.Line,
			f.FindingHash, f.Status, f.IgnoreReason,
		).Scan(&f.ID, &f.CreatedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListFindings(ctx context.Context, scanID string) ([]model.ScanFinding, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, scan_id, rule_id, severity, title, file_path, line,
		        finding_hash, status, ignore_reason, created_at
		 FROM scan_findings WHERE scan_id=$1
		 ORDER BY created_at ASC`, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ScanFinding
	for rows.Next() {
		var f model.ScanFinding
		if err := rows.Scan(&f.ID, &f.ScanID, &f.RuleID, &f.Severity, &f.Title,
			&f.FilePath, &f.Line, &f.FindingHash, &f.Status, &f.IgnoreReason, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpdateFindingStatus triages a single finding (open/ignored/fixed).
func (s *Store) UpdateFindingStatus(ctx context.Context, id string, status model.FindingStatus, reason *string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE scan_findings SET status=$2, ignore_reason=$3 WHERE id=$1`,
		id, status, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
