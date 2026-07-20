package model

import "time"

// User is a global (tenant-agnostic) identity. RLS 비대상.
type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	PasswordHash  *string   `json:"-"` // 절대 직렬화 금지
	OAuthProvider *string   `json:"oauth_provider,omitempty"`
	OAuthSub      *string   `json:"-"`
	Name          *string   `json:"name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Membership binds a user to an org with a role (RBAC-01).
type Membership struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// OrgSummary is a user's view of one of their orgs (GET /orgs).
type OrgSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// MemberSummary is a row in GET /orgs/:orgId/members.
type MemberSummary struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// ApiKey is an org-scoped CI/CLI credential. key_hash 만 저장 (AUTH-05).
type ApiKey struct {
	ID         string     `json:"id"`
	OrgID      string     `json:"-"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Project is an org-scoped container for resources.
type Project struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"-"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}
