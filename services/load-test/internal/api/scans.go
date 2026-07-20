package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/scanner"
	"github.com/klaro/load-test/internal/store"
	"github.com/klaro/load-test/internal/tenancy"
)

// maxUploadBytes caps the SAST source archive size accepted by uploadScanSource.
const maxUploadBytes = 200 << 20 // 200 MiB

func (d Deps) createScan(c *gin.Context) {
	var req struct {
		Type        string `json:"type"`
		TargetURL   string `json:"target_url"`
		Mode        string `json:"mode"`         // dast: baseline(default)|active
		RepoURL     string `json:"repo_url"`     // sast
		Ref         string `json:"ref"`          // sast (optional branch/tag)
		UploadToken string `json:"upload_token"` // sast
		PRNumber    *int   `json:"pr_number"`    // trigger label only, not a source
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}
	st := model.ScanType(req.Type)
	if st != model.ScanTypeSAST && st != model.ScanTypeDAST {
		writeError(c, 400, "VALIDATION_ERROR", "type must be sast or dast", nil)
		return
	}

	pid := projectID(c)
	tx := tenancy.Tx(c)
	orgID := tenancy.OrgID(c)
	sc := &model.Scan{ProjectID: pid, Type: st, Status: model.ScanStatusPending}
	job := model.ScanJob{OrgID: orgID, ProjectID: pid, Type: st}

	switch st {
	case model.ScanTypeDAST:
		// DAST requires a verified target domain ([SC-01]); active uses the same gate.
		if req.TargetURL == "" {
			writeError(c, 400, "VALIDATION_ERROR", "target_url required for dast", nil)
			return
		}
		host, err := store.HostFromURL(req.TargetURL)
		if err != nil {
			writeError(c, 400, "VALIDATION_ERROR", "invalid target_url", nil)
			return
		}
		verified, err := d.Store.IsDomainVerified(c, tx, pid, host)
		if err != nil {
			writeInternal(c, err)
			return
		}
		if !verified {
			writeError(c, 403, "DOMAIN_NOT_VERIFIED", "target domain must be verified", nil)
			return
		}
		sc.TargetURL = &req.TargetURL
		mode := string(scanner.ParseMode(req.Mode))
		sc.Mode = &mode
		job.TargetURL = req.TargetURL
		job.Mode = mode

	case model.ScanTypeSAST:
		// [SAST-03] source input is required: repo_url XOR upload_token.
		hasRepo := req.RepoURL != ""
		hasUpload := req.UploadToken != ""
		if hasRepo == hasUpload { // both or neither
			writeError(c, 400, "VALIDATION_ERROR", "sast requires exactly one of repo_url or upload_token", nil)
			return
		}
		if hasUpload {
			if !d.stagingExists(req.UploadToken) {
				writeError(c, 400, "VALIDATION_ERROR", "upload_token not found or expired", nil)
				return
			}
			// [M-2] the upload token must belong to the requesting org.
			if !d.uploadTokenBelongsToOrg(c, req.UploadToken, orgID) {
				writeError(c, 404, "NOT_FOUND", "upload_token not found", nil)
				return
			}
			srcType := "upload"
			sc.SourceType = &srcType
			sc.SourceRef = &req.UploadToken
			job.Source = model.ScanSource{Type: "upload", Token: req.UploadToken}
		} else {
			// [C-1/H-1] validate repo_url before enqueue: https-only + SSRF gate,
			// no option-injection. Fail fast with 400 rather than at the worker.
			if err := scanner.ValidateRepoURL(req.RepoURL); err != nil {
				writeError(c, 400, "VALIDATION_ERROR", "invalid repo_url: "+scrubValidationErr(err), nil)
				return
			}
			if err := scanner.ValidateRef(req.Ref); err != nil {
				writeError(c, 400, "VALIDATION_ERROR", "invalid ref", nil)
				return
			}
			srcType := "repo"
			sc.SourceType = &srcType
			sc.SourceRef = &req.RepoURL
			job.Source = model.ScanSource{Type: "repo", RepoURL: req.RepoURL, Ref: req.Ref}
		}
	}

	// trigger: PR-driven if a pr_number is supplied, else manual.
	if req.PRNumber != nil {
		sc.Trigger = model.ScanTriggerPR
		sc.PRNumber = req.PRNumber
	} else {
		sc.Trigger = model.ScanTriggerManual
	}

	if err := d.Store.CreateScan(c, tx, sc); err != nil {
		writeInternal(c, err)
		return
	}

	job.ScanID = sc.ID
	if err := d.ScanQueue.EnqueueScan(c, job); err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": sc.ID, "status": sc.Status})
}

