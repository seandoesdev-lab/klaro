// Package tenancy holds the org-scoped request context helpers: the resolved
// Principal (who + which org + role) and the org-scoped pgx.Tx that RLS rides on.
package tenancy

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// AuthType distinguishes how a request authenticated.
type AuthType string

const (
	AuthJWT    AuthType = "jwt"
	AuthAPIKey AuthType = "apikey"
	AuthDev    AuthType = "dev"
)

// Principal is the authenticated caller plus the resolved active org/role.
type Principal struct {
	UserID   string   // JWT/dev: user id. API key: 빈 문자열(유저 없음).
	OrgID    string   // resolveOrg 확정 org
	Role     string   // 활성 org 에서의 역할 (owner/admin/member/viewer)
	AuthType AuthType // jwt | apikey | dev
}

// gin.Context 키 상수 (문자열 오타 방지).
const (
	keyPrincipal = "klaro.principal"
	keyTx        = "klaro.tx"
)

// SetPrincipal / GetPrincipal — 미들웨어 간 principal 전달.
func SetPrincipal(c *gin.Context, p *Principal) { c.Set(keyPrincipal, p) }

func GetPrincipal(c *gin.Context) *Principal {
	v, ok := c.Get(keyPrincipal)
	if !ok {
		return nil
	}
	p, _ := v.(*Principal)
	return p
}

// OrgID / UserID / Role — 핸들러 편의 접근자.
func OrgID(c *gin.Context) string {
	if p := GetPrincipal(c); p != nil {
		return p.OrgID
	}
	return ""
}

func UserID(c *gin.Context) string {
	if p := GetPrincipal(c); p != nil {
		return p.UserID
	}
	return ""
}

func Role(c *gin.Context) string {
	if p := GetPrincipal(c); p != nil {
		return p.Role
	}
	return ""
}

// ProjectID 은 /projects/:id/... 리소스 경로의 project 파라미터다.
func ProjectID(c *gin.Context) string { return c.Param("id") }

// SetTx / Tx — org 스코프 트랜잭션 (RLS 관통). tenancyTx 미들웨어가 주입.
func SetTx(c *gin.Context, tx pgx.Tx) { c.Set(keyTx, tx) }

func Tx(c *gin.Context) pgx.Tx {
	v, ok := c.Get(keyTx)
	if !ok {
		return nil
	}
	tx, _ := v.(pgx.Tx)
	return tx
}
