package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/load-test/internal/model"
)

// CreateAgent registers an APM agent, minting an ingest token.
func (s *Store) CreateAgent(ctx context.Context, a *model.ApmAgent) error {
	if a.IngestToken == "" {
		a.IngestToken = model.NewIngestToken()
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO apm_agents (project_id, language, ingest_token)
		 VALUES ($1,$2,$3) RETURNING id, created_at`,
		a.ProjectID, a.Language, a.IngestToken,
	).Scan(&a.ID, &a.CreatedAt)
}

func (s *Store) ListAgents(ctx context.Context, projectID string) ([]model.ApmAgent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, language, last_seen_at, created_at
		 FROM apm_agents WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ApmAgent
	for rows.Next() {
		var a model.ApmAgent
		if err := rows.Scan(&a.ID, &a.Language, &a.LastSeenAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// TouchAgent validates that token belongs to projectID, updates last_seen_at,
// and returns the agent. ErrNotFound if no such (project, token) pair.
func (s *Store) TouchAgent(ctx context.Context, projectID, token string) (*model.ApmAgent, error) {
	var a model.ApmAgent
	err := s.pool.QueryRow(ctx,
		`UPDATE apm_agents SET last_seen_at=now()
		 WHERE project_id=$1 AND ingest_token=$2
		 RETURNING id, project_id, language, last_seen_at, created_at`,
		projectID, token,
	).Scan(&a.ID, &a.ProjectID, &a.Language, &a.LastSeenAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// InsertSpans bulk-inserts spans for a project.
func (s *Store) InsertSpans(ctx context.Context, spans []model.ApmSpan) error {
	for i := range spans {
		sp := &spans[i]
		_, err := s.pool.Exec(ctx,
			`INSERT INTO apm_spans
			   (project_id, service, trace_id, span_id, parent_span_id, name, duration_ms, status, ts)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			sp.ProjectID, sp.Service, sp.TraceID, sp.SpanID, sp.ParentSpanID,
			sp.Name, sp.DurationMs, defStr(sp.Status, "ok"), sp.Ts)
		if err != nil {
			return err
		}
	}
	return nil
}

// InsertLogs bulk-inserts log lines for a project.
func (s *Store) InsertLogs(ctx context.Context, logs []model.ApmLog) error {
	for i := range logs {
		lg := &logs[i]
		_, err := s.pool.Exec(ctx,
			`INSERT INTO apm_logs (project_id, level, message, ts)
			 VALUES ($1,$2,$3,$4)`,
			lg.ProjectID, defStr(lg.Level, "info"), lg.Message, lg.Ts)
		if err != nil {
			return err
		}
	}
	return nil
}

// SlowTraces returns spans with duration_ms >= minMs, newest first.
func (s *Store) SlowTraces(ctx context.Context, projectID string, minMs float64) ([]model.ApmSpan, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, service, trace_id, span_id, parent_span_id, name, duration_ms, status, ts
		 FROM apm_spans
		 WHERE project_id=$1 AND duration_ms >= $2
		 ORDER BY ts DESC, duration_ms DESC
		 LIMIT 200`, projectID, minMs)
	if err != nil {
		return nil, err
	}
	return scanSpans(rows)
}

// TraceByID returns every span of a trace (the span tree, parent order first).
func (s *Store) TraceByID(ctx context.Context, projectID, traceID string) ([]model.ApmSpan, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, service, trace_id, span_id, parent_span_id, name, duration_ms, status, ts
		 FROM apm_spans
		 WHERE project_id=$1 AND trace_id=$2
		 ORDER BY (parent_span_id IS NOT NULL), ts ASC`, projectID, traceID)
	if err != nil {
		return nil, err
	}
	return scanSpans(rows)
}

// ListLogs returns the newest logs for a project up to limit.
func (s *Store) ListLogs(ctx context.Context, projectID string, limit int) ([]model.ApmLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, level, message, ts
		 FROM apm_logs WHERE project_id=$1 ORDER BY ts DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ApmLog
	for rows.Next() {
		var lg model.ApmLog
		if err := rows.Scan(&lg.ID, &lg.Level, &lg.Message, &lg.Ts); err != nil {
			return nil, err
		}
		out = append(out, lg)
	}
	return out, rows.Err()
}

func scanSpans(rows pgx.Rows) ([]model.ApmSpan, error) {
	defer rows.Close()
	var out []model.ApmSpan
	for rows.Next() {
		var sp model.ApmSpan
		if err := rows.Scan(&sp.ID, &sp.Service, &sp.TraceID, &sp.SpanID,
			&sp.ParentSpanID, &sp.Name, &sp.DurationMs, &sp.Status, &sp.Ts); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

func defStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
