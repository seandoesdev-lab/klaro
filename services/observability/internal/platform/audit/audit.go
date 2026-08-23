// Package audit records org-scoped lifecycle events in audit_logs.
//
// Design section 3 requires key issue/revoke/rotate and rule create/update/delete
// to be auditable (SOC2). Writes go through db.WithOrg, so an audit row lands
// under RLS exactly like the resource it describes - an audit trail that could
// be written across tenants would be worse than none.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/platform/db"
)

// Actions recorded by the observability control plane.
const (
	ActionKeyIssue  = "obs.key.issue"
	ActionKeyRevoke = "obs.key.revoke"
	ActionKeyRotate = "obs.key.rotate"

	ActionRuleCreate = "obs.rule.create"
	ActionRuleUpdate = "obs.rule.update"
	ActionRuleDelete = "obs.rule.delete"

	ActionDashboardCreate = "obs.dashboard.create"
	ActionDashboardUpdate = "obs.dashboard.update"
	ActionDashboardDelete = "obs.dashboard.delete"
)

// ErrInvalidEntry is returned for a structurally unusable entry.
var ErrInvalidEntry = errors.New("invalid audit entry")

// Entry is one audit record. OrgID is supplied separately by the caller's
// tenant scope, so it cannot disagree with the transaction's app.current_org.
type Entry struct {
	ActorUserID  *string
	Action       string
	ResourceType string
	ResourceID   *string
	Metadata     map[string]any
}

func (e Entry) validate() error {
	if e.Action == "" {
		return fmt.Errorf("%w: action is required", ErrInvalidEntry)
	}
	if e.ActorUserID != nil && !db.ValidOrgID(*e.ActorUserID) {
		return fmt.Errorf("%w: actor_user_id %q is not a uuid", ErrInvalidEntry, *e.ActorUserID)
	}
	if e.ResourceID != nil && !db.ValidOrgID(*e.ResourceID) {
		return fmt.Errorf("%w: resource_id %q is not a uuid", ErrInvalidEntry, *e.ResourceID)
	}
	return nil
}

// Recorder writes audit entries.
type Recorder interface {
	Record(ctx context.Context, orgID string, e Entry) error
	// RecordTx writes inside a caller-owned transaction so the audit row commits
	// or rolls back with the change it describes.
	RecordTx(ctx context.Context, tx pgx.Tx, e Entry) error
}

// PGRecorder persists to audit_logs.
type PGRecorder struct{ db *db.DB }

// NewPG builds a Postgres-backed Recorder.
func NewPG(d *db.DB) *PGRecorder { return &PGRecorder{db: d} }

const insertSQL = `
INSERT INTO audit_logs (org_id, actor_user_id, action, resource_type, resource_id, metadata)
VALUES (current_setting('app.current_org')::uuid, $1, $2, $3, $4, $5)`

// Record opens its own tenant-scoped transaction.
func (r *PGRecorder) Record(ctx context.Context, orgID string, e Entry) error {
	return r.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return r.RecordTx(ctx, tx, e)
	})
}

// RecordTx writes within an existing tenant-scoped transaction.
//
// org_id is taken from app.current_org rather than a parameter: the row is then
// guaranteed to match the transaction's tenant, and RLS WITH CHECK would reject
// it otherwise anyway.
func (r *PGRecorder) RecordTx(ctx context.Context, tx pgx.Tx, e Entry) error {
	if err := e.validate(); err != nil {
		return err
	}
	meta := e.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	if _, err := tx.Exec(ctx, insertSQL,
		e.ActorUserID, e.Action, nullStr(e.ResourceType), e.ResourceID, raw,
	); err != nil {
		return fmt.Errorf("insert audit_logs: %w", err)
	}
	return nil
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var _ Recorder = (*PGRecorder)(nil)
