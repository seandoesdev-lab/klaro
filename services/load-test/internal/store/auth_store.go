package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/load-test/internal/model"
)

// ── users (전역, RLS 비대상: app/sys 어느 풀에서도 접근 가능) ─────────────────────

// GetUserByEmail 은 로그인/가입 전 부트스트랩이므로 sys 풀로 조회한다 (D-10).
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	return scanUser(s.sys.QueryRow(ctx,
		`SELECT id, email, password_hash, oauth_provider, oauth_sub, name, created_at, updated_at
		 FROM users WHERE email=$1`, email))
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	return scanUser(s.sys.QueryRow(ctx,
		`SELECT id, email, password_hash, oauth_provider, oauth_sub, name, created_at, updated_at
		 FROM users WHERE id=$1`, id))
}

func scanUser(row pgx.Row) (*model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.OAuthProvider, &u.OAuthSub,
		&u.Name, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// SignupResult carries the ids created by the atomic signup path (D-6).
type SignupResult struct {
	UserID         string
	OrgID          string
	DefaultProject string
}

// SignupWithOrg atomically creates a user + personal org + owner membership +
// default project on the sys (BYPASSRLS) pool. All-or-nothing (D-6, M-3).
func (s *Store) SignupWithOrg(ctx context.Context, email string, passwordHash *string, name *string, orgName string) (*SignupResult, error) {
	tx, err := s.sys.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var res SignupResult
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name) VALUES ($1,$2,$3) RETURNING id`,
		email, passwordHash, name).Scan(&res.UserID); err != nil {
		if isUniqueViolation(err) { // 동시 가입 email 경합 → 409 (F-3)
			return nil, ErrConflict
		}
		return nil, err
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO organizations (name) VALUES ($1) RETURNING id`, orgName).Scan(&res.OrgID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO memberships (org_id, user_id, role) VALUES ($1,$2,'owner')`,
		res.OrgID, res.UserID); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO projects (org_id, name) VALUES ($1,'default') RETURNING id`,
		res.OrgID).Scan(&res.DefaultProject); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &res, nil
}

// CreateOAuthUserWithOrg creates an OAuth-only user (password NULL) + personal
// org + owner + default project on the sys pool (D-4 신규 계정 경로).
func (s *Store) CreateOAuthUserWithOrg(ctx context.Context, email, provider, sub string, name *string) (*SignupResult, error) {
	tx, err := s.sys.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var res SignupResult
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, oauth_provider, oauth_sub, name) VALUES ($1,$2,$3,$4) RETURNING id`,
		email, provider, sub, name).Scan(&res.UserID); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO organizations (name) VALUES ($1) RETURNING id`, email).Scan(&res.OrgID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO memberships (org_id, user_id, role) VALUES ($1,$2,'owner')`,
		res.OrgID, res.UserID); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO projects (org_id, name) VALUES ($1,'default') RETURNING id`,
		res.OrgID).Scan(&res.DefaultProject); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &res, nil
}

// LinkOAuth attaches oauth_provider/oauth_sub to an existing (email) user (D-4 자동 링크).
func (s *Store) LinkOAuth(ctx context.Context, userID, provider, sub string) error {
	_, err := s.sys.Exec(ctx,
		`UPDATE users SET oauth_provider=$2, oauth_sub=$3, updated_at=now()
		 WHERE id=$1 AND oauth_provider IS NULL`, userID, provider, sub)
	return err
}

// ── memberships / orgs (부트스트랩: 스코프 확정 전 → sys 풀) ──────────────────────

