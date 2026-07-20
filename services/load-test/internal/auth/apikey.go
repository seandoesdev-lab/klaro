package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// APIKeyPrefix marks a bearer token as an API key (vs a JWT) so authenticate
// can branch (AUTH-05).
const APIKeyPrefix = "klaro_"

// GenerateAPIKey mints a high-entropy key and returns (plaintext, sha256 hash).
// 원문은 발급 응답에서 1회만 노출되고 저장되지 않는다 (AUTH-05).
func GenerateAPIKey() (plaintext, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	plaintext = APIKeyPrefix + hex.EncodeToString(b)
	return plaintext, HashAPIKey(plaintext)
}

// HashAPIKey returns sha256(hex) of a key (키는 고엔트로피라 bcrypt 불필요).
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// IsAPIKey reports whether a bearer token looks like an API key.
func IsAPIKey(token string) bool { return strings.HasPrefix(token, APIKeyPrefix) }

// ConstantTimeEqual compares two hashes without timing leaks.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
