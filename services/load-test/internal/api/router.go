package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/auth"
	"github.com/klaro/load-test/internal/domainverify"
	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/rbac"
	"github.com/klaro/load-test/internal/store"
)

//go:embed web/index.html
var indexHTML []byte

type Deps struct {
	Store     *store.Store
	Queue     queue.JobQueue
	ScanQueue queue.ScanQueue
	SrcTokens queue.SrcTokenStore // [M-2] SAST upload token → org binding (nil = permissive dev/test)
	Signal    queue.Signaler
	Verifier  *domainverify.Verifier
	JWT       *auth.JWTManager
	Refresh   *auth.RefreshStore
	OAuth     *auth.OAuthManager
	DevToken  string
	AppEnv    string // "dev" 에서만 dev-token 활성(D-1)
}

func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/", func(c *gin.Context) { c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML) })

	// WS는 인증 그룹 밖(실시간 스트림). 공유 리포트/ingest 도 특례 경로.
	registerWS(r, d)
	r.POST("/projects/:id/apm/ingest", d.authenticateIngest, d.tenancyTx, d.apmIngest)
	r.GET("/shared/:slug", d.getSharedReport)

	// ── 인증(public) ──────────────────────────────────────────────────────────
	av := r.Group("/v1/auth")
	{
		av.POST("/signup", d.signup)
		av.POST("/login", d.login)
		av.POST("/refresh", d.refresh)
		av.POST("/logout", d.logout)
		av.GET("/oauth/:provider", d.oauthStart)
		av.GET("/oauth/:provider/callback", d.oauthCallback)
	}

	// ── org 목록/생성 (authenticate 만; org 스코프 미확정) ─────────────────────────
	ao := r.Group("/v1", d.authenticate)
	{
		ao.GET("/orgs", d.listOrgs)
		ao.POST("/orgs", d.createOrg)
	}

	// ── org 스코프(authenticate → resolveOrg → tenancyTx; 라우트별 authorize) ─────
	V := rbac.RoleViewer
	M := rbac.RoleMember
	A := rbac.RoleAdmin
	g := r.Group("/", d.authenticate, d.resolveOrg, d.tenancyTx)
	{
		// 멤버 (v1)
		g.GET("/v1/orgs/:orgId/members", d.authorize(M), d.listMembers)
		g.POST("/v1/orgs/:orgId/members", d.authorize(A), d.addMember)
		g.PATCH("/v1/orgs/:orgId/members/:userId", d.authorize(A), d.updateMember)
		g.DELETE("/v1/orgs/:orgId/members/:userId", d.authorize(A), d.removeMember)
		// API Key (v1)
		g.GET("/v1/orgs/:orgId/api-keys", d.authorize(A), d.listApiKeys)
		g.POST("/v1/orgs/:orgId/api-keys", d.authorize(A), d.createApiKey)
		g.DELETE("/v1/orgs/:orgId/api-keys/:id", d.authorize(A), d.revokeApiKey)
		// 프로젝트 (v1)
		g.GET("/v1/projects", d.authorize(V), d.listProjects)
		g.POST("/v1/projects", d.authorize(M), d.createProject)
		g.GET("/v1/projects/:id", d.authorize(V), d.getProject)
		g.PATCH("/v1/projects/:id", d.authorize(M), d.updateProject)
		g.DELETE("/v1/projects/:id", d.authorize(A), d.deleteProject)

		// ── 기존 리소스 라우트(경로 유지, X-Org-Id 필수) ──────────────────────────
		g.POST("/projects/:id/domains", d.authorize(M), d.createDomain)
		g.GET("/projects/:id/domains", d.authorize(V), d.listDomains)
		g.POST("/projects/:id/domains/:domainId/verify", d.authorize(M), d.verifyDomain)
		g.POST("/projects/:id/load-tests", d.authorize(M), d.createLoadTest)
		g.GET("/projects/:id/load-tests", d.authorize(V), d.listLoadTests)
		g.GET("/load-tests/:id", d.authorize(V), d.getLoadTest)
		g.POST("/load-tests/:id/abort", d.authorize(M), d.abortLoadTest)
		g.GET("/load-tests/:id/results", d.authorize(V), d.getResults)
		g.POST("/projects/:id/scans/source", d.authorize(M), d.uploadScanSource)
		g.POST("/projects/:id/scans", d.authorize(M), d.createScan)
		g.GET("/projects/:id/scans", d.authorize(V), d.listScans)
		g.GET("/scans/:id", d.authorize(V), d.getScan)
		g.GET("/scans/:id/findings", d.authorize(V), d.listFindings)
		g.PATCH("/scans/:id/findings/:findingId", d.authorize(M), d.updateFinding)

		// APM (S3)
		g.POST("/projects/:id/apm/agents", d.authorize(M), d.createAgent)
		g.GET("/projects/:id/apm/agents", d.authorize(V), d.listAgents)
		g.GET("/projects/:id/apm/traces", d.authorize(V), d.listSlowTraces)
		g.GET("/projects/:id/apm/traces/:traceId", d.authorize(V), d.getTrace)
		g.GET("/projects/:id/apm/logs", d.authorize(V), d.listApmLogs)
		g.POST("/projects/:id/apm/demo", d.authorize(M), d.seedApmDemo)

		// Reports (S4)
		g.POST("/projects/:id/reports", d.authorize(M), d.createReport)
		g.GET("/projects/:id/reports", d.authorize(V), d.listReports)
		g.GET("/reports/:id", d.authorize(V), d.getReport)
		g.POST("/reports/:id/share", d.authorize(M), d.createShare)
		g.DELETE("/reports/:id/share/:shareId", d.authorize(M), d.revokeShare)
	}
	return r
}