// GetMembership returns the user's role in an org, ErrNotFound if none (RBAC-03).
// sys 풀: resolveOrg 는 app.current_org 확정 전 실행되므로 BYPASSRLS 필요.
func (s *Store) GetMembership(ctx context.Context, userID, orgID string) (string, error) {
	var role string
	err := s.sys.QueryRow(ctx,
		`SELECT role FROM memberships WHERE user_id=$1 AND org_id=$2`, userID, orgID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

// ListOrgsByUser lists the orgs a user belongs to with their role (GET /orgs).
func (s *Store) ListOrgsByUser(ctx context.Context, userID string) ([]model.OrgSummary, error) {
	rows, err := s.sys.Query(ctx,
		`SELECT o.id, o.name, m.role
		 FROM memberships m JOIN organizations o ON o.id = m.org_id
		 WHERE m.user_id=$1 ORDER BY o.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.OrgSummary{}
	for rows.Next() {
		var o model.OrgSummary
		if err := rows.Scan(&o.ID, &o.Name, &o.Role); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CreateOrgWithOwner creates an org + owner membership + default project for an
// existing user on the sys pool (POST /orgs, D-6).
func (s *Store) CreateOrgWithOwner(ctx context.Context, name, userID string) (string, error) {
	tx, err := s.sys.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var orgID string
	if err := tx.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO projects (org_id, name) VALUES ($1,'default')`, orgID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return orgID, nil
}

// ── members (org 스코프 → app tx) ─────────────────────────────────────────────

func (s *Store) ListMembers(ctx context.Context, q Querier, orgID string) ([]model.MemberSummary, error) {
	// memberships 는 RLS 로 활성 org 로 스코프됨. users 는 전역이라 조인 가능.
	rows, err := q.Query(ctx,
		`SELECT u.id, u.email, m.role
		 FROM memberships m JOIN users u ON u.id = m.user_id
		 WHERE m.org_id=$1 ORDER BY m.created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MemberSummary{}
	for rows.Next() {
		var m model.MemberSummary
		if err := rows.Scan(&m.UserID, &m.Email, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMemberByEmail adds an existing user (by email) to the active org (M-2).
// 미가입 email → ErrNotFound (404). 이미 멤버 → ErrConflict.
func (s *Store) AddMemberByEmail(ctx context.Context, q Querier, orgID, email, role string) (string, error) {
	var userID string
	// users 전역 조회 (RLS 무관)
	err := q.QueryRow(ctx, `SELECT id FROM users WHERE email=$1`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	tag, err := q.Exec(ctx,
		`INSERT INTO memberships (org_id, user_id, role)
		 VALUES (current_setting('app.current_org',true)::uuid,$1,$2)
		 ON CONFLICT (org_id, user_id) DO NOTHING`, userID, role)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrConflict
	}
	return userID, nil
}

// CountOwners returns the number of owner memberships in the active org (D-6).
func (s *Store) CountOwners(ctx context.Context, q Querier, orgID string) (int, error) {
	var n int
	err := q.QueryRow(ctx,
		`SELECT count(*) FROM memberships WHERE org_id=$1 AND role='owner'`, orgID).Scan(&n)
	return n, err
}

// GetMemberRole returns a member's role within the active org.
func (s *Store) GetMemberRole(ctx context.Context, q Querier, orgID, userID string) (string, error) {
	var role string
	err := q.QueryRow(ctx,
		`SELECT role FROM memberships WHERE org_id=$1 AND user_id=$2`, orgID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

func (s *Store) UpdateMemberRole(ctx context.Context, q Querier, orgID, userID, role string) error {
	tag, err := q.Exec(ctx,
		`UPDATE memberships SET role=$3 WHERE org_id=$1 AND user_id=$2`, orgID, userID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RemoveMember(ctx context.Context, q Querier, orgID, userID string) error {
	tag, err := q.Exec(ctx,
		`DELETE FROM memberships WHERE org_id=$1 AND user_id=$2`, orgID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── api_keys ──────────────────────────────────────────────────────────────────

// GetApiKeyByHash resolves an API key at authenticate time on the sys pool
// (pre-org-scope). Returns id, org_id, role plus expiry/revoke flags.
func (s *Store) GetApiKeyByHash(ctx context.Context, keyHash string) (id, orgID, role string, err error) {
	var expiresAt, revokedAt *time.Time
	err = s.sys.QueryRow(ctx,
		`SELECT id, org_id, role, expires_at, revoked_at FROM api_keys WHERE key_hash=$1`, keyHash,
	).Scan(&id, &orgID, &role, &expiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	if err != nil {
		return "", "", "", err
	}
	if revokedAt != nil {
		return "", "", "", ErrRevoked
	}
	if expiresAt != nil && time.Now().After(*expiresAt) {
		return "", "", "", ErrExpired
	}
	return id, orgID, role, nil
}

// TouchApiKey stamps last_used_at on the sys pool (auth path).
func (s *Store) TouchApiKey(ctx context.Context, id string) {
	_, _ = s.sys.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id=$1`, id)
}

func (s *Store) CreateApiKey(ctx context.Context, q Querier, name, role, keyHash string, expiresAt *time.Time) (*model.ApiKey, error) {
	k := &model.ApiKey{Name: name, Role: role, ExpiresAt: expiresAt}
	err := q.QueryRow(ctx,
		`INSERT INTO api_keys (org_id, key_hash, name, role, expires_at)
		 VALUES (current_setting('app.current_org',true)::uuid,$1,$2,$3,$4) RETURNING id, created_at`,
		keyHash, name, role, expiresAt,
	).Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		return nil, err
	}
	return k, nil
}

func (s *Store) ListApiKeys(ctx context.Context, q Querier, orgID string) ([]model.ApiKey, error) {
	rows, err := q.Query(ctx,
		`SELECT id, name, role, last_used_at, expires_at, created_at
		 FROM api_keys WHERE org_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ApiKey{}
	for rows.Next() {
		var k model.ApiKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Role, &k.LastUsedAt, &k.ExpiresAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) RevokeApiKey(ctx context.Context, q Querier, id string) error {
	tag, err := q.Exec(ctx,
		`UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── projects (org 스코프 → app tx) ────────────────────────────────────────────

func (s *Store) CreateProject(ctx context.Context, q Querier, name string) (*model.Project, error) {
	p := &model.Project{Name: name}
	err := q.QueryRow(ctx,
		`INSERT INTO projects (org_id, name)
		 VALUES (current_setting('app.current_org',true)::uuid,$1) RETURNING id, org_id, created_at`,
		name).Scan(&p.ID, &p.OrgID, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Store) ListProjects(ctx context.Context, q Querier) ([]model.Project, error) {
	rows, err := q.Query(ctx,
		`SELECT id, org_id, name, created_at FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Project{}
	for rows.Next() {
		var p model.Project
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(ctx context.Context, q Querier, id string) (*model.Project, error) {
	var p model.Project
	err := q.QueryRow(ctx,
		`SELECT id, org_id, name, created_at FROM projects WHERE id=$1`, id,
	).Scan(&p.ID, &p.OrgID, &p.Name, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) UpdateProject(ctx context.Context, q Querier, id, name string) error {
	tag, err := q.Exec(ctx, `UPDATE projects SET name=$2 WHERE id=$1`, id, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProject(ctx context.Context, q Querier, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM projects WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
