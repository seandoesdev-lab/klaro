package ingestkey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/klaro/observability/internal/platform/db"
)

// Key statuses (migration 0001).
const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

var (
	// ErrDuplicateName is returned when an org already has a key by that name.
	ErrDuplicateName = errors.New("an observability key with that name already exists")
	// ErrNotFound is returned when no key with that id exists in the caller org.
	ErrNotFound = errors.New("observability key not found")
	// ErrRevoked is returned when an operation needs a live key but found a
	// revoked one.
	ErrRevoked = errors.New("observability key is revoked")
	// ErrInvalidName is returned for an unusable key name.
	ErrInvalidName = errors.New("invalid observability key name")
)

// maxNameLen keeps a key name short enough that the rotation rename (which
// appends the rotated key's uuid) stays comfortably inside a text column and
// legible in the UI.
const maxNameLen = 120

// ScopeLabel tags a key with the slice of the estate it is meant for. Design
// HOW-4 chose label tagging over a hierarchical key tree: it is advisory
// metadata for humans and dashboards, never an authorisation boundary.
type ScopeLabel struct {
	Service string `json:"service,omitempty"`
	Env     string `json:"env,omitempty"`
}

// Key is one observability key as the API exposes it. The secret is absent by
// construction - it is returned once, out of band, by Issue and Rotate.
type Key struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	KeyPrefix     string      `json:"key_prefix"`
	Status        string      `json:"status"`
	ScopeLabel    *ScopeLabel `json:"scope_label,omitempty"`
	LastUsedAt    *time.Time  `json:"last_used_at"`
	GraceUntil    *time.Time  `json:"grace_until,omitempty"`
	RevokedAt     *time.Time  `json:"revoked_at,omitempty"`
	RotatedFromID *string     `json:"rotated_from_id,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
}

// Store is the Postgres persistence for observability keys.
//
// Every method except Resolve runs inside db.WithOrg, so RLS scopes it to the
// caller org. Resolve is the deliberate exception and says why.
type Store struct{ db *db.DB }

// NewStore builds a Store over d.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

// keyColumns is the projection shared by every read. RLS already restricts the
// rows to the caller org, so no query here repeats the org predicate.
const keyColumns = `id, name, key_prefix, status, scope_label, last_used_at,
	grace_until, revoked_at, rotated_from_id, created_at`

func scanKey(row pgx.Row) (Key, error) {
	var (
		k         Key
		rawScope  []byte
		rotatedID *string
	)
	err := row.Scan(&k.ID, &k.Name, &k.KeyPrefix, &k.Status, &rawScope,
		&k.LastUsedAt, &k.GraceUntil, &k.RevokedAt, &rotatedID, &k.CreatedAt)
	if err != nil {
		return Key{}, err
	}
	k.RotatedFromID = rotatedID
	if len(rawScope) > 0 {
		var s ScopeLabel
		if err := json.Unmarshal(rawScope, &s); err != nil {
			return Key{}, fmt.Errorf("decode scope_label: %w", err)
		}
		if s != (ScopeLabel{}) {
			k.ScopeLabel = &s
		}
	}
	return k, nil
}

// ValidateName trims and checks a caller-supplied key name.
func ValidateName(name string) (string, error) {
	trimmed := trimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalidName)
	}
	if len(trimmed) > maxNameLen {
		return "", fmt.Errorf("%w: name must be at most %d characters", ErrInvalidName, maxNameLen)
	}
	return trimmed, nil
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

const insertSQL = `
INSERT INTO observability_keys (org_id, name, key_prefix, key_hash, scope_label, rotated_from_id)
VALUES (current_setting('app.current_org')::uuid, $1, $2, $3, $4, $5)
RETURNING ` + keyColumns

// insertKey mints a secret and writes the row. The plaintext is returned to the
// caller and never persisted.
func insertKey(ctx context.Context, tx pgx.Tx, name string, scope *ScopeLabel, rotatedFrom *string) (Key, string, error) {
	secret, err := NewSecret()
	if err != nil {
		return Key{}, "", err
	}
	prefix, err := DisplayPrefix(secret)
	if err != nil {
		return Key{}, "", err
	}
	var rawScope []byte
	if scope != nil && *scope != (ScopeLabel{}) {
		if rawScope, err = json.Marshal(scope); err != nil {
			return Key{}, "", fmt.Errorf("encode scope_label: %w", err)
		}
	}

	k, err := scanKey(tx.QueryRow(ctx, insertSQL, name, prefix, Hash(secret), rawScope, rotatedFrom))
	if err != nil {
		if isUniqueViolation(err) {
			return Key{}, "", fmt.Errorf("%w: %s", ErrDuplicateName, name)
		}
		return Key{}, "", fmt.Errorf("insert observability key: %w", err)
	}
	return k, secret, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Issue creates a new key for orgID. The returned secret is the only copy that
// will ever exist.
func (s *Store) Issue(ctx context.Context, orgID, name string, scope *ScopeLabel, after AfterWrite) (Key, string, error) {
	name, err := ValidateName(name)
	if err != nil {
		return Key{}, "", err
	}
	var (
		k      Key
		secret string
	)
	err = s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		k, secret, err = insertKey(ctx, tx, name, scope, nil)
		if err != nil {
			return err
		}
		return runAfter(ctx, tx, after, k)
	})
	if err != nil {
		return Key{}, "", err
	}
	return k, secret, nil
}

const listSQL = `SELECT ` + keyColumns + ` FROM observability_keys ORDER BY created_at DESC, id`

// List returns every key visible to orgID, newest first.
func (s *Store) List(ctx context.Context, orgID string) ([]Key, error) {
	out := []Key{}
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listSQL)
		if err != nil {
			return fmt.Errorf("list observability keys: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			k, err := scanKey(rows)
			if err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const lockSQL = `SELECT ` + keyColumns + ` FROM observability_keys WHERE id = $1 FOR UPDATE`

// renameRotatedSQL renames the superseded key so the caller-facing name can
// move to its replacement. The uuid suffix guarantees the rename cannot collide
// with UNIQUE(org_id, name), even for two rotations in the same second.
const renameRotatedSQL = `
UPDATE observability_keys
SET name        = left(name, $2) || ' (rotated ' || id::text || ')',
    grace_until = now() + make_interval(secs => $3)
