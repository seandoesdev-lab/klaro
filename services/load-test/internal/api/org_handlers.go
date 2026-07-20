package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/auth"
	"github.com/klaro/load-test/internal/rbac"
	"github.com/klaro/load-test/internal/store"
	"github.com/klaro/load-test/internal/tenancy"
)

// listOrgs — GET /v1/orgs. 내 멤버십 기준(sys 풀, org 스코프 미확정).
func (d Deps) listOrgs(c *gin.Context) {
	uid := tenancy.UserID(c)
	if uid == "" {
		writeError(c, 400, "VALIDATION_ERROR", "api key cannot list orgs", nil)
		return
	}
	items, err := d.Store.ListOrgsByUser(c, uid)
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// createOrg — POST /v1/orgs. owner 멤버십 + default project 동반 생성(sys 풀).
func (d Deps) createOrg(c *gin.Context) {
	uid := tenancy.UserID(c)
	if uid == "" {
		writeError(c, 400, "VALIDATION_ERROR", "api key cannot create orgs", nil)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		writeError(c, 400, "VALIDATION_ERROR", "name required", nil)
		return
	}
	orgID, err := d.Store.CreateOrgWithOwner(c, req.Name, uid)
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": orgID})
}

// ── members ───────────────────────────────────────────────────────────────────

func (d Deps) listMembers(c *gin.Context) {
	items, err := d.Store.ListMembers(c, tenancy.Tx(c), tenancy.OrgID(c))
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// addMember — 기존 user 만 email 로 즉시 멤버십 추가(M-2). 미가입 → 404.
func (d Deps) addMember(c *gin.Context) {
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Email == "" {
		writeError(c, 400, "VALIDATION_ERROR", "email required", nil)
		return
	}
	if req.Role == "" {
		req.Role = string(rbac.RoleMember)
	}
	if !rbac.Valid(rbac.Role(req.Role)) {
		writeError(c, 400, "VALIDATION_ERROR", "invalid role", nil)
		return
	}
	uid, err := d.Store.AddMemberByEmail(c, tenancy.Tx(c), tenancy.OrgID(c), req.Email, req.Role)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "user not found; ask them to sign up first", nil)
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(c, 409, "CONFLICT", "user already a member", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user_id": uid, "role": req.Role})
}

// updateMember — 역할 변경. 마지막 owner 강등 → 409(D-6).
func (d Deps) updateMember(c *gin.Context) {
	var req struct {
		Role string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !rbac.Valid(rbac.Role(req.Role)) {
		writeError(c, 400, "VALIDATION_ERROR", "valid role required", nil)
		return
	}
	tx := tenancy.Tx(c)
	orgID := tenancy.OrgID(c)
	target := c.Param("userId")
	cur, err := d.Store.GetMemberRole(c, tx, orgID, target)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "member not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	owners, err := d.Store.CountOwners(c, tx, orgID)
	if err != nil {
		writeInternal(c, err)
		return
	}
	if rbac.WouldDemoteLastOwner(owners, rbac.Role(cur), rbac.Role(req.Role)) {
		writeError(c, 409, "CONFLICT", rbac.ErrLastOwner.Error(), nil)
		return
	}
	if err := d.Store.UpdateMemberRole(c, tx, orgID, target, req.Role); err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user_id": target, "role": req.Role})
}

// removeMember — 마지막 owner 제거 → 409(D-6).
func (d Deps) removeMember(c *gin.Context) {
	tx := tenancy.Tx(c)
	orgID := tenancy.OrgID(c)
	target := c.Param("userId")
	cur, err := d.Store.GetMemberRole(c, tx, orgID, target)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "member not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	owners, err := d.Store.CountOwners(c, tx, orgID)
	if err != nil {
		writeInternal(c, err)
		return
	}
	if rbac.WouldRemoveLastOwner(owners, rbac.Role(cur)) {
		writeError(c, 409, "CONFLICT", rbac.ErrLastOwner.Error(), nil)
		return
	}
	if err := d.Store.RemoveMember(c, tx, orgID, target); err != nil {
		writeInternal(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ── api keys ────────────────────────────────────────────────────────────────

func (d Deps) listApiKeys(c *gin.Context) {
	items, err := d.Store.ListApiKeys(c, tenancy.Tx(c), tenancy.OrgID(c))
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// createApiKey — 원문 key 는 응답에서 1회만 노출(AUTH-05). 저장은 sha256 만.
func (d Deps) createApiKey(c *gin.Context) {
	var req struct {
		Name      string     `json:"name"`
		Role      string     `json:"role"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		writeError(c, 400, "VALIDATION_ERROR", "name required", nil)
		return
	}
	if req.Role == "" {
		req.Role = string(rbac.RoleMember) // D-7 기본 member
	}
	// F-5: API Key 는 member/viewer 로만 발급(관리 권한 키 금지). owner/admin 발급 차단.
	if req.Role != string(rbac.RoleMember) && req.Role != string(rbac.RoleViewer) {
		writeError(c, 400, "VALIDATION_ERROR", "api key role must be member or viewer", nil)
		return
	}
	plaintext, hash := auth.GenerateAPIKey()
	k, err := d.Store.CreateApiKey(c, tenancy.Tx(c), req.Name, req.Role, hash, req.ExpiresAt)
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id":         k.ID,
		"name":       k.Name,
		"role":       k.Role,
		"key":        plaintext, // 1회 노출
		"expires_at": k.ExpiresAt,
		"created_at": k.CreatedAt,
	})
}

func (d Deps) revokeApiKey(c *gin.Context) {
	err := d.Store.RevokeApiKey(c, tenancy.Tx(c), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "api key not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
