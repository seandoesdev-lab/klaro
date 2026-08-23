// Package jwtauth verifies the bearer JWT the klaro control planes accept.
//
// It is written against the standard library on purpose. A JWT verifier is
// ~200 lines of well-specified work, and the alternative - pulling a signing
// library in - adds a dependency to audit for the sake of code that has to be
// read line by line anyway. [COST-05] only forbids new *paid* dependencies, so
// this is a maintenance choice rather than a cost one.
//
// What it refuses, and why each refusal matters:
//
//   - a token whose header alg is not the single algorithm this verifier was
//     built for. Accepting whatever the token declares is the classic JWT
//     break: "alg":"none" authenticates anybody, and "alg":"HS256" against an
//     RS256 deployment lets a caller sign with the public key.
//   - a token with no exp. A bearer credential that never expires cannot be
//     revoked by waiting, so every leak of one is permanent.
//   - a token with no org_id or no role. Those two claims become the RLS scope
//     and the authorization decision, so a missing one must fail closed rather
//     than default to something.
//
// It deliberately does NOT decide whether the org in the token may touch the
// org in the URL. That is tenancy's job (tenancy.Resolve), so there is exactly
// one place in the service where cross-tenant access is judged.
package jwtauth

import (
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Alg is a supported signature algorithm.
type Alg string

// Supported algorithms. HS256 suits a deployment that shares a secret with its
// issuer; RS256 suits an external identity provider publishing a public key,
// which is where klaro is heading (design section 2.1: "JWT + OAuth2").
const (
	HS256 Alg = "HS256"
	RS256 Alg = "RS256"
)

// Verification failures. Callers should log the detail and tell the client only
// "unauthenticated": a precise reason ("expired" vs "bad signature") tells an
// attacker which half of the problem to keep working on.
var (
	// ErrMalformed: not three base64url segments, or not JSON inside.
	ErrMalformed = errors.New("jwtauth: malformed token")
	// ErrAlg: the header declares an algorithm this verifier does not use.
	ErrAlg = errors.New("jwtauth: unexpected signing algorithm")
	// ErrSignature: the signature does not verify under the configured key.
	ErrSignature = errors.New("jwtauth: signature is not valid")
	// ErrExpired: exp is in the past, or nbf/iat are in the future.
	ErrExpired = errors.New("jwtauth: token is not valid at this time")
	// ErrClaims: a required claim is missing or unusable.
	ErrClaims = errors.New("jwtauth: unusable claims")
	// ErrConfig: the verifier itself was built wrong.
	ErrConfig = errors.New("jwtauth: invalid verifier configuration")
)

// Claims is the subset of the payload klaro authorizes on.
//
// org_id and role are custom claims rather than OAuth scopes because the whole
// API is org-scoped and role-gated; encoding them as opaque scope strings would
// only mean parsing them back out again at every route.
type Claims struct {
	Subject   string `json:"sub"`
	Issuer    string `json:"iss"`
	OrgID     string `json:"org_id"`
	Role      string `json:"role"`
	ExpiresAt int64  `json:"exp"`
	NotBefore int64  `json:"nbf"`
	IssuedAt  int64  `json:"iat"`

	// Audience is "aud", which RFC 7519 allows to be either a string or an
	// array of strings. It is decoded separately for that reason.
	Audience []string `json:"-"`
}

// Options configures a Verifier.
type Options struct {
	// Alg is the one algorithm accepted. Required.
	Alg Alg
	// HSSecret is the shared secret for HS256.
	HSSecret []byte
	// RSAPublicKey verifies RS256 signatures.
	RSAPublicKey *rsa.PublicKey

	// Issuer, when set, must equal the iss claim.
	Issuer string
	// Audience, when set, must appear in the aud claim.
	Audience string
	// Leeway absorbs clock skew between issuer and verifier on exp/nbf/iat.
	Leeway time.Duration

	// Now is injectable so expiry is testable without sleeping.
	Now func() time.Time
}

// Verifier checks tokens against one key and one algorithm.
type Verifier struct {
	opts Options
}

// New validates the options and returns a Verifier.
func New(o Options) (*Verifier, error) {
	switch o.Alg {
	case HS256:
		// 32 bytes is HMAC-SHA256's block-equivalent strength. A short shared
		// secret is the realistic way an HS256 deployment gets broken, so it is
		// rejected here rather than warned about.
		if len(o.HSSecret) < 32 {
			return nil, fmt.Errorf("%w: HS256 needs a secret of at least 32 bytes, got %d", ErrConfig, len(o.HSSecret))
		}
	case RS256:
		if o.RSAPublicKey == nil {
			return nil, fmt.Errorf("%w: RS256 needs an RSA public key", ErrConfig)
		}
		if bits := o.RSAPublicKey.N.BitLen(); bits < 2048 {
			return nil, fmt.Errorf("%w: RSA key is %d bits, need at least 2048", ErrConfig, bits)
		}
	default:
		return nil, fmt.Errorf("%w: alg must be HS256 or RS256, got %q", ErrConfig, o.Alg)
	}
	if o.Leeway < 0 {
		return nil, fmt.Errorf("%w: leeway must not be negative", ErrConfig)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Verifier{opts: o}, nil
}

// header is the JOSE header, of which only alg is load-bearing here.
type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Verify checks the signature and then the time and identity claims, returning
// the payload only if all of it held.
func (v *Verifier) Verify(token string) (Claims, error) {
	signed, sigPart, ok := cutLast(token, '.')
	if !ok {
		return Claims{}, ErrMalformed
	}
	headerPart, payloadPart, ok := strings.Cut(signed, ".")
	if !ok || headerPart == "" || payloadPart == "" || strings.Contains(payloadPart, ".") {
		return Claims{}, ErrMalformed
	}

	headerJSON, err := decodeSegment(headerPart)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var h header
	if err := json.Unmarshal(headerJSON, &h); err != nil {
		return Claims{}, ErrMalformed
	}
	// The comparison is against the configured algorithm, never against a set
	// of "algorithms we know how to do". That single line is what closes both
	// alg confusion and "alg":"none".
	if h.Alg != string(v.opts.Alg) {
		return Claims{}, fmt.Errorf("%w: got %q, want %q", ErrAlg, h.Alg, v.opts.Alg)
	}

	sig, err := decodeSegment(sigPart)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	if err := v.verifySignature([]byte(signed), sig); err != nil {
		return Claims{}, err
	}

	// Claims are only read after the signature held, so nothing below can be
	// influenced by an unsigned payload.
	payload, err := decodeSegment(payloadPart)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, ErrMalformed
	}
	claims.Audience, err = decodeAudience(payload)
	if err != nil {
		return Claims{}, err
	}
	if err := v.checkClaims(claims); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

func (v *Verifier) verifySignature(signed, sig []byte) error {
	switch v.opts.Alg {
	case HS256:
		mac := hmac.New(sha256.New, v.opts.HSSecret)
		mac.Write(signed)
		if subtle.ConstantTimeCompare(mac.Sum(nil), sig) != 1 {
			return ErrSignature
		}
		return nil
	case RS256:
		sum := sha256.Sum256(signed)
		if err := rsa.VerifyPKCS1v15(v.opts.RSAPublicKey, crypto.SHA256, sum[:], sig); err != nil {
			return ErrSignature
		}
		return nil
	default:
		// Unreachable: New rejects every other alg.
		return ErrConfig
	}
}

func (v *Verifier) checkClaims(c Claims) error {
	now := v.opts.Now()
	if c.ExpiresAt == 0 {
		return fmt.Errorf("%w: exp is required", ErrClaims)
	}
	if now.After(time.Unix(c.ExpiresAt, 0).Add(v.opts.Leeway)) {
		return fmt.Errorf("%w: expired at %d", ErrExpired, c.ExpiresAt)
	}
	if c.NotBefore != 0 && now.Before(time.Unix(c.NotBefore, 0).Add(-v.opts.Leeway)) {
		return fmt.Errorf("%w: not valid before %d", ErrExpired, c.NotBefore)
	}
	if c.IssuedAt != 0 && now.Before(time.Unix(c.IssuedAt, 0).Add(-v.opts.Leeway)) {
		return fmt.Errorf("%w: issued in the future (%d)", ErrExpired, c.IssuedAt)
	}
	if v.opts.Issuer != "" && c.Issuer != v.opts.Issuer {
		return fmt.Errorf("%w: iss %q is not %q", ErrClaims, c.Issuer, v.opts.Issuer)
	}
	if v.opts.Audience != "" && !contains(c.Audience, v.opts.Audience) {
		return fmt.Errorf("%w: aud does not include %q", ErrClaims, v.opts.Audience)
	}
	if c.OrgID == "" {
		return fmt.Errorf("%w: org_id is required", ErrClaims)
	}
	if c.Role == "" {
		return fmt.Errorf("%w: role is required", ErrClaims)
	}
	return nil
}

// decodeAudience handles aud being either a string or an array of strings.
func decodeAudience(payload []byte) ([]string, error) {
	var raw struct {
		Aud json.RawMessage `json:"aud"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, ErrMalformed
	}
	if len(raw.Aud) == 0 || string(raw.Aud) == "null" {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw.Aud, &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw.Aud, &many); err == nil {
		return many, nil
	}
	return nil, fmt.Errorf("%w: aud must be a string or an array of strings", ErrClaims)
}

// decodeSegment decodes one JWS segment.
//
// Raw (unpadded) base64url is the only accepted encoding: RFC 7515 requires it,
// and accepting the padded form as well would mean two different strings verify
// as the same token.
func decodeSegment(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// cutLast splits on the final separator, which is how the signature is peeled
// off while leaving the signed input (header.payload) byte-identical.
func cutLast(s string, sep byte) (before, after string, found bool) {
	i := strings.LastIndexByte(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ParseRSAPublicKeyPEM reads a PEM public key, accepting a PKIX "PUBLIC KEY"
// block, a PKCS#1 "RSA PUBLIC KEY" block, or a certificate carrying one.
func ParseRSAPublicKeyPEM(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block found", ErrConfig)
	}
	switch block.Type {
	case "PUBLIC KEY":
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: parse public key: %v", ErrConfig, err)
		}
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%w: public key is %T, want RSA", ErrConfig, key)
		}
		return rsaKey, nil
	case "RSA PUBLIC KEY":
		key, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: parse pkcs1 public key: %v", ErrConfig, err)
		}
		return key, nil
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: parse certificate: %v", ErrConfig, err)
		}
		rsaKey, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%w: certificate key is %T, want RSA", ErrConfig, cert.PublicKey)
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("%w: unsupported PEM block %q", ErrConfig, block.Type)
	}
}

// ParseRSAPublicKeyFile is ParseRSAPublicKeyPEM over a file path.
func ParseRSAPublicKeyFile(path string) (*rsa.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read public key: %v", ErrConfig, err)
	}
	return ParseRSAPublicKeyPEM(b)
}

// ReadSecretFile reads an HS256 secret from a file, trimming the trailing
// newline an editor or `echo` leaves behind. A secret differing from the
// issuer's by one invisible byte fails every request with "bad signature",
// which is a miserable thing to debug.
func ReadSecretFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read secret: %v", ErrConfig, err)
	}
	return []byte(strings.TrimRight(string(b), "\r\n")), nil
}
