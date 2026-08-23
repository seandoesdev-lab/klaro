package ingestkey

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateName(t *testing.T) {
	got, err := ValidateName("  prod collector  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prod collector" {
		t.Errorf("ValidateName = %q, want trimmed", got)
	}

	for _, bad := range []string{"", "   ", "\t\n", strings.Repeat("x", maxNameLen+1)} {
		if _, err := ValidateName(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidName", bad, err)
		}
	}
	// Exactly at the limit is allowed.
	if _, err := ValidateName(strings.Repeat("x", maxNameLen)); err != nil {
		t.Errorf("name of exactly %d chars rejected: %v", maxNameLen, err)
	}
}

func TestResolutionLiveness(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	cases := []struct {
		name    string
		res     Resolution
		live    bool
		elapsed bool
	}{
		{
			name: "active with no deadline",
			res:  Resolution{Status: StatusActive},
			live: true,
		},
		{
			// The point of rotation grace: the old key keeps working while the
			// fleet redeploys.
			name: "rotated key inside its grace window",
			res:  Resolution{Status: StatusActive, GraceUntil: &future},
			live: true,
		},
		{
			name:    "rotated key past its grace window",
			res:     Resolution{Status: StatusActive, GraceUntil: &past},
			live:    false,
			elapsed: true,
		},
		{
			// Grace expiring exactly now must stop working, not linger.
			name:    "grace deadline is now",
			res:     Resolution{Status: StatusActive, GraceUntil: &now},
			live:    false,
			elapsed: true,
		},
		{
			name: "revoked key with a stale grace value",
			res:  Resolution{Status: StatusRevoked, GraceUntil: &future},
			live: false,
		},
		{
			name: "revoked key",
			res:  Resolution{Status: StatusRevoked, RevokedAt: &past},
			live: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.Live(now); got != tc.live {
				t.Errorf("Live = %v, want %v", got, tc.live)
			}
			if got := tc.res.GraceElapsed(now); got != tc.elapsed {
				t.Errorf("GraceElapsed = %v, want %v", got, tc.elapsed)
			}
		})
	}
}

// A malformed id must be refused before any SQL runs, which a nil *db.DB
// proves: reaching the database would panic.
func TestMutationsRejectMalformedKeyID(t *testing.T) {
	s := NewStore(nil)
	ctx := t.Context()

	if _, err := s.Revoke(ctx, "00000000-0000-0000-0000-0000000000aa", "not-a-uuid", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Revoke = %v, want ErrNotFound", err)
	}
	if _, _, _, err := s.Rotate(ctx, "00000000-0000-0000-0000-0000000000aa", "not-a-uuid", time.Hour, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Rotate = %v, want ErrNotFound", err)
	}
}

func TestIssueRejectsBadNameBeforeTouchingTheDatabase(t *testing.T) {
	if _, _, err := NewStore(nil).Issue(t.Context(), "00000000-0000-0000-0000-0000000000aa", " ", nil, nil); !errors.Is(err, ErrInvalidName) {
		t.Errorf("Issue = %v, want ErrInvalidName", err)
	}
}

func TestResolveRejectsMalformedSecretBeforeTouchingTheDatabase(t *testing.T) {
	if _, err := NewStore(nil).Resolve(t.Context(), "hunter2"); !errors.Is(err, ErrMalformedSecret) {
		t.Errorf("Resolve = %v, want ErrMalformedSecret", err)
	}
}
