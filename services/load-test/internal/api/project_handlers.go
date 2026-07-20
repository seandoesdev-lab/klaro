package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/store"
	"github.com/klaro/load-test/internal/tenancy"
)

// listProjects — 활성 org 로 RLS 자동 스코프.
func (d Deps) listProjects(c *gin.Context) {
	items, err := d.Store.ListProjects(c, tenancy.Tx(c))
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) createProject(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		writeError(c, 400, "VALIDATION_ERROR", "name required", nil)
		return
	}
	p, err := d.Store.CreateProject(c, tenancy.Tx(c), req.Name)
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": p.ID, "name": p.Name})
}

// getProject — RLS 0건이면 404(RBAC-04).
func (d Deps) getProject(c *gin.Context) {
	p, err := d.Store.GetProject(c, tenancy.Tx(c), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "project not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

func (d Deps) updateProject(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		writeError(c, 400, "VALIDATION_ERROR", "name required", nil)
		return
	}
	err := d.Store.UpdateProject(c, tenancy.Tx(c), c.Param("id"), req.Name)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "project not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": c.Param("id"), "name": req.Name})
}

func (d Deps) deleteProject(c *gin.Context) {
	err := d.Store.DeleteProject(c, tenancy.Tx(c), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "project not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
