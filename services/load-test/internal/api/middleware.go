package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/auth"
	"github.com/klaro/load-test/internal/rbac"
	"github.com/klaro/load-test/internal/store"
	"github.com/klaro/load-test/internal/tenancy"
)

// dev 시드 (0008): dev-token(D-1)이 매핑되는 실제 user/org.
const (
	devUserID = "00000000-0000-0000-0000-000000000003"
	devOrgID  = "00000000-0000-0000-0000-000000000001"
)

const ctxIngestAgent = "klaro.ingest_agent"

// ── 단계 1: authenticate ──────────────────────────────────────────────────────
// JWT / API Key / dev-token 을 구분해 principal.user_id(+API Key 는 org/role 선주입)를 세운다.
func (d Deps) authenticate(c *gin.Context) {
	tok := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if tok == "" {
		writeError(c, 401, "UNAUTHENTICATED", "missing bearer token", nil)
		return
	}

	// dev-token: APP_ENV=dev 에서만. 고정 project 주입 없이 실제 dev user/org 로 매핑(D-1).
	if d.AppEnv == "dev" && d.DevToken != "" && tok == d.DevToken {
		tenancy.SetPrincipal(c, &tenancy.Principal{UserID: devUserID, AuthType: tenancy.AuthDev})
		c.Next()
		return
	}

	// API Key: 접두사로 분기 → sha256 조회(sys 풀). org/role 선주입.
	if auth.IsAPIKey(tok) {
		id, orgID, role, err := d.Store.GetApiKeyByHash(c, auth.HashAPIKey(tok))
		if err != nil {
			writeError(c, 401, "UNAUTHENTICATED", "invalid or expired api key", nil)
			return
		}
		d.Store.TouchApiKey(c, id)
		tenancy.SetPrincipal(c, &tenancy.Principal{OrgID: orgID, Role: role, AuthType: tenancy.AuthAPIKey})
		c.Next()
		return
	}

	// JWT Access.
	userID, err := d.JWT.Verify(tok)
	if err != nil {
		writeError(c, 401, "UNAUTHENTICATED", "invalid or expired token", nil)
		return
	}
	tenancy.SetPrincipal(c, &tenancy.Principal{UserID: userID, AuthType: tenancy.AuthJWT})
	c.Next()
}

// ── 단계 2: resolveOrg ────────────────────────────────────────────────────────
// 경로 :orgId 또는 헤더 X-Org-Id 로 활성 org 확정 후 멤버십 검증(sys 풀, 스코프 확정 전).
func (d Deps) resolveOrg(c *gin.Context) {
	p := tenancy.GetPrincipal(c)
	if p == nil {
		writeError(c, 401, "UNAUTHENTICATED", "no principal", nil)
		return
	}

	// API Key: org 선주입. 경로 :orgId 가 있으면 일치 검증(불일치 → 404 은닉, D-8).
	if p.AuthType == tenancy.AuthAPIKey {
		if oid := c.Param("orgId"); oid != "" && oid != p.OrgID {
			writeError(c, 404, "NOT_FOUND", "organization not found", nil)
			return
		}
		c.Next()
		return
	}

	target := c.Param("orgId")
	if target == "" {
		target = c.GetHeader("X-Org-Id")
	}
	if target == "" && p.AuthType == tenancy.AuthDev {
		target = devOrgID // dev 기본 org(D-1)
	}
	if target == "" {
		writeError(c, 400, "VALIDATION_ERROR", "X-Org-Id header or :orgId required", nil)
		return
	}

	role, err := d.Store.GetMembership(c, p.UserID, target)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "organization not found", nil) // D-8 은닉
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	p.OrgID = target
	p.Role = role
	c.Next()
}

// ── 단계 3: authorize(min) ────────────────────────────────────────────────────
// 라우트 최소 역할 판정. viewer 는 GET 라우트(min=viewer)만 통과(D-5).
func (d Deps) authorize(min rbac.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := tenancy.GetPrincipal(c)
		if p == nil || !rbac.AtLeast(rbac.Role(p.Role), min) {
			writeError(c, 403, "FORBIDDEN", "insufficient role", nil)
			return
		}
		c.Next()
	}
}

// ── 단계 4: tenancyTx ─────────────────────────────────────────────────────────
// app 풀 tx + set_config('app.current_org') 로 RLS 스코프를 걸고, 응답 상태로 커밋/롤백(D-11).
func (d Deps) tenancyTx(c *gin.Context) {
	p := tenancy.GetPrincipal(c)
	if p == nil || p.OrgID == "" {
		writeError(c, 401, "UNAUTHENTICATED", "no org scope", nil)
		return
	}
	tx, err := d.Store.BeginOrg(c, p.OrgID)
	if err != nil {
		writeError(c, 500, "INTERNAL", "tx begin failed", nil)
		return
	}
	tenancy.SetTx(c, tx)

	c.Next()

	if c.IsAborted() || c.Writer.Status() >= 400 || len(c.Errors) > 0 {
		_ = tx.Rollback(c)
		return
	}
	if err := tx.Commit(c); err != nil {
		_ = tx.Rollback(c)
	}
}

// ── APM ingest 특례(D-10) ─────────────────────────────────────────────────────
// X-Ingest-Token 을 sys 풀로 agent→org 해석 후 principal.org_id 주입.
// 이후 tenancyTx(app 풀)로 전환해 org 스코프에서 span/log write.
func (d Deps) authenticateIngest(c *gin.Context) {
	token := c.GetHeader("X-Ingest-Token")
	if token == "" {
		writeError(c, 401, "UNAUTHENTICATED", "missing X-Ingest-Token", nil)
		return
	}
	agent, orgID, err := d.Store.ResolveAgentOrg(c, c.Param("id"), token)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 401, "UNAUTHENTICATED", "invalid ingest token", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	tenancy.SetPrincipal(c, &tenancy.Principal{OrgID: orgID, AuthType: tenancy.AuthAPIKey})
	c.Set(ctxIngestAgent, agent)
	c.Next()
}

// projectID returns the :id path param (the project scope of /projects/:id/*).
// RLS(활성 org)가 실제 크로스테넌트 차단을 담당한다.
func projectID(c *gin.Context) string { return c.Param("id") }
