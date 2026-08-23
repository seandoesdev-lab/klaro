package dashboards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/klaro/observability/internal/platform/audit"
	"github.com/klaro/observability/internal/platform/db"
)

// Errors callers distinguish.
var (
	// ErrDuplicateName is a name collision inside the org.
	ErrDuplicateName = errors.New("a dashboard with that name already exists")
	// ErrNotFound is a dashboard that does not exist in the caller org.
	ErrNotFound = errors.New("dashboard not found")
	// ErrInvalidName is an unusable dashboard name.
	ErrInvalidName = errors.New("invalid dashboard name")
)

// Dashboard is one saved layout.
type Dashboard struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Spec        Spec      `json:"spec"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Input is the caller-supplied form. Pointer fields distinguish "absent" from
// "set to empty", so PATCH can be a partial update.
type Input struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Spec        *Spec   `json:"spec"`
}

// AfterWrite runs inside the same transaction as the change it observes, so an
// audit row can never disagree with the dashboard it describes.
type AfterWrite func(ctx context.Context, tx pgx.Tx, d Dashboard) error

func runAfter(ctx context.Context, tx pgx.Tx, after AfterWrite, d Dashboard) error {
	if after == nil {
		return nil
	}
	return after(ctx, tx, d)
}

// AuditHook records action against the affected dashboard (design section 3).
//
// The panel document is not copied into the audit row: it can be tens of
// kilobytes, and what an auditor needs is who changed which dashboard when, not
// a second copy of every query in it.
func AuditHook(rec audit.Recorder, action string, actorUserID *string) AfterWrite {
	if rec == nil {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx, d Dashboard) error {
		id := d.ID
		return rec.RecordTx(ctx, tx, audit.Entry{
			ActorUserID:  actorUserID,
			Action:       action,
			ResourceType: "dashboard",
			ResourceID:   &id,
			Metadata:     map[string]any{"name": d.Name, "panels": len(d.Spec.Panels)},
		})
	}
}

// Store is the Postgres persistence. Every method runs inside db.WithOrg, so
// RLS scopes it to the caller org.
type Store struct{ db *db.DB }

// NewStore builds a Store.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

const columns = `id, name, description, spec, created_at, updated_at`

func scan(row pgx.Row) (Dashboard, error) {
	var (
		d           Dashboard
		description *string
		rawSpec     []byte
	)
	if err := row.Scan(&d.ID, &d.Name, &description, &rawSpec, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return Dashboard{}, err
	}
	if description != nil {
		d.Description = *description
	}
	if len(rawSpec) > 0 {
		if err := json.Unmarshal(rawSpec, &d.Spec); err != nil {
			return Dashboard{}, fmt.Errorf("decode spec: %w", err)
		}
	}
	if d.Spec.Panels == nil {
		// An empty document serialises as [] rather than null, so the frontend
		// can iterate without a nil check.
		d.Spec.Panels = []Panel{}
	}
	return d, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// validateName trims and bounds a caller-supplied name.
func validateName(name string) (string, error) {
	trimmed := trimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalidName)
	}
	if len(trimmed) > MaxNameLen {
		return "", fmt.Errorf("%w: name must be at most %d characters", ErrInvalidName, MaxNameLen)
	}
	return trimmed, nil
}

func trimSpace(s string) string {
	isSpace := func(b byte) bool {
		return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
	}
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

// apply overlays an input and validates the result.
func (d *Dashboard) apply(in Input) error {
	if in.Name != nil {
		name, err := validateName(*in.Name)
		if err != nil {
			return err
		}
		d.Name = name
	}
	if in.Description != nil {
		if len(*in.Description) > MaxDescription {
			return fmt.Errorf("%w: description must be at most %d characters", ErrInvalidSpec, MaxDescription)
		}
		d.Description = *in.Description
	}
	if in.Spec != nil {
		d.Spec = *in.Spec
	}
	if d.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidName)
	}
	return d.Spec.Validate()
}

const insertSQL = `
INSERT INTO dashboards (org_id, name, description, spec)
VALUES (current_setting('app.current_org')::uuid, $1, nullif($2, ''), $3)
RETURNING ` + columns

// Create stores a new dashboard.
func (s *Store) Create(ctx context.Context, orgID string, in Input, after AfterWrite) (Dashboard, error) {
	var d Dashboard
	if err := d.apply(in); err != nil {
		return Dashboard{}, err
	}
	spec, err := json.Marshal(d.Spec)
	if err != nil {
		return Dashboard{}, fmt.Errorf("encode spec: %w", err)
	}

	var out Dashboard
	err = s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scan(tx.QueryRow(ctx, insertSQL, d.Name, d.Description, spec))
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %s", ErrDuplicateName, d.Name)
			}
			return fmt.Errorf("insert dashboard: %w", err)
		}
		return runAfter(ctx, tx, after, out)
	})
	if err != nil {
		return Dashboard{}, err
	}
	return out, nil
}

const listSQL = `SELECT ` + columns + ` FROM dashboards ORDER BY name`
const getSQL = `SELECT ` + columns + ` FROM dashboards WHERE id = $1`
const lockSQL = getSQL + ` FOR UPDATE`

// List returns every dashboard in the caller org, ordered by name: a dashboard
// list is navigation, and navigation should not reorder itself as things are
// edited.
func (s *Store) List(ctx context.Context, orgID string) ([]Dashboard, error) {
	out := []Dashboard{}
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listSQL)
		if err != nil {
			return fmt.Errorf("list dashboards: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get reads one dashboard.
func (s *Store) Get(ctx context.Context, orgID, dashID string) (Dashboard, error) {
	if !db.ValidOrgID(dashID) {
		return Dashboard{}, fmt.Errorf("%w: %s", ErrNotFound, dashID)
	}
	var out Dashboard
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scan(tx.QueryRow(ctx, getSQL, dashID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, dashID)
		}
		return err
	})
	if err != nil {
		return Dashboard{}, err
	}
	return out, nil
}

const updateSQL = `
UPDATE dashboards
SET name = $2, description = nullif($3, ''), spec = $4, updated_at = now()
WHERE id = $1
RETURNING ` + columns

// Update applies a partial change. The row is locked and the merged document
// re-validated, so a PATCH cannot leave behind a spec the Explorer would refuse.
func (s *Store) Update(ctx context.Context, orgID, dashID string, in Input, after AfterWrite) (Dashboard, error) {
	if !db.ValidOrgID(dashID) {
		return Dashboard{}, fmt.Errorf("%w: %s", ErrNotFound, dashID)
	}
	var out Dashboard
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scan(tx.QueryRow(ctx, lockSQL, dashID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, dashID)
		}
		if err != nil {
			return fmt.Errorf("load dashboard: %w", err)
		}
		if err := current.apply(in); err != nil {
			return err
		}
		spec, err := json.Marshal(current.Spec)
		if err != nil {
			return fmt.Errorf("encode spec: %w", err)
		}

		out, err = scan(tx.QueryRow(ctx, updateSQL, dashID, current.Name, current.Description, spec))
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %s", ErrDuplicateName, current.Name)
			}
			return fmt.Errorf("update dashboard: %w", err)
		}
		return runAfter(ctx, tx, after, out)
	})
	if err != nil {
		return Dashboard{}, err
	}
	return out, nil
}

const removeSQL = `DELETE FROM dashboards WHERE id = $1 RETURNING ` + columns

// Remove drops a dashboard.
func (s *Store) Remove(ctx context.Context, orgID, dashID string, after AfterWrite) error {
	if !db.ValidOrgID(dashID) {
		return fmt.Errorf("%w: %s", ErrNotFound, dashID)
	}
	return s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		gone, err := scan(tx.QueryRow(ctx, removeSQL, dashID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, dashID)
		}
		if err != nil {
			return fmt.Errorf("remove dashboard: %w", err)
		}
		return runAfter(ctx, tx, after, gone)
	})
}
