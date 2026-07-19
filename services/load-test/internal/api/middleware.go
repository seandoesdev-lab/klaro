package api

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// devProjectID is the fixed project seeded by migration 0001.
const devProjectID = "00000000-0000-0000-0000-000000000002"

// authStub accepts a single dev bearer token and injects the fixed project.
// Replace with JWT/OAuth/RBAC in production (swap point).
func authStub(devToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		tok := strings.TrimPrefix(h, "Bearer ")
		if tok == "" || tok != devToken {
			writeError(c, 401, "UNAUTHENTICATED", "missing or invalid token", nil)
			return
		}
		c.Set("project_id", devProjectID)
		c.Next()
	}
}

func projectID(c *gin.Context) string {
	v, _ := c.Get("project_id")
	s, _ := v.(string)
	return s
}
