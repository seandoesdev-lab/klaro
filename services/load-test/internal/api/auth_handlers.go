package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/auth"
	"github.com/klaro/load-test/internal/store"
)

// issueTokens mints access + refresh tokens for a user (AUTH-02).
func (d Deps) issueTokens(c *gin.Context, userID string) {
	access, expiresIn, err := d.JWT.Issue(userID)
	if err != nil {
		writeError(c, 500, "INTERNAL", "token issue failed", nil)
		return
	}
	refresh, err := d.Refresh.Issue(c, userID)
	if err != nil {
		writeError(c, 500, "INTERNAL", "refresh issue failed", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token":  access,
		"refresh_token": refresh,
		"expires_in":    expiresIn,
	})
}

// signup — AUTH-01. 개인 org + owner + default project 원자 생성(D-6).
func (d Deps) signup(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Email == "" || req.Password == "" {
		writeError(c, 400, "VALIDATION_ERROR", "email and password required", nil)
		return
	}
	// 중복 email → 409 (citext UNIQUE + 선검사).
	if _, err := d.Store.GetUserByEmail(c, req.Email); err == nil {
		writeError(c, 409, "CONFLICT", "email already registered", nil)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeInternal(c, err)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(c, 500, "INTERNAL", "hash failed", nil)
		return
	}
	var namePtr *string
	if req.Name != "" {
		namePtr = &req.Name
	}
	orgName := req.Email
	if req.Name != "" {
		orgName = req.Name
	}
	res, err := d.Store.SignupWithOrg(c, req.Email, &hash, namePtr, orgName)
	if errors.Is(err, store.ErrConflict) { // 선검사 이후 경합한 동시 가입 (F-3)
		writeError(c, 409, "CONFLICT", "email already registered", nil)
		return
	}
	if err != nil {
		writeInternal(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user_id": res.UserID, "org_id": res.OrgID})
}

// login — AUTH-02. 실패는 계정 존재 여부를 노출하지 않는다(401).
func (d Deps) login(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}
	u, err := d.Store.GetUserByEmail(c, req.Email)
	if err != nil || u.PasswordHash == nil || !auth.VerifyPassword(*u.PasswordHash, req.Password) {
		writeError(c, 401, "UNAUTHENTICATED", "invalid credentials", nil)
		return
	}
	d.issueTokens(c, u.ID)
}

// refresh — AUTH-03. 회전 + 재사용/무효 → 401.
func (d Deps) refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.RefreshToken == "" {
		writeError(c, 400, "VALIDATION_ERROR", "refresh_token required", nil)
		return
	}
	newRefresh, userID, err := d.Refresh.Rotate(c, req.RefreshToken)
	if err != nil {
		writeError(c, 401, "UNAUTHENTICATED", "invalid refresh token", nil)
		return
	}
	access, expiresIn, err := d.JWT.Issue(userID)
	if err != nil {
		writeError(c, 500, "INTERNAL", "token issue failed", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token":  access,
		"refresh_token": newRefresh,
		"expires_in":    expiresIn,
	})
}

// logout — AUTH-03. family 폐기(멱등).
func (d Deps) logout(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.RefreshToken != "" {
		_ = d.Refresh.Revoke(c, req.RefreshToken)
	}
	c.Status(http.StatusNoContent)
}

// oauthStateCookie carries the per-request CSRF state across the OAuth round-trip (F-2).
const oauthStateCookie = "klaro_oauth_state"

// randState mints a random, unguessable OAuth state token.
func randState() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// oauthStart — AUTH-04. provider 동의 화면(또는 dev mock 콜백)으로 302.
// 요청별 랜덤 state 를 httpOnly 쿠키에 저장해 콜백에서 CSRF 검증한다(F-2).
func (d Deps) oauthStart(c *gin.Context) {
	provider := c.Param("provider")
	if !d.OAuth.Known(provider) {
		writeError(c, 400, "VALIDATION_ERROR", "provider must be github or google", nil)
		return
	}
	state := randState()
	url, err := d.OAuth.AuthRedirectURL(provider, state)
	if err != nil {
		writeError(c, 400, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	// maxAge 600s, path "/", httpOnly. secure 는 프로덕션(HTTPS)에서만.
	c.SetCookie(oauthStateCookie, state, 600, "/", "", d.AppEnv != "dev", true)
	c.Redirect(http.StatusFound, url)
}

// oauthCallback — AUTH-04 + D-4(자동 링크). 프로파일 획득 → 유저 매칭/생성 → 토큰 발급.
func (d Deps) oauthCallback(c *gin.Context) {
	provider := c.Param("provider")
	if !d.OAuth.Known(provider) {
		writeError(c, 400, "VALIDATION_ERROR", "unknown provider", nil)
		return
	}

	// F-2: state CSRF 검증. 쿠키의 state 와 콜백 query state 가 일치해야 한다.
	cookieState, _ := c.Cookie(oauthStateCookie)
	if cookieState == "" || c.Query("state") != cookieState {
		writeError(c, 401, "UNAUTHENTICATED", "invalid oauth state", nil)
		return
	}
	c.SetCookie(oauthStateCookie, "", -1, "/", "", d.AppEnv != "dev", true) // 1회용: 즉시 폐기

	var profile *auth.Profile
	// F-1: mock 분기는 dev 환경 + 해당 provider 크리덴셜 미설정(DevMode)일 때만 허용.
	// 프로덕션/크리덴셜 구성 시에는 반드시 code 교환 경로만 탄다(계정 탈취 방지).
	if d.AppEnv == "dev" && d.OAuth.DevMode(provider) && c.Query("mock_email") != "" {
		profile = d.OAuth.MockProfile(provider, c.Query("mock_email"), c.Query("mock_sub"))
	} else if code := c.Query("code"); code != "" {
		p, err := d.OAuth.Exchange(c, provider, code)
		if err != nil {
			writeError(c, 401, "UNAUTHENTICATED", "oauth exchange failed", nil)
			return
		}
		profile = p
	} else {
		writeError(c, 400, "VALIDATION_ERROR", "authorization code required", nil)
		return
	}
	if profile.Email == "" {
		writeError(c, 422, "VALIDATION_ERROR", "provider returned no email", nil)
		return
	}

	u, err := d.Store.GetUserByEmail(c, profile.Email)
	switch {
	case err == nil:
		// 기존 email → OAuth 자동 링크(D-4). password_hash 보존.
		if u.OAuthProvider == nil {
			if lerr := d.Store.LinkOAuth(c, u.ID, provider, profile.Sub); lerr != nil {
				writeInternal(c, lerr)
				return
			}
		}
		d.issueTokens(c, u.ID)
	case errors.Is(err, store.ErrNotFound):
		res, cerr := d.Store.CreateOAuthUserWithOrg(c, profile.Email, provider, profile.Sub, &profile.Name)
		if cerr != nil {
			writeInternal(c, cerr)
			return
		}
		d.issueTokens(c, res.UserID)
	default:
		writeInternal(c, err)
	}
}
