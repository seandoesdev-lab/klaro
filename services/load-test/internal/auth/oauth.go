package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
)

// ErrOAuthProvider is returned for an unknown provider (AUTH-04: github|google).
var ErrOAuthProvider = errors.New("unknown oauth provider")

// Profile is the normalized identity fetched from an OAuth provider.
type Profile struct {
	Email string
	Sub   string
	Name  string
}

// OAuthManager configures GitHub/Google. If a provider's client credentials are
// unset it runs in dev-mock mode (D-3): no external call, deterministic profile.
type OAuthManager struct {
	configs map[string]*oauth2.Config
	client  *http.Client
}

// NewOAuthManager reads provider credentials from env. redirectBase is the API
// origin (e.g. http://localhost:8080). Missing creds → dev mock for that provider.
func NewOAuthManager(redirectBase string) *OAuthManager {
	cb := func(p string) string { return redirectBase + "/v1/auth/oauth/" + p + "/callback" }
	m := &OAuthManager{configs: map[string]*oauth2.Config{}, client: &http.Client{Timeout: 10 * time.Second}}

	if id := os.Getenv("GITHUB_CLIENT_ID"); id != "" {
		m.configs["github"] = &oauth2.Config{
			ClientID: id, ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
			Endpoint: github.Endpoint, RedirectURL: cb("github"), Scopes: []string{"read:user", "user:email"},
		}
	}
	if id := os.Getenv("GOOGLE_CLIENT_ID"); id != "" {
		m.configs["google"] = &oauth2.Config{
			ClientID: id, ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
			Endpoint: google.Endpoint, RedirectURL: cb("google"), Scopes: []string{"openid", "email", "profile"},
		}
	}
	return m
}

// Known reports whether provider is github or google.
func (m *OAuthManager) Known(provider string) bool {
	return provider == "github" || provider == "google"
}

// DevMode reports whether the provider lacks real credentials (mock path).
func (m *OAuthManager) DevMode(provider string) bool { return m.configs[provider] == nil }

// AuthRedirectURL returns where GET /oauth/:provider should 302 to.
// Real: the provider consent URL. Dev: our own callback with deterministic mock
// params so the flow is exercisable offline (D-3).
func (m *OAuthManager) AuthRedirectURL(provider, state string) (string, error) {
	if !m.Known(provider) {
		return "", ErrOAuthProvider
	}
	if cfg := m.configs[provider]; cfg != nil {
		return cfg.AuthCodeURL(state), nil
	}
	// dev mock: 결정적 프로파일로 콜백에 되돌린다.
	email := fmt.Sprintf("dev-%s@klaro.local", provider)
	sub := "mock-" + provider + "-001"
	q := url.Values{"mock_email": {email}, "mock_sub": {sub}, "state": {state}}
	return "/v1/auth/oauth/" + provider + "/callback?" + q.Encode(), nil
}

// MockProfile builds a Profile from callback mock params (dev path).
func (m *OAuthManager) MockProfile(provider, email, sub string) *Profile {
	if sub == "" {
		sub = "mock-" + provider
	}
	return &Profile{Email: email, Sub: sub, Name: email}
}

// Exchange completes the real authorization-code flow and fetches the profile.
// Only reached when the provider has real credentials.
func (m *OAuthManager) Exchange(ctx context.Context, provider, code string) (*Profile, error) {
	cfg := m.configs[provider]
	if cfg == nil {
		return nil, ErrOAuthProvider
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, err
	}
	var userInfoURL string
	switch provider {
	case "github":
		userInfoURL = "https://api.github.com/user"
	case "google":
		userInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	default:
		return nil, ErrOAuthProvider
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var raw struct {
		ID    any    `json:"id"`
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	sub := raw.Sub
	if sub == "" {
		sub = fmt.Sprintf("%v", raw.ID)
	}
	name := raw.Name
	if name == "" {
		name = raw.Login
	}
	return &Profile{Email: raw.Email, Sub: sub, Name: name}, nil
}
