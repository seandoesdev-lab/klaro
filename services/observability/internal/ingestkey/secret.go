// Package ingestkey issues and verifies the org-scoped observability keys that
// authenticate telemetry ingest (OBS-02, design HOW-4).
//
// One key belongs to an org, not to a project or a host: many services and many
// hosts send under the same key and separate themselves with resource labels.
// The plaintext secret exists only in the issue/rotate response - the database
// stores sha256(secret), so a database dump cannot be replayed as ingest
// credentials and a lost key can only be replaced, never recovered.
package ingestkey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// SecretPrefix marks a klaro observability key. It makes a leaked secret
// recognisable in a log or a commit, which is what secret scanners key on.
const SecretPrefix = "obsk_"

// secretEntropyBytes is 192 bits of randomness. Base64url-encoded that is 32
// characters, so a full secret is 37 characters.
const secretEntropyBytes = 24

// displayPrefixLen is how much of the secret is kept in the clear for display
// (observability_keys.key_prefix). Long enough for a human to tell two keys
// apart, far too short to brute-force the rest.
const displayPrefixLen = len(SecretPrefix) + 8

// ErrMalformedSecret is returned for input that is not shaped like a key.
var ErrMalformedSecret = errors.New("malformed observability key")

// NewSecret mints a fresh plaintext secret.
func NewSecret() (string, error) {
	buf := make([]byte, secretEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate key secret: %w", err)
	}
	// RawURLEncoding: no padding and no '+' or '/', so the secret survives being
	// pasted into a URL, an env var or a YAML scalar without quoting.
	return SecretPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash returns the stored form of a secret: lowercase hex sha256.
//
// No salt and no KDF, deliberately. A salted per-row hash cannot be looked up
// by hash, and the Collector authz path needs exactly that single-index lookup
// on every cache miss. The input is 192 bits of uniform randomness rather than
// a human password, so there is no dictionary to stretch against.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// EqualHash compares two stored hashes in constant time.
func EqualHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// DisplayPrefix returns the non-secret display fragment of a secret.
func DisplayPrefix(secret string) (string, error) {
	if err := ValidateSecret(secret); err != nil {
		return "", err
	}
	return secret[:displayPrefixLen], nil
}

// ValidateSecret checks the shape of a presented secret before it is hashed.
//
// This is a cheap filter, not authentication: rejecting obvious junk keeps
// malformed ingest headers from turning into database round trips.
func ValidateSecret(secret string) error {
	if !strings.HasPrefix(secret, SecretPrefix) {
		return fmt.Errorf("%w: missing %q prefix", ErrMalformedSecret, SecretPrefix)
	}
	body := secret[len(SecretPrefix):]
	if len(body) != base64.RawURLEncoding.EncodedLen(secretEntropyBytes) {
		return fmt.Errorf("%w: unexpected length %d", ErrMalformedSecret, len(secret))
	}
	if _, err := base64.RawURLEncoding.DecodeString(body); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedSecret, err)
	}
	return nil
}
