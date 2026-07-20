// Package auth holds credential<->token conversion primitives: password hashing,
// JWT access tokens, opaque rotating refresh tokens (Redis), API keys, and OAuth.
// It is DB-agnostic (store is injected by callers) and RBAC-agnostic.
package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost 12 per 설계 §1.1.
const bcryptCost = 12

// HashPassword returns a bcrypt hash of pw (AUTH-01: 평문 저장 금지).
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword reports whether pw matches the stored bcrypt hash.
func VerifyPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
