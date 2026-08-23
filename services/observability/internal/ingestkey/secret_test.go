package ingestkey

import (
	"errors"
	"strings"
	"testing"
)

func TestNewSecretIsUniqueAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		s, err := NewSecret()
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateSecret(s); err != nil {
			t.Fatalf("NewSecret produced %q which ValidateSecret rejects: %v", s, err)
		}
		if seen[s] {
			t.Fatalf("duplicate secret %q", s)
		}
		seen[s] = true
	}
}

func TestValidateSecretRejectsJunk(t *testing.T) {
	good, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"",
		"hunter2",
		strings.TrimPrefix(good, SecretPrefix), // prefix stripped
		good + "x",                             // too long
		good[:len(good)-1],                     // too short
		SecretPrefix + strings.Repeat("*", 32), // right length, not base64url
	}
	for _, s := range bad {
		if err := ValidateSecret(s); !errors.Is(err, ErrMalformedSecret) {
			t.Errorf("ValidateSecret(%q) = %v, want ErrMalformedSecret", s, err)
		}
	}
}

// The display prefix goes into the API response and the database. It must never
// be enough to reconstruct the secret.
func TestDisplayPrefixLeaksOnlyTheLabel(t *testing.T) {
	s, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	p, err := DisplayPrefix(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, p) {
		t.Fatalf("prefix %q is not a prefix of %q", p, s)
	}
	if len(p) != displayPrefixLen {
		t.Errorf("prefix length = %d, want %d", len(p), displayPrefixLen)
	}
	if hidden := len(s) - len(p); hidden < 24 {
		t.Errorf("only %d characters stay secret", hidden)
	}
}

func TestDisplayPrefixRejectsMalformed(t *testing.T) {
	if _, err := DisplayPrefix("nope"); !errors.Is(err, ErrMalformedSecret) {
		t.Errorf("err = %v, want ErrMalformedSecret", err)
	}
}

func TestHashIsStableAndNotTheSecret(t *testing.T) {
	s, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	h := Hash(s)
	if h != Hash(s) {
		t.Error("Hash is not deterministic")
	}
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(h))
	}
	if strings.Contains(h, strings.TrimPrefix(s, SecretPrefix)) {
		t.Error("hash contains the secret")
	}

	other, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if Hash(other) == h {
		t.Error("distinct secrets hashed equal")
	}
}

func TestEqualHash(t *testing.T) {
	a := Hash("obsk_one")
	if !EqualHash(a, Hash("obsk_one")) {
		t.Error("equal hashes compared unequal")
	}
	if EqualHash(a, Hash("obsk_two")) {
		t.Error("different hashes compared equal")
	}
	if EqualHash(a, a[:10]) {
		t.Error("length mismatch compared equal")
	}
}
