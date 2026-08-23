package alerting

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

// Event states (migration 0005).
const (
	StateFiring   = "firing"
	StateResolved = "resolved"
)

// Event is one firing episode of a rule.
type Event struct {
	ID               string            `json:"id"`
	RuleID           string            `json:"rule_id"`
	State            string            `json:"state"`
	Value            *float64          `json:"value"`
	Labels           map[string]string `json:"labels,omitempty"`
	NotifiedChannels []Channel         `json:"notified_channels,omitempty"`
	StartedAt        time.Time         `json:"started_at"`
	ResolvedAt       *time.Time        `json:"resolved_at"`
}

// AfterWrite runs inside the same transaction as the change it observes, so an
// audit row can never disagree with the rule it describes.
type AfterWrite func(ctx context.Context, tx pgx.Tx, r Rule) error

func runAfter(ctx context.Context, tx pgx.Tx, after AfterWrite, r Rule) error {
	if after == nil {
		return nil
	}
	return after(ctx, tx, r)
}

// AuditHook records action against the affected rule (design section 3).
func AuditHook(rec audit.Recorder, action string, actorUserID *string) AfterWrite {
	if rec == nil {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx, r Rule) error {
		id := r.ID
		return rec.RecordTx(ctx, tx, audit.Entry{
			ActorUserID:  actorUserID,
			Action:       action,
			ResourceType: "alert_rule",
			ResourceID:   &id,
			Metadata: map[string]any{
				"name":       r.Name,
				"signal":     r.Signal,
				"query":      r.Query,
				"comparator": r.Comparator,
				"threshold":  r.Threshold,
				"enabled":    r.Enabled,
			},
		})
	}
}

// Store is the Postgres persistence for rules and events. Every method runs
// inside db.WithOrg, so RLS scopes it to the caller org.
type Store struct{ db *db.DB }

// NewStore builds a Store.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

const ruleColumns = `id, name, signal, query_spec, query, comparator, threshold,
	for_duration_sec, severity, channels, enabled, created_at, updated_at`

func scanRule(row pgx.Row) (Rule, error) {
	var (
		r           Rule
		rawSpec     []byte
		rawChannels []byte
		threshold   float64
	)
	err := row.Scan(&r.ID, &r.Name, &r.Signal, &rawSpec, &r.Query, &r.Comparator,
		&threshold, &r.ForDurationSec, &r.Severity, &rawChannels, &r.Enabled,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return Rule{}, err
	}
	r.Threshold = threshold
	if len(rawSpec) > 0 {
		if err := json.Unmarshal(rawSpec, &r.QuerySpec); err != nil {
			return Rule{}, fmt.Errorf("decode query_spec: %w", err)
		}
	}
	r.Channels = []Channel{}
	if len(rawChannels) > 0 {
		if err := json.Unmarshal(rawChannels, &r.Channels); err != nil {
			return Rule{}, fmt.Errorf("decode channels: %w", err)
		}
	}
	return r, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

const insertRuleSQL = `
INSERT INTO alert_rules (org_id, name, signal, query_spec, query, comparator, threshold,
	for_duration_sec, severity, channels, enabled)
VALUES (current_setting('app.current_org')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING ` + ruleColumns

// Create stores a new rule. The MetricsQL is rendered from the spec here, never
// taken from the caller.
func (s *Store) Create(ctx context.Context, orgID string, in Input, after AfterWrite) (Rule, error) {
	r := Rule{Enabled: true, ForDurationSec: int(defaultForDur.Seconds())}
	if err := r.Apply(in, orgID); err != nil {
		return Rule{}, err
	}
	spec, err := json.Marshal(r.QuerySpec)
	if err != nil {
		return Rule{}, fmt.Errorf("encode query_spec: %w", err)
	}
	channels, err := json.Marshal(r.Channels)
	if err != nil {
		return Rule{}, fmt.Errorf("encode channels: %w", err)
	}

	var out Rule
	err = s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanRule(tx.QueryRow(ctx, insertRuleSQL, r.Name, r.Signal, spec, r.Query,
			r.Comparator, r.Threshold, r.ForDurationSec, r.Severity, channels, r.Enabled))
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %s", ErrDuplicateName, r.Name)
			}
			return fmt.Errorf("insert alert rule: %w", err)
		}
		return runAfter(ctx, tx, after, out)
	})
	if err != nil {
		return Rule{}, err
	}
	return out, nil
}