// srcDir returns the shared tmpfs staging root for SAST upload archives.
func (d Deps) srcDir() string {
	if v := os.Getenv("SCAN_SRC_DIR"); v != "" {
		return v
	}
	return "/scan-src"
}

// stagingExists reports whether an upload staging file for token is present.
func (d Deps) stagingExists(token string) bool {
	if token == "" || strings.ContainsAny(token, "/\\.") {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(d.srcDir(), token+".*"))
	return len(matches) > 0
}

// uploadTokenBelongsToOrg checks the [M-2] token→org binding. When no token store
// is wired (dev/test), it is permissive; production wires SrcTokens so a token is
// only usable by the org that uploaded it.
func (d Deps) uploadTokenBelongsToOrg(c *gin.Context, token, orgID string) bool {
	if d.SrcTokens == nil {
		return true
	}
	owner, err := d.SrcTokens.GetSrcToken(c, token)
	if err != nil || owner == "" {
		return false
	}
	return owner == orgID
}

// scrubValidationErr returns a client-safe message for a repo_url rejection,
// stripping the wrapped internal detail while keeping the reason category.
func scrubValidationErr(err error) string {
	if errors.Is(err, scanner.ErrBlockedRepoURL) {
		msg := err.Error()
		if i := strings.Index(msg, ": "); i >= 0 {
			return msg[i+2:]
		}
	}
	return "rejected"
}

// uploadScanSource streams a multipart tar/zip SAST source archive into the shared
// tmpfs staging volume (RAM only, [EPHEM-01]) and returns an upload_token. The
// archive bytes are never written to disk or the DB.
func (d Deps) uploadScanSource(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBytes)
	fh, err := c.FormFile("file")
	if err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "multipart file field required", nil)
		return
	}
	ext := archiveExt(fh.Filename)
	if ext == "" {
		writeError(c, 400, "VALIDATION_ERROR", "file must be .tar, .tar.gz, .tgz or .zip", nil)
		return
	}
	token, err := randomToken()
	if err != nil {
		writeInternal(c, err)
		return
	}
	if err := os.MkdirAll(d.srcDir(), 0o1777); err != nil {
		writeInternal(c, err)
		return
	}
	dst := filepath.Join(d.srcDir(), token+ext)

	src, err := fh.Open()
	if err != nil {
		writeInternal(c, err)
		return
	}
	defer src.Close()
	out, err := os.Create(dst)
	if err != nil {
		writeInternal(c, err)
		return
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		_ = os.Remove(dst)
		writeError(c, 400, "VALIDATION_ERROR", "upload too large or interrupted", nil)
		return
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		writeInternal(c, err)
		return
	}
	// [M-2] bind the token to the uploading org (Redis TTL) so only that org can
	// reference the staged source in a subsequent scan.
	if d.SrcTokens != nil {
		if err := d.SrcTokens.PutSrcToken(c, token, tenancy.OrgID(c), time.Hour); err != nil {
			_ = os.Remove(dst)
			writeInternal(c, err)
			return
		}
	}
	c.JSON(http.StatusCreated, gin.H{"upload_token": token, "expires_in": 3600})
}

// archiveExt returns the canonical archive extension for a filename, or "".
func archiveExt(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".tar.gz"):
		return ".tar.gz"
	case strings.HasSuffix(l, ".tgz"):
		return ".tgz"
	case strings.HasSuffix(l, ".tar"):
		return ".tar"
	case strings.HasSuffix(l, ".zip"):
		return ".zip"
	default:
		return ""
	}
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (d Deps) listScans(c *gin.Context) {
	items, err := d.Store.ListScans(c, tenancy.Tx(c), projectID(c))
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) getScan(c *gin.Context) {
	sc, err := d.Store.GetScan(c, tenancy.Tx(c), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "scan not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, sc)
}

func (d Deps) listFindings(c *gin.Context) {
	if _, err := d.Store.GetScan(c, tenancy.Tx(c), c.Param("id")); errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "scan not found", nil)
		return
	}
	items, err := d.Store.ListFindings(c, tenancy.Tx(c), c.Param("id"))
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) updateFinding(c *gin.Context) {
	var req struct {
		Status       string  `json:"status"`
		IgnoreReason *string `json:"ignore_reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}
	fs := model.FindingStatus(req.Status)
	if fs != model.FindingOpen && fs != model.FindingIgnored && fs != model.FindingFixed {
		writeError(c, 400, "VALIDATION_ERROR", "status must be open, ignored or fixed", nil)
		return
	}
	err := d.Store.UpdateFindingStatus(c, tenancy.Tx(c), c.Param("findingId"), fs, req.IgnoreReason)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "finding not found", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": c.Param("findingId"), "status": fs})
}
