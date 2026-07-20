package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/load-test/internal/model"
)

// jsonbArg adapts a json.RawMessage for a jsonb column: nil/empty → SQL NULL,
// otherwise a string so pgx sends it as jsonb text (not bytea).
func jsonbArg(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func (s *Store) CreateScan(ctx context.Context, q Querier, sc *model.Scan) error {
	return q.QueryRow(ctx,
		`INSERT INTO scans (org_id, project_id, type, trigger, target_url, pr_number, status, source_type, source_ref, mode)
		 VALUES (current_setting('app.current_org',true)::uuid,$1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id, status, created_at`,
		sc.ProjectID, sc.Type, sc.Trigger, sc.TargetURL, sc.PRNumber, sc.Status,
		sc.SourceType, sc.SourceRef, sc.Mode,
	).Scan(&sc.ID, &sc.Status, &sc.CreatedAt)
}

func (s *Store) GetScan(ctx context.Context, q Querier, id string) (*model.Scan, error) {
	var sc model.Scan
	err := q.QueryRow(ctx,
		`SELECT id, project_id, type, trigger, target_url, pr_number, status,
		        score, started_at, finished_at, created_at, source_type, source_ref, mode
		 FROM scans WHERE id=$1`, id,
	).Scan(&sc.ID, &sc.ProjectID, &sc.Type, &sc.Trigger, &sc.TargetURL, &sc.PRNumber,
		&sc.Status, &sc.Score, &sc.StartedAt, &sc.FinishedAt, &sc.CreatedAt,
		&sc.SourceType, &sc.SourceRef, &sc.Mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

func (s *Store) ListScans(ctx context.Context, q Querier, projectID string) ([]model.Scan, error) {
	rows, err := q.Query(ctx,
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
func (s *Store) UpdateScanStatus(ctx context.Context, q Querier, id string, to model.ScanStatus, score *int) error {
	var cur model.ScanStatus
	if err := q.QueryRow(ctx, `SELECT status FROM scans WHERE id=$1`, id).Scan(&cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !model.CanScanTransition(cur, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, cur, to)
	}
	_, err := q.Exec(ctx,
		`UPDATE scans SET
		   status=$2,
		   score=COALESCE($3, score),
		   started_at=CASE WHEN $2='running' THEN now() ELSE started_at END,
		   finished_at=CASE WHEN $2 IN ('completed','failed') THEN now() ELSE finished_at END
		 WHERE id=$1`,
		id, to, score)
	return err
}

// SaveFindings inserts all findings for a scan in one pass (org_id from session).
func (s *Store) SaveFindings(ctx context.Context, q Querier, findings []model.ScanFinding) error {
	for i := range findings {
		f := &findings[i]
		err := q.QueryRow(ctx,
			`INSERT INTO scan_findings
			   (org_id, scan_id, rule_id, severity, title, file_path, line, finding_hash, status, ignore_reason,
			    evidence, cwe, confidence, package, package_version)
			 VALUES (current_setting('app.current_org',true)::uuid,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			 RETURNING id, created_at`,
			f.ScanID, f.RuleID, f.Severity, f.Title, f.FilePath, f.Line,
			f.FindingHash, f.Status, f.IgnoreReason,
			jsonbArg(f.Evidence), f.CWE, f.Confidence, f.Package, f.PackageVersion,
		).Scan(&f.ID, &f.CreatedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

// findingColumns is the shared SELECT list for scan_findings (incl. Phase 2 cols).
const findingColumns = `id, scan_id, rule_id, severity, title, file_path, line,
	finding_hash, status, ignore_reason, evidence, cwe, confidence, package, package_version, created_at`

func scanFinding(rows pgx.Rows, f *model.ScanFinding) error {
	return rows.Scan(&f.ID, &f.ScanID, &f.RuleID, &f.Severity, &f.Title,
		&f.FilePath, &f.Line, &f.FindingHash, &f.Status, &f.IgnoreReason,
		&f.Evidence, &f.CWE, &f.Confidence, &f.Package, &f.PackageVersion, &f.CreatedAt)
}

func (s *Store) ListFindings(ctx context.Context, q Querier, scanID string) ([]model.ScanFinding, error) {
	rows, err := q.Query(ctx,
		`SELECT `+findingColumns+`
		 FROM scan_findings WHERE scan_id=$1
		 ORDER BY created_at ASC`, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ScanFinding
	for rows.Next() {
		var f model.ScanFinding
		if err := scanFinding(rows, &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetLatestCompletedScanFindings returns the findings of the most recent completed
// scan of the same project+type, excluding excludeScanID (the in-flight scan). Used
// by the worker to carry forward triage and detect fixed findings on re-scan (#8).
// Runs inside the caller's org tx so RLS scopes it to the tenant.
func (s *Store) GetLatestCompletedScanFindings(ctx context.Context, q Querier, projectID string, typ model.ScanType, excludeScanID string) ([]model.ScanFinding, error) {
	rows, err := q.Query(ctx,
		`WITH latest AS (
		     SELECT id FROM scans
		      WHERE project_id=$1 AND type=$2 AND status='completed' AND id <> $3
		      ORDER BY created_at DESC LIMIT 1
		 )
		 SELECT `+findingColumns+`
		   FROM scan_findings
		  WHERE scan_id IN (SELECT id FROM latest)
		  ORDER BY created_at ASC`, projectID, typ, excludeScanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ScanFinding
	for rows.Next() {
		var f model.ScanFinding
		if err := scanFinding(rows, &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpdateFindingStatus triages a single finding (open/ignored/fixed).
func (s *Store) UpdateFindingStatus(ctx context.Context, q Querier, id string, status model.FindingStatus, reason *string) error {
	tag, err := q.Exec(ctx,
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
