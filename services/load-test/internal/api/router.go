package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/domainverify"
	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/store"
)

//go:embed web/index.html
var indexHTML []byte

type Deps struct {
	Store    *store.Store
	Queue    queue.JobQueue
	Signal   queue.Signaler
	Verifier *domainverify.Verifier
	DevToken string
}

func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	// 대시보드 UI는 인증 그룹 밖에서 같은 오리진으로 서빙(CORS 불필요)
	r.GET("/", func(c *gin.Context) { c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML) })

	// WS는 인증 그룹 밖에 등록(Task 8에서 실제 구현으로 교체)
	registerWS(r, d)

	auth := r.Group("/", authStub(d.DevToken))
	{
		auth.POST("/projects/:id/domains", d.createDomain)
		auth.GET("/projects/:id/domains", d.listDomains)
		auth.POST("/projects/:id/domains/:domainId/verify", d.verifyDomain)
		auth.POST("/projects/:id/load-tests", d.createLoadTest)
		auth.GET("/projects/:id/load-tests", d.listLoadTests)
		auth.GET("/load-tests/:id", d.getLoadTest)
		auth.POST("/load-tests/:id/abort", d.abortLoadTest)
		auth.GET("/load-tests/:id/results", d.getResults)
	}
	return r
}
