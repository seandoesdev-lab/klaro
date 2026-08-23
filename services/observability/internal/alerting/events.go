package alerting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/platform/db"
)

// Fingerprint identifies one firing episode: the rule plus the label set that
// fired.
//
// vmalert re-sends a firing alert every resend interval. Without a stable key
// for "this exact alert is already open", the receiver would append an event
// row every minute for as long as the problem lasted, and the history would be
// unreadable precisely when someone needed to read it.
func Fingerprint(ruleID string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	_, _ = h.Write([]byte(ruleID))
	for _, k := range keys {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{1})
		_, _ = h.Write([]byte(labels[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

const eventColumns = `id, rule_id, state, value, labels, notified_channels, started_at, resolved_at`

func scanEvent(row pgx.Row) (Event, error) {
	var (
		e           Event
		rawLabels   []byte
		rawChannels []byte
	)
	err := row.Scan(&e.ID, &e.RuleID, &e.State, &e.Value, &rawLabels, &rawChannels,
		&e.StartedAt, &e.ResolvedAt)
	if err != nil {
		return Event{}, err
	}
	if len(rawLabels) > 0 {
		if err := json.Unmarshal(rawLabels, &e.Labels); err != nil {
			return Event{}, fmt.Errorf("decode labels: %w", err)
		}
	}
	if len(rawChannels) > 0 {
		if err := json.Unmarshal(rawChannels, &e.NotifiedChannels); err != nil {
			return Event{}, fmt.Errorf("decode notified_channels: %w", err)
		}
	}
	return e, nil
}

// openEventSQL claims a firing episode.
//
// ON CONFLICT DO NOTHING against the partial unique index on open events is
// what makes re-sends free: the second insert for the same (rule, labels) while
// one is still open simply does nothing.
const openEventSQL = `
INSERT INTO alert_events (org_id, rule_id, state, value, labels, notified_channels, fingerprint)
VALUES (current_setting('app.current_org')::uuid, $1, 'firing', $2, $3, $4, $5)
ON CONFLICT (org_id, rule_id, fingerprint) WHERE resolved_at IS NULL DO NOTHING
RETURNING ` + eventColumns

// OpenFiring records a rule firing, or reports that it was already open.
//
// created is false for a re-send. Callers use it to decide whether to notify:
// an operator wants to be told once, not once a minute.
func (s *Store) OpenFiring(ctx context.Context, orgID, ruleID string, value *float64,
	labels map[string]string, channels []Channel) (event Event, created bool, err error) {
	if !db.ValidOrgID(ruleID) {
		return Event{}, false, fmt.Errorf("%w: %s", ErrNotFound, ruleID)
	}
	rawLabels, err := json.Marshal(labels)
	if err != nil {
		return Event{}, false, fmt.Errorf("encode labels: %w", err)
	}
	if channels == nil {
		channels = []Channel{}
	}
	rawChannels, err := json.Marshal(channels)
	if err != nil {
		return Event{}, false, fmt.Errorf("encode notified_channels: %w", err)
	}
	fp := Fingerprint(ruleID, labels)

	err = s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		e, err := scanEvent(tx.QueryRow(ctx, openEventSQL, ruleID, value, rawLabels, rawChannels, fp))
		switch {
		case err == nil:
			event, created = e, true
			return nil
		case errors.Is(err, pgx.ErrNoRows):
			// Already open. Fetch it so the caller still gets the event it can
			// report on, just without a second notification.
			event, err = scanEvent(tx.QueryRow(ctx,
				`SELECT `+eventColumns+` FROM alert_events
				 WHERE rule_id = $1 AND fingerprint = $2 AND resolved_at IS NULL`, ruleID, fp))
			return err
		default:
			return fmt.Errorf("open alert event: %w", err)
		}
	})
	if err != nil {
		return Event{}, false, err
	}
	return event, created, nil
}

const resolveEventSQL = `
UPDATE alert_events
SET state = 'resolved', resolved_at = now()
WHERE rule_id = $1 AND fingerprint = $2 AND resolved_at IS NULL
RETURNING ` + eventColumns

// Resolve closes an open episode. changed is false when nothing was open,
// which happens whenever vmalert resolves an alert this process never saw fire
// (a restart, or a resolve that outran its firing notification).
func (s *Store) Resolve(ctx context.Context, orgID, ruleID string,
	labels map[string]string) (event Event, changed bool, err error) {
	if !db.ValidOrgID(ruleID) {
		return Event{}, false, fmt.Errorf("%w: %s", ErrNotFound, ruleID)
	}
	fp := Fingerprint(ruleID, labels)
	err = s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		e, err := scanEvent(tx.QueryRow(ctx, resolveEventSQL, ruleID, fp))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("resolve alert event: %w", err)
		}
		event, changed = e, true
		return nil
	})
	if err != nil {
		return Event{}, false, err
	}
	return event, changed, nil
}

// EventFilter narrows an event listing.
type EventFilter struct {
	State  string
	RuleID string
	From   time.Time
	To     time.Time
	Limit  int
}

const maxEventLimit = 500

// ListEvents returns the org's alert history, newest first [OBS-07].
//
// The filters are applied in SQL with bound parameters rather than by building
// a WHERE clause from strings, so an empty filter and a hostile one produce the
// same shape of query.
const listEventsSQL = `
SELECT ` + eventColumns + ` FROM alert_events
WHERE ($1 = '' OR state = $1)
  AND ($2::uuid IS NULL OR rule_id = $2::uuid)
  AND ($3::timestamptz IS NULL OR started_at >= $3::timestamptz)
  AND ($4::timestamptz IS NULL OR started_at <= $4::timestamptz)
ORDER BY started_at DESC, id
LIMIT $5`

// ListEvents reads the event history.
func (s *Store) ListEvents(ctx context.Context, orgID string, f EventFilter) ([]Event, error) {
	if f.State != "" && f.State != StateFiring && f.State != StateResolved {
		return nil, fmt.Errorf("%w: state must be %q or %q", ErrInvalidRule, StateFiring, StateResolved)
	}
	if f.RuleID != "" && !db.ValidOrgID(f.RuleID) {
		return nil, fmt.Errorf("%w: rule_id must be a uuid", ErrInvalidRule)
	}
	if f.Limit <= 0 || f.Limit > maxEventLimit {
		f.Limit = maxEventLimit
	}

	var ruleID *string
	if f.RuleID != "" {
		ruleID = &f.RuleID
	}
	var from, to *time.Time
	if !f.From.IsZero() {
		from = &f.From
	}
	if !f.To.IsZero() {
		to = &f.To
	}

	out := []Event{}
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listEventsSQL, f.State, ruleID, from, to, f.Limit)
		if err != nil {
			return fmt.Errorf("list alert events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEvent(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
