//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/klaro/load-test/internal/auth"
	"github.com/klaro/load-test/internal/domainverify"
	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/store"
)

// newSecTestRouter builds a router with SrcTokens wired so M-2 (upload token org
// binding) is enforced. Requires the same env as the other e2e tests.
func newSecTestRouter(t *testing.T) http.Handler {
	appDSN := os.Getenv("TEST_DATABASE_URL")
	redisAddr := os.Getenv("TEST_REDIS_ADDR")
	if appDSN == "" || redisAddr == "" {
		t.Skip("TEST_DATABASE_URL / TEST_REDIS_ADDR not set")
	}
	st, err := store.New(context.Background(), appDSN, os.Getenv("TEST_SYSTEM_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rd := queue.NewRedis(redisAddr)
	// isolate SAST upload staging on a temp dir for this test process.
	_ = os.Setenv("SCAN_SRC_DIR", t.TempDir())
	d := Deps{
		Store: st, Queue: rd, ScanQueue: rd, SrcTokens: rd, Signal: rd,
		Verifier: domainverify.New(), JWT: auth.NewJWTManager("e2e-secret"),
		Refresh: auth.NewRefreshStore(rd.Client()),
		OAuth:   auth.NewOAuthManager("http://localhost:8080"), DevToken: "dev", AppEnv: "test",
	}
	return NewRouter(d)
}

func signupLoginProject(t *testing.T, h http.Handler, tag string) (org, tok, proj string) {
	uniq := tag + strconv.FormatInt(time.Now().UnixNano(), 36)
	email := uniq + "@klaro.test"
	code, resp := doJSON(t, h, "POST", "/v1/auth/signup", "", "", map[string]string{"email": email, "password": "pw-" + uniq, "name": tag})
	if code != 201 {
		t.Fatalf("signup %s = %d %v", tag, code, resp)
	}
	org, _ = resp["org_id"].(string)
	code, resp = doJSON(t, h, "POST", "/v1/auth/login", "", "", map[string]string{"email": email, "password": "pw-" + uniq})
	if code != 200 {
		t.Fatalf("login %s = %d", tag, code)
	}
	tok, _ = resp["access_token"].(string)
	code, resp = doJSON(t, h, "POST", "/v1/projects", tok, org, map[string]string{"name": "proj-" + tag})
	if code != 201 {
		t.Fatalf("project %s = %d %v", tag, code, resp)
	}
	proj, _ = resp["id"].(string)
	return org, tok, proj
}

// TestScanRepoURLInjectionRejected proves C-1/H-1 at the API boundary: git
// transport/argument injection and SSRF repo_urls are rejected with 400 before any
// clone/enqueue.
func TestScanRepoURLInjectionRejected(t *testing.T) {
	h := newSecTestRouter(t)
	org, tok, proj := signupLoginProject(t, h, "c1")

	bad := []string{
		"ext::sh -c 'touch /tmp/pwned'",
		"file:///etc/passwd",
		"http://169.254.169.254/latest/meta-data/",
		"https://169.254.169.254/x",
		"https://10.0.0.5/x.git",
		"https://127.0.0.1/x.git",
		"git://example.com/x.git",
		"--upload-pack=touch /tmp/pwned",
	}
	for _, repo := range bad {
		code, resp := doJSON(t, h, "POST", "/projects/"+proj+"/scans", tok, org,
			map[string]any{"type": "sast", "repo_url": repo})
		if code != 400 {
			t.Errorf("repo_url %q → %d want 400 (%v)", repo, code, resp)
		}
	}
}

// TestUploadTokenOrgScope proves M-2: an upload token can only be used by the org
// that uploaded it; another org referencing it gets 404.
func TestUploadTokenOrgScope(t *testing.T) {
	h := newSecTestRouter(t)
	orgA, tokA, projA := signupLoginProject(t, h, "m2a")
	orgB, tokB, projB := signupLoginProject(t, h, "m2b")

	// A uploads a source archive → token
	token := uploadSource(t, h, tokA, orgA, projA)

	// B references A's token → 404
	code, resp := doJSON(t, h, "POST", "/projects/"+projB+"/scans", tokB, orgB,
		map[string]any{"type": "sast", "upload_token": token})
	if code != 404 {
		t.Errorf("B using A's token → %d want 404 (%v)", code, resp)
	}

	// A references own token → 202 (accepted)
	code, resp = doJSON(t, h, "POST", "/projects/"+projA+"/scans", tokA, orgA,
		map[string]any{"type": "sast", "upload_token": token})
	if code != 202 {
		t.Errorf("A using own token → %d want 202 (%v)", code, resp)
	}
}

// uploadSource posts a small zip archive and returns the upload_token.
func uploadSource(t *testing.T, h http.Handler, tok, org, proj string) string {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "src.zip")
	// minimal valid empty-ish zip
	zipBytes := []byte("PK\x05\x06" + string(make([]byte, 18)))
	_, _ = fw.Write(zipBytes)
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/projects/"+proj+"/scans/source", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Org-Id", org)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("upload = %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	token, _ := out["upload_token"].(string)
	if token == "" {
		t.Fatal("upload returned no token")
	}
	return token
}
