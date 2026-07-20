package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashVerify(t *testing.T) {
	hash, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "s3cret-pw" || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("hash must be a bcrypt digest, got %q", hash)
	}
	if !VerifyPassword(hash, "s3cret-pw") {
		t.Error("correct password should verify")
	}
	if VerifyPassword(hash, "wrong") {
		t.Error("wrong password must not verify")
	}
}

func TestJWTIssueVerify(t *testing.T) {
	m := NewJWTManager("test-secret")
	tok, exp, err := m.Issue("user-123")
	if err != nil {
		t.Fatal(err)
	}
	if exp <= 0 {
		t.Fatalf("expires_in must be positive, got %d", exp)
	}
	sub, err := m.Verify(tok)
	if err != nil || sub != "user-123" {
		t.Fatalf("verify: sub=%q err=%v", sub, err)
	}
	// 다른 시크릿으로 서명한 토큰은 거부.
	other := NewJWTManager("different-secret")
	if _, err := other.Verify(tok); err == nil {
		t.Error("token signed by another secret must be rejected")
	}
	if _, err := m.Verify("garbage.token.value"); err == nil {
		t.Error("garbage token must be rejected")
	}
}

func TestAPIKeyGenerateHash(t *testing.T) {
	plaintext, hash := GenerateAPIKey()
	if !strings.HasPrefix(plaintext, APIKeyPrefix) {
		t.Fatalf("plaintext must carry the %q prefix", APIKeyPrefix)
	}
	if !IsAPIKey(plaintext) {
		t.Error("IsAPIKey should detect the key")
	}
	if IsAPIKey("Bearer-jwt-token") {
		t.Error("non-key must not be detected as api key")
	}
	if hash == plaintext {
		t.Error("stored hash must not equal plaintext (AUTH-05)")
	}
	if HashAPIKey(plaintext) != hash {
		t.Error("HashAPIKey must be deterministic")
	}
	if !ConstantTimeEqual(hash, HashAPIKey(plaintext)) {
		t.Error("equal hashes must compare equal")
	}
}

func TestOAuthMockProfile(t *testing.T) {
	m := NewOAuthManager("http://localhost:8080")
	if !m.Known("github") || !m.Known("google") {
		t.Fatal("github/google must be known providers")
	}
	if m.Known("twitter") {
		t.Fatal("unknown provider must be rejected")
	}
	// 크리덴셜 미설정 → dev mock
	if !m.DevMode("github") {
		t.Fatal("without creds github must be in dev mock mode")
	}
	url, err := m.AuthRedirectURL("github", "state")
	if err != nil || !strings.Contains(url, "mock_email") {
		t.Fatalf("dev redirect must carry mock params: %q err=%v", url, err)
	}
	p := m.MockProfile("github", "a@b.com", "sub-1")
	if p.Email != "a@b.com" || p.Sub != "sub-1" {
		t.Fatalf("unexpected mock profile %+v", p)
	}
}