const listRulesSQL = `SELECT ` + ruleColumns + ` FROM alert_rules ORDER BY created_at DESC, id`
const getRuleSQL = `SELECT ` + ruleColumns + ` FROM alert_rules WHERE id = $1`
const lockRuleSQL = getRuleSQL + ` FOR UPDATE`

// List returns every rule in the caller org.
func (s *Store) List(ctx context.Context, orgID string) ([]Rule, error) {
	out := []Rule{}
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listRulesSQL)
		if err != nil {
			return fmt.Errorf("list alert rules: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRule(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get reads one rule.
func (s *Store) Get(ctx context.Context, orgID, ruleID string) (Rule, error) {
	if !db.ValidOrgID(ruleID) {
		return Rule{}, fmt.Errorf("%w: %s", ErrNotFound, ruleID)
	}
	var out Rule
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanRule(tx.QueryRow(ctx, getRuleSQL, ruleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, ruleID)
		}
		return err
	})
	if err != nil {
		return Rule{}, err
	}
	return out, nil
}

const updateRuleSQL = `
UPDATE alert_rules
SET name = $2, signal = $3, query_spec = $4, query = $5, comparator = $6, threshold = $7,
    for_duration_sec = $8, severity = $9, channels = $10, enabled = $11, updated_at = now()
WHERE id = $1
RETURNING ` + ruleColumns

// Update applies a partial change. The rule is locked and re-rendered, so a
// changed spec and a stale query cannot coexist.
func (s *Store) Update(ctx context.Context, orgID, ruleID string, in Input, after AfterWrite) (Rule, error) {
	if !db.ValidOrgID(ruleID) {
		return Rule{}, fmt.Errorf("%w: %s", ErrNotFound, ruleID)
	}
	var out Rule
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanRule(tx.QueryRow(ctx, lockRuleSQL, ruleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, ruleID)
		}
		if err != nil {
			return fmt.Errorf("load alert rule: %w", err)
		}
		if err := current.Apply(in, orgID); err != nil {
			return err
		}
		spec, err := json.Marshal(current.QuerySpec)
		if err != nil {
			return fmt.Errorf("encode query_spec: %w", err)
		}
		channels, err := json.Marshal(current.Channels)
		if err != nil {
			return fmt.Errorf("encode channels: %w", err)
		}

		out, err = scanRule(tx.QueryRow(ctx, updateRuleSQL, ruleID, current.Name, current.Signal,
			spec, current.Query, current.Comparator, current.Threshold, current.ForDurationSec,
			current.Severity, channels, current.Enabled))
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %s", ErrDuplicateName, current.Name)
			}
			return fmt.Errorf("update alert rule: %w", err)
		}
		return runAfter(ctx, tx, after, out)
	})
	if err != nil {
		return Rule{}, err
	}
	return out, nil
}

const removeRuleSQL = `DELETE FROM alert_rules WHERE id = $1 RETURNING ` + ruleColumns

// Remove drops a rule. Its events go with it (ON DELETE CASCADE): an event
// history pointing at a rule nobody can read is noise, not history.
func (s *Store) Remove(ctx context.Context, orgID, ruleID string, after AfterWrite) error {
	if !db.ValidOrgID(ruleID) {
		return fmt.Errorf("%w: %s", ErrNotFound, ruleID)
	}
	return s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		gone, err := scanRule(tx.QueryRow(ctx, removeRuleSQL, ruleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, ruleID)
		}
		if err != nil {
			return fmt.Errorf("remove alert rule: %w", err)
		}
		return runAfter(ctx, tx, after, gone)
	})
}
