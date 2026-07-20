package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// RefreshTTL is the refresh-token lifetime (설계 D-2: 14일).
const RefreshTTL = 14 * 24 * time.Hour

// ErrRefreshInvalid covers missing/expired/revoked/reused refresh tokens (AUTH-03).
var ErrRefreshInvalid = errors.New("invalid refresh token")

// RefreshStore implements opaque, rotating refresh tokens with reuse detection
// backed by Redis (설계 D-2). 원문 토큰은 저장하지 않고 sha256 만 키로 쓴다.
//
//	rt:{sha}   -> {user_id, family_id, used}  TTL 14d   (토큰 레코드)
//	fam:{fid}  -> "1"                          TTL 14d   (family 활성 마커; 폐기 시 삭제)
type RefreshStore struct{ c *redis.Client }

func NewRefreshStore(c *redis.Client) *RefreshStore { return &RefreshStore{c: c} }

type refreshRec struct {
	UserID   string `json:"u"`
	FamilyID string `json:"f"`
	Used     bool   `json:"used"`
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func shaKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "rt:" + hex.EncodeToString(sum[:])
}

func famKey(fid string) string { return "fam:" + fid }

// Issue mints a brand-new refresh token in a fresh family (login/signup/oauth).
func (s *RefreshStore) Issue(ctx context.Context, userID string) (string, error) {
	fid := randHex(16)
	if err := s.c.Set(ctx, famKey(fid), "1", RefreshTTL).Err(); err != nil {
		return "", err
	}
	return s.mint(ctx, userID, fid)
}

func (s *RefreshStore) mint(ctx context.Context, userID, familyID string) (string, error) {
	token := randHex(32)
	rec, _ := json.Marshal(refreshRec{UserID: userID, FamilyID: familyID})
	if err := s.c.Set(ctx, shaKey(token), rec, RefreshTTL).Err(); err != nil {
		return "", err
	}
	return token, nil
}

// Rotate validates and consumes an old token, issuing a new one in the same
// family. Reuse of an already-consumed token revokes the whole family (AUTH-03).
func (s *RefreshStore) Rotate(ctx context.Context, oldToken string) (newToken, userID string, err error) {
	key := shaKey(oldToken)
	raw, e := s.c.Get(ctx, key).Result()
	if errors.Is(e, redis.Nil) {
		return "", "", ErrRefreshInvalid
	}
	if e != nil {
		return "", "", e
	}
	var rec refreshRec
	if e := json.Unmarshal([]byte(raw), &rec); e != nil {
		return "", "", ErrRefreshInvalid
	}
	// 재사용 감지: 이미 소비된 토큰 → family 전체 폐기.
	if rec.Used {
		_ = s.c.Del(ctx, famKey(rec.FamilyID)).Err()
		return "", "", ErrRefreshInvalid
	}
	// family 활성 확인 (로그아웃/재사용으로 폐기되었으면 거부).
	if n, _ := s.c.Exists(ctx, famKey(rec.FamilyID)).Result(); n == 0 {
		return "", "", ErrRefreshInvalid
	}
	// 구 토큰을 used 로 표시 (재사용 감지용, TTL 유지).
	ttl, _ := s.c.TTL(ctx, key).Result()
	if ttl <= 0 {
		ttl = RefreshTTL
	}
	used, _ := json.Marshal(refreshRec{UserID: rec.UserID, FamilyID: rec.FamilyID, Used: true})
	_ = s.c.Set(ctx, key, used, ttl).Err()

	nt, e := s.mint(ctx, rec.UserID, rec.FamilyID)
	if e != nil {
		return "", "", e
	}
	return nt, rec.UserID, nil
}

// Revoke invalidates the entire family the token belongs to (logout, D-2).
func (s *RefreshStore) Revoke(ctx context.Context, token string) error {
	raw, e := s.c.Get(ctx, shaKey(token)).Result()
	if errors.Is(e, redis.Nil) {
		return nil // 이미 없음 → 로그아웃은 멱등
	}
	if e != nil {
		return e
	}
	var rec refreshRec
	if e := json.Unmarshal([]byte(raw), &rec); e != nil {
		return nil
	}
	return s.c.Del(ctx, famKey(rec.FamilyID)).Err()
}
