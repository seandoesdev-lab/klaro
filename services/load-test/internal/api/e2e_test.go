//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
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

// e2e 흐름: signup → login → org 스코프 CRUD → 다른 org 리소스 404.
// 요구 환경변수: TEST_DATABASE_URL(klaro_app), TEST_SYSTEM_DATABASE_URL(klaro_system), TEST_REDIS_ADDR.
func newTestRouter(t *testing.T) (*Deps, http.Handler) {
	return newTestRouterEnv(t, "test")
}

func newTestRouterEnv(t *testing.T, appEnv string) (*Deps, http.Handler) {
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
	d := Deps{
		Store: st, Queue: rd, ScanQueue: rd, Signal: rd, Verifier: domainverify.New(),
		JWT: auth.NewJWTManager("e2e-secret"), Refresh: auth.NewRefreshStore(rd.Client()),
		OAuth: auth.NewOAuthManager("http://localhost:8080"), DevToken: "dev", AppEnv: appEnv,
	}
	return &d, NewRouter(d)
}

func doJSON(t *testing.T, h http.Handler, method, path, token, orgID string, body any) (int, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if orgID != "" {
		req.Header.Set("X-Org-Id", orgID)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestE2EAuthFlow(t *testing.T) {
	_, h := newTestRouter(t)
	uniq := strconv.FormatInt(time.Now().UnixNano(), 36)
	emailA := "a-" + uniq + "@klaro.test"
	emailB := "b-" + uniq + "@klaro.test"

	// signup A
	code, resp := doJSON(t, h, "POST", "/v1/auth/signup", "", "", map[string]string{"email": emailA, "password": "pw-aaaa", "name": "A"})
	if code != 201 {
		t.Fatalf("signup A = %d %v", code, resp)
	}
	orgA, _ := resp["org_id"].(string)
	if orgA == "" {
		t.Fatal("signup A returned no org_id")
	}
	// 중복 signup → 409
	if code, _ := doJSON(t, h, "POST", "/v1/auth/signup", "", "", map[string]string{"email": emailA, "password": "pw"}); code != 409 {
		t.Fatalf("duplicate signup = %d want 409", code)
	}

	// login A
	code, resp = doJSON(t, h, "POST", "/v1/auth/login", "", "", map[string]string{"email": emailA, "password": "pw-aaaa"})
	if code != 200 {
		t.Fatalf("login A = %d %v", code, resp)
	}
	tokA, _ := resp["access_token"].(string)
	if tokA == "" {
		t.Fatal("login A returned no access_token")
	}
	// 잘못된 비밀번호 → 401
	if code, _ := doJSON(t, h, "POST", "/v1/auth/login", "", "", map[string]string{"email": emailA, "password": "wrong"}); code != 401 {
		t.Fatalf("bad login = %d want 401", code)
	}

	// create project in org A
	code, resp = doJSON(t, h, "POST", "/v1/projects", tokA, orgA, map[string]string{"name": "proj-A"})
	if code != 201 {
		t.Fatalf("create project = %d %v", code, resp)
	}
	projA, _ := resp["id"].(string)

	// get own project → 200
	if code, _ := doJSON(t, h, "GET", "/v1/projects/"+projA, tokA, orgA, nil); code != 200 {
		t.Fatalf("get own project = %d want 200", code)
	}

	// signup + login B
	code, resp = doJSON(t, h, "POST", "/v1/auth/signup", "", "", map[string]string{"email": emailB, "password": "pw-bbbb"})
	if code != 201 {
		t.Fatalf("signup B = %d %v", code, resp)
	}
	orgB, _ := resp["org_id"].(string)
	code, resp = doJSON(t, h, "POST", "/v1/auth/login", "", "", map[string]string{"email": emailB, "password": "pw-bbbb"})
	tokB, _ := resp["access_token"].(string)

	// B가 A의 org 로 스코프 시도 → 멤버십 없음 → 404
	if code, _ := doJSON(t, h, "GET", "/v1/projects", tokB, orgA, nil); code != 404 {
		t.Fatalf("B scoping A's org = %d want 404", code)
	}
	// B가 자기 org 스코프에서 A의 project id 조회 → RLS 0건 → 404
	if code, _ := doJSON(t, h, "GET", "/v1/projects/"+projA, tokB, orgB, nil); code != 404 {
		t.Fatalf("B reading A's project = %d want 404", code)
	}

	// 미인증 → 401
	if code, _ := doJSON(t, h, "GET", "/v1/projects", "", orgA, nil); code != 401 {
		t.Fatalf("unauthenticated = %d want 401", code)
	}
}

// getRaw performs a GET and returns the raw recorder (headers/cookies 접근용).
func getRaw(h http.Handler, path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// F-2 + F-1(dev): dev 환경에서 OAuth mock 흐름은 state 쿠키 검증을 통과해야만 토큰 발급.
func TestOAuthDevMockFlow(t *testing.T) {
	_, h := newTestRouterEnv(t, "dev")

	// start → 302 + Set-Cookie(state) + Location(mock callback with state).
	w := getRaw(h, "/v1/auth/oauth/github", nil)
	if w.Code != http.StatusFound {
		t.Fatalf("oauth start = %d want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	cookies := w.Result().Cookies()
	if loc == "" || len(cookies) == 0 {
		t.Fatalf("expected Location + state cookie, got loc=%q cookies=%d", loc, len(cookies))
	}

	// callback WITHOUT the state cookie → F-2 위반 → 401.
	if wc := getRaw(h, loc, nil); wc.Code != 401 {
		t.Fatalf("callback without state cookie = %d want 401", wc.Code)
	}

	// callback WITH the state cookie → mock 프로파일 로그인 → 200 + tokens.
	wc := getRaw(h, loc, cookies)
	if wc.Code != 200 {
		t.Fatalf("dev mock callback = %d want 200 (body=%s)", wc.Code, wc.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(wc.Body.Bytes(), &out)
	if out["access_token"] == nil || out["refresh_token"] == nil {
		t.Fatalf("dev mock callback missing tokens: %v", out)
	}
}

// F-1: 비-dev 환경에서는 mock_email 이 무시되어 임의 계정 로그인이 불가해야 한다.
// state 검증은 통과시키되(정상 흐름 재현) mock 분기가 비활성 → code 없음 → 400.
func TestOAuthMockRejectedOutsideDev(t *testing.T) {
	_, h := newTestRouterEnv(t, "test") // 비-dev

	w := getRaw(h, "/v1/auth/oauth/github", nil)
	loc := w.Header().Get("Location")
	cookies := w.Result().Cookies()
	if loc == "" || len(cookies) == 0 {
		t.Fatalf("expected Location + state cookie")
	}
	// 유효한 state 쿠키를 실어 F-2는 통과 → 그러나 mock 분기 비활성(F-1) → code 부재 → 400.
	wc := getRaw(h, loc, cookies)
	if wc.Code != 400 {
		t.Fatalf("non-dev mock callback = %d want 400 (mock must be ignored; body=%s)", wc.Code, wc.Body.String())
	}
}
