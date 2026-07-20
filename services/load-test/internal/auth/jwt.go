package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is returned for any signature/expiry/parse failure (AUTH-02).
var ErrInvalidToken = errors.New("invalid token")

// AccessTTL is the stateless access-token lifetime (설계 D-2: 15분).
const AccessTTL = 15 * time.Minute

// JWTManager issues and verifies HS256 access tokens.
type JWTManager struct {
	secret []byte
	ttl    time.Duration
}

func NewJWTManager(secret string) *JWTManager {
	return &JWTManager{secret: []byte(secret), ttl: AccessTTL}
}

// Issue mints an access token whose sub is the user id. Returns (token, expiresInSeconds).
func (m *JWTManager) Issue(userID string) (string, int, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	if err != nil {
		return "", 0, err
	}
	return signed, int(m.ttl.Seconds()), nil
}

// Verify checks signature + expiry and returns the subject (user id).
func (m *JWTManager) Verify(token string) (string, error) {
	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return m.secret, nil
	})
	if err != nil || !parsed.Valid || claims.Subject == "" {
		return "", ErrInvalidToken
	}
	return claims.Subject, nil
}