WHERE id = $1
RETURNING grace_until`

// Rotate issues a replacement key and puts the old one on a grace clock.
//
// Both keys are accepted until grace elapses (design HOW-4), which is what lets
// a fleet redeploy without a gap. Immediate cutover is Revoke, not Rotate.
func (s *Store) Rotate(ctx context.Context, orgID, keyID string, grace time.Duration, after AfterWrite) (Key, string, time.Time, error) {
	if !db.ValidOrgID(keyID) {
		return Key{}, "", time.Time{}, fmt.Errorf("%w: %s", ErrNotFound, keyID)
	}
	if grace < 0 {
		return Key{}, "", time.Time{}, errors.New("rotation grace must not be negative")
	}
	var (
		fresh      Key
		secret     string
		graceUntil time.Time
	)
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		old, err := scanKey(tx.QueryRow(ctx, lockSQL, keyID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, keyID)
		}
		if err != nil {
			return fmt.Errorf("load observability key: %w", err)
		}
		if old.Status != StatusActive {
			return fmt.Errorf("%w: %s", ErrRevoked, keyID)
		}

		if err := tx.QueryRow(ctx, renameRotatedSQL,
			keyID, maxNameLen, grace.Seconds(),
		).Scan(&graceUntil); err != nil {
			return fmt.Errorf("mark rotated key: %w", err)
		}

		fresh, secret, err = insertKey(ctx, tx, old.Name, old.ScopeLabel, &old.ID)
		if err != nil {
			return err
		}
		return runAfter(ctx, tx, after, fresh)
	})
	if err != nil {
		return Key{}, "", time.Time{}, err
	}
	return fresh, secret, graceUntil, nil
}

const revokeSQL = `
UPDATE observability_keys
SET status = 'revoked', revoked_at = now(), grace_until = NULL
WHERE id = $1 AND status = 'active'
RETURNING ` + keyColumns

const existsSQL = `SELECT ` + keyColumns + ` FROM observability_keys WHERE id = $1`

// Revoke kills a key immediately - no grace. An already-revoked key is reported
// as revoked rather than as an error, so a retried DELETE stays idempotent.
func (s *Store) Revoke(ctx context.Context, orgID, keyID string, after AfterWrite) (Key, error) {
	if !db.ValidOrgID(keyID) {
		return Key{}, fmt.Errorf("%w: %s", ErrNotFound, keyID)
	}
	var k Key
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		k, err = scanKey(tx.QueryRow(ctx, revokeSQL, keyID))
		if errors.Is(err, pgx.ErrNoRows) {
			// Either it does not exist in this org, or it was revoked already.
			k, err = scanKey(tx.QueryRow(ctx, existsSQL, keyID))
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrNotFound, keyID)
			}
			return err
		}
		if err != nil {
			return fmt.Errorf("revoke observability key: %w", err)
		}
		return runAfter(ctx, tx, after, k)
	})
	if err != nil {
		return Key{}, err
	}
	return k, nil
}

// Resolution is what the ingest fast path learns from a presented secret.
type Resolution struct {
	OrgID      string
	KeyID      string
	Status     string
	GraceUntil *time.Time
	RevokedAt  *time.Time
}

// Live reports whether the key may still be used for ingest at now.
func (r Resolution) Live(now time.Time) bool {
	if r.Status != StatusActive {
		return false
	}
	// A rotated predecessor stays usable until its grace deadline passes.
	return r.GraceUntil == nil || r.GraceUntil.After(now)
}

// GraceElapsed reports a key that is still marked active but whose rotation
// grace has run out - the state ExpireGracedTx exists to clean up.
func (r Resolution) GraceElapsed(now time.Time) bool {
	return r.Status == StatusActive && r.GraceUntil != nil && !r.GraceUntil.After(now)
}

const resolveSQL = `
SELECT r_org_id, r_key_id, r_status, r_grace_until, r_revoked_at
FROM obs_resolve_ingest_key($1)`

// Resolve maps a presented secret to its org.
//
// This is the one query in the package that runs outside db.WithOrg, because it
// is the query that discovers which org to scope to - RLS cannot help before
// the org is known. It goes through obs_resolve_ingest_key (migration 0009), a
// SECURITY DEFINER function owned by a NOLOGIN role, which returns nothing but
// the org and status of one exact key_hash. There is no way to enumerate keys
// through it, and the caller must already hold the secret.
func (s *Store) Resolve(ctx context.Context, secret string) (Resolution, error) {
	if err := ValidateSecret(secret); err != nil {
		return Resolution{}, err
	}
	var r Resolution
	err := s.db.Pool().QueryRow(ctx, resolveSQL, Hash(secret)).
		Scan(&r.OrgID, &r.KeyID, &r.Status, &r.GraceUntil, &r.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resolution{}, ErrNotFound
	}
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve ingest key: %w", err)
	}
	return r, nil
}

// touchSQL rate-limits itself: last_used_at is for humans, and writing it on
// every ingest authz would turn a read path into a write storm.
const touchSQL = `
UPDATE observability_keys
SET last_used_at = now()
WHERE id = $1
  AND (last_used_at IS NULL OR last_used_at < now() - make_interval(secs => $2))`

// TouchLastUsedTx records that a key was just used, at most once per window.
func TouchLastUsedTx(ctx context.Context, tx pgx.Tx, keyID string, window time.Duration) error {
	if _, err := tx.Exec(ctx, touchSQL, keyID, window.Seconds()); err != nil {
		return fmt.Errorf("touch last_used_at: %w", err)
	}
	return nil
}

const expireSQL = `
UPDATE observability_keys
SET status = 'revoked', revoked_at = now()
WHERE id = $1 AND status = 'active' AND grace_until IS NOT NULL AND grace_until <= now()`

// ExpireGracedTx retires a rotated key whose grace window has closed.
//
// Doing it on the authz path rather than in a cron keeps the two views
// consistent: the instant ingest stops accepting a key, the list endpoint shows
// it revoked. A sweeper would leave a window where those two disagree.
func ExpireGracedTx(ctx context.Context, tx pgx.Tx, keyID string) error {
	if _, err := tx.Exec(ctx, expireSQL, keyID); err != nil {
		return fmt.Errorf("expire graced key: %w", err)
	}
	return nil
}
