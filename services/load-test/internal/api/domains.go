package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/store"
)

func randToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (d Deps) createDomain(c *gin.Context) {
	var req struct {
		Domain string `json:"domain"`
		Method string `json:"method"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Domain == "" {
		writeError(c, 400, "VALIDATION_ERROR", "domain required", nil)
		return
	}
	if req.Method != "dns_txt" && req.Method != "file" {
		writeError(c, 400, "VALIDATION_ERROR", "method must be dns_txt or file", nil)
		return
	}
	dom := &model.VerifiedDomain{
		ProjectID: projectID(c), Domain: req.Domain, Method: req.Method, Token: randToken(),
	}
	if err := d.Store.CreateDomain(c, dom); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id": dom.ID, "domain": dom.Domain, "method": dom.Method, "status": dom.Status,
		"verification": gin.H{
			"record_name":  "_klaro." + dom.Domain,
			"record_value": "klaro-verify=" + dom.Token,
			"file_path":    "/klaro-challenge.txt",
			"file_content": dom.Token,
		},
	})
}

func (d Deps) listDomains(c *gin.Context) {
	items, err := d.Store.ListDomains(c, projectID(c))
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) verifyDomain(c *gin.Context) {
	dom, err := d.Store.GetDomain(c, c.Param("domainId"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "domain not found", nil)
		return
	}
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	ok, verr := d.Verifier.Verify(c, dom.Domain, dom.Method, dom.Token)
	if verr != nil {
		writeError(c, 422, "VALIDATION_ERROR", verr.Error(), nil)
		return
	}
	if !ok {
		writeError(c, 422, "VALIDATION_ERROR", "verification token not found", nil)
		return
	}
	if err := d.Store.MarkDomainVerified(c, dom.ID); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": dom.ID, "status": "verified"})
}
