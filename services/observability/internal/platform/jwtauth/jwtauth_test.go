package jwtauth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

// The tests mint their own tokens rather than pasting fixtures, so a change to
// the verifier is checked against freshly signed input instead of against a
// string that was correct once.

const testSecret = "0123456789abcdef0123456789abcdef" // exactly 32 bytes

var testNow = time.Unix(1_780_000_000, 0)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signHS builds a token, taking the header verbatim so a test can declare an
// alg the signature does not match.
func signHS(t *testing.T, secret string, hdr map[string]any, claims map[string]any) string {
	t.Helper()
	h, err := json.Marshal(hdr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed := b64(h) + "." + b64(p)
	return signed + "." + b64(hmacSHA256([]byte(secret), []byte(signed)))
}

// hmacSHA256 is spelled out from the RFC 2104 construction rather than taken
// from crypto/hmac, so the test does not verify the verifier using the very
// call it is checking.
func hmacSHA256(key, msg []byte) []byte {
	const blockSize = 64
	if len(key) > blockSize {
		sum := sha256.Sum256(key)
		key = sum[:]
	}
	pad := make([]byte, blockSize)
	copy(pad, key)
	inner := make([]byte, blockSize)
	outer := make([]byte, blockSize)
	for i := 0; i < blockSize; i++ {
		inner[i] = pad[i] ^ 0x36
		outer[i] = pad[i] ^ 0x5c
	}
	ih := sha256.New()
	ih.Write(inner)
	ih.Write(msg)
	oh := sha256.New()
	oh.Write(outer)
	oh.Write(ih.Sum(nil))
	return oh.Sum(nil)
}

func hsVerifier(t *testing.T, opts ...func(*Options)) *Verifier {
	t.Helper()
	o := Options{
		Alg:      HS256,
		HSSecret: []byte(testSecret),
		Now:      func() time.Time { return testNow },
	}
	for _, f := range opts {
		f(&o)
	}
	v, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

func goodClaims() map[string]any {
	return map[string]any{
		"sub":    "user-1",
		"org_id": "00000000-0000-0000-0000-00000000000a",
		"role":   "admin",
		"exp":    testNow.Add(time.Hour).Unix(),
		"iat":    testNow.Add(-time.Minute).Unix(),
	}
}

func hs256Header() map[string]any { return map[string]any{"alg": "HS256", "typ": "JWT"} }

func TestVerifyAcceptsAWellFormedToken(t *testing.T) {
	v := hsVerifier(t)
	got, err := v.Verify(signHS(t, testSecret, hs256Header(), goodClaims()))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.OrgID != "00000000-0000-0000-0000-00000000000a" || got.Role != "admin" || got.Subject != "user-1" {
		t.Errorf("claims = %+v", got)
	}
}

// The signature must be checked against the configured key, not against the one
// the token would like to be checked against.
func TestVerifyRejectsAForeignSignature(t *testing.T) {
	v := hsVerifier(t)
	_, err := v.Verify(signHS(t, "ffffffffffffffffffffffffffffffff", hs256Header(), goodClaims()))
	if !errors.Is(err, ErrSignature) {
		t.Fatalf("err = %v, want ErrSignature", err)
	}
}

// alg:none is the canonical JWT bypass: a token with an empty signature that a
// naive parser treats as verified.
func TestVerifyRejectsAlgNone(t *testing.T) {
	v := hsVerifier(t)
	h, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	p, _ := json.Marshal(goodClaims())
	if _, err := v.Verify(b64(h) + "." + b64(p) + "."); !errors.Is(err, ErrAlg) {
		t.Fatalf("err = %v, want ErrAlg", err)
	}
}

// Alg confusion: an RS256 deployment must not accept an HS256 token signed with
// the public key, so the check is "is it the configured alg", never "can we do
// this alg".
func TestVerifyRejectsAlgConfusionAgainstRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v, err := New(Options{Alg: RS256, RSAPublicKey: &key.PublicKey, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(signHS(t, testSecret, hs256Header(), goodClaims())); !errors.Is(err, ErrAlg) {
		t.Fatalf("err = %v, want ErrAlg", err)
	}
}

func TestVerifyAcceptsRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v, err := New(Options{Alg: RS256, RSAPublicKey: &key.PublicKey, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}

	h, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT"})
	p, _ := json.Marshal(goodClaims())
	signed := b64(h) + "." + b64(p)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(signed + "." + b64(sig)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// The same signature over a different payload must not verify.
	other, _ := json.Marshal(map[string]any{
		"org_id": "00000000-0000-0000-0000-00000000000b",
		"role":   "owner",
		"exp":    testNow.Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(b64(h) + "." + b64(other) + "." + b64(sig)); !errors.Is(err, ErrSignature) {
		t.Fatalf("a swapped payload verified: %v", err)
	}
}

func TestVerifyRejectsTimeAndClaimProblems(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{"expired", func(c map[string]any) { c["exp"] = testNow.Add(-time.Hour).Unix() }, ErrExpired},
		{"not yet valid", func(c map[string]any) { c["nbf"] = testNow.Add(time.Hour).Unix() }, ErrExpired},
		{"issued in the future", func(c map[string]any) { c["iat"] = testNow.Add(time.Hour).Unix() }, ErrExpired},
		{"no exp", func(c map[string]any) { delete(c, "exp") }, ErrClaims},
		{"no org", func(c map[string]any) { delete(c, "org_id") }, ErrClaims},
		{"no role", func(c map[string]any) { delete(c, "role") }, ErrClaims},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := goodClaims()
			tc.mutate(claims)
			_, err := hsVerifier(t).Verify(signHS(t, testSecret, hs256Header(), claims))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// Leeway exists so a few seconds of clock skew is not an outage; it must not
// turn into an unbounded grace period.
func TestVerifyLeewayCoversSkewButNotMore(t *testing.T) {
	claims := goodClaims()
	claims["exp"] = testNow.Add(-30 * time.Second).Unix()

	withLeeway := hsVerifier(t, func(o *Options) { o.Leeway = time.Minute })
	if _, err := withLeeway.Verify(signHS(t, testSecret, hs256Header(), claims)); err != nil {
		t.Fatalf("30s of skew inside a 1m leeway: %v", err)
	}
	claims["exp"] = testNow.Add(-2 * time.Minute).Unix()
	if _, err := withLeeway.Verify(signHS(t, testSecret, hs256Header(), claims)); !errors.Is(err, ErrExpired) {
		t.Fatal("2m past exp accepted with a 1m leeway")
	}
}

func TestVerifyChecksIssuerAndAudienceWhenConfigured(t *testing.T) {
	v := hsVerifier(t, func(o *Options) {
		o.Issuer = "https://auth.klaro.local"
		o.Audience = "klaro-obsplane"
	})

	claims := goodClaims()
	claims["iss"] = "https://auth.klaro.local"
	claims["aud"] = []string{"klaro-loadtest", "klaro-obsplane"}
	if _, err := v.Verify(signHS(t, testSecret, hs256Header(), claims)); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// aud is also allowed to be a bare string (RFC 7519).
	claims["aud"] = "klaro-obsplane"
	if _, err := v.Verify(signHS(t, testSecret, hs256Header(), claims)); err != nil {
		t.Fatalf("string aud: %v", err)
	}

	claims["aud"] = "someone-else"
	if _, err := v.Verify(signHS(t, testSecret, hs256Header(), claims)); !errors.Is(err, ErrClaims) {
		t.Error("a token for another audience was accepted")
	}
	claims["aud"] = "klaro-obsplane"
	claims["iss"] = "https://evil.example"
	if _, err := v.Verify(signHS(t, testSecret, hs256Header(), claims)); !errors.Is(err, ErrClaims) {
		t.Error("a token from another issuer was accepted")
	}
}

func TestVerifyRejectsMalformedInput(t *testing.T) {
	v := hsVerifier(t)
	for _, tok := range []string{
		"",
		"not-a-token",
		"only.two",
		"a.b.c.d",
		"!!!." + b64([]byte(`{}`)) + ".sig",
	} {
		if _, err := v.Verify(tok); err == nil {
			t.Errorf("%q verified", tok)
		}
	}
}

// Padded base64 must not be an alternative spelling of the same token: two
// strings that both verify make a token blocklist unreliable.
func TestVerifyRejectsPaddedBase64(t *testing.T) {
	tok := signHS(t, testSecret, hs256Header(), goodClaims())
	parts := strings.Split(tok, ".")
	if _, err := hsVerifier(t).Verify(parts[0] + "." + parts[1] + "." + parts[2] + "="); err == nil {
		t.Fatal("padded signature segment verified")
	}
}

func TestNewRejectsWeakConfiguration(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{"no alg", Options{HSSecret: []byte(testSecret)}},
		{"unsupported alg", Options{Alg: Alg("HS512"), HSSecret: []byte(testSecret)}},
		{"short secret", Options{Alg: HS256, HSSecret: []byte("too-short")}},
		{"rs256 without key", Options{Alg: RS256}},
		{"negative leeway", Options{Alg: HS256, HSSecret: []byte(testSecret), Leeway: -time.Second}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.opts); !errors.Is(err, ErrConfig) {
				t.Fatalf("err = %v, want ErrConfig", err)
			}
		})
	}
}

func TestNewRejectsAShortRSAKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Alg: RS256, RSAPublicKey: &key.PublicKey}); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestParseRSAPublicKeyPEMRoundTrips(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRSAPublicKeyPEM(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err != nil {
		t.Fatalf("ParseRSAPublicKeyPEM: %v", err)
	}
	if got.N.Cmp(key.PublicKey.N) != 0 {
		t.Error("parsed a different key")
	}
	if _, err := ParseRSAPublicKeyPEM([]byte("not pem")); !errors.Is(err, ErrConfig) {
		t.Errorf("err = %v, want ErrConfig", err)
	}
}
