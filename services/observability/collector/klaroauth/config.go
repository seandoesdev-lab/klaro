// Package klaroauth authenticates OTLP ingest against the klaro control plane.
//
// The SDK sends an org observability key in a header (design HOW-4). This
// extension trades that secret for the org it belongs to and the tenant
// identity each storage backend needs, by asking the control plane over mTLS
// (design section 4.4), and caches the answer so the question is asked once per
// key per TTL rather than once per batch.
//
// The resolved identity is put on the request context - never taken from
// anything the SDK sent - so downstream components route telemetry by an
// identity the control plane vouched for.
package klaroauth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is the klaroauth extension configuration.
type Config struct {
	// Endpoint is the control plane authz route, for example
	// https://obsplane:8443/internal/authz/ingest-key
	Endpoint string `mapstructure:"endpoint"`

	// Header carries the org key. Defaults to klaro-obs-key.
	Header string `mapstructure:"header"`

	// CacheTTL bounds how long a successful grant is reused. It is also the
	// worst case delay before a revoked key stops being accepted, so it trades
	// ingest latency against revocation latency. The control plane suggests a
	// value in each grant; this is the ceiling applied to it.
	CacheTTL time.Duration `mapstructure:"cache_ttl"`

	// NegativeCacheTTL bounds how long a rejection is remembered. It exists so a
	// misconfigured fleet retrying with a bad key cannot turn into a request
	// flood against the control plane. Keep it short: it is also how long a
	// newly issued key can keep being refused.
	NegativeCacheTTL time.Duration `mapstructure:"negative_cache_ttl"`

	// Timeout bounds one authz call.
	Timeout time.Duration `mapstructure:"timeout"`

	// TLS is the client half of the internal mTLS hop.
	TLS TLSConfig `mapstructure:"tls"`

	// InternalToken is presented to the control plane as
	// `Authorization: Bearer`. The internal plane authenticates every request
	// independently of its transport, so this is what identifies the gateway
	// when tls.insecure short-circuits the client certificate.
	//
	// InternalTokenFile is the same value read from a file, which is how a
	// mounted secret arrives. Set one or the other, not both.
	InternalToken     string `mapstructure:"internal_token"`
	InternalTokenFile string `mapstructure:"internal_token_file"`
}

// TLSConfig locates the client certificate material for the control plane hop.
type TLSConfig struct {
	CAFile     string `mapstructure:"ca_file"`
	CertFile   string `mapstructure:"cert_file"`
	KeyFile    string `mapstructure:"key_file"`
	ServerName string `mapstructure:"server_name"`

	// Insecure disables TLS entirely. mTLS is the contract for this hop
	// (CLAUDE.md), so this exists only for a local compose run before
	// certificates are minted and must be set deliberately - an absent bundle
	// never silently downgrades.
	Insecure bool `mapstructure:"insecure"`
}

// Defaults applied to anything left unset.
const (
	DefaultHeader           = "klaro-obs-key"
	DefaultCacheTTL         = 30 * time.Second
	DefaultNegativeCacheTTL = 5 * time.Second
	DefaultTimeout          = 3 * time.Second
)

// ErrInvalidConfig is returned by Validate.
var ErrInvalidConfig = errors.New("invalid klaroauth configuration")

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("%w: endpoint is required", ErrInvalidConfig)
	}
	secure := strings.HasPrefix(c.Endpoint, "https://")
	if !secure && !strings.HasPrefix(c.Endpoint, "http://") {
		return fmt.Errorf("%w: endpoint must be an http(s) URL, got %q", ErrInvalidConfig, c.Endpoint)
	}
	// A plaintext endpoint would carry ingest secrets in the clear. Refusing it
	// unless tls.insecure was asked for keeps a typo from becoming a leak.
	if !secure && !c.TLS.Insecure {
		return fmt.Errorf("%w: a plaintext endpoint requires tls.insecure: true", ErrInvalidConfig)
	}
	if !c.TLS.Insecure {
		if c.TLS.CAFile == "" || c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			return fmt.Errorf("%w: tls.ca_file, tls.cert_file and tls.key_file are all required for mTLS", ErrInvalidConfig)
		}
	}
	if c.InternalToken != "" && c.InternalTokenFile != "" {
		return fmt.Errorf("%w: set internal_token or internal_token_file, not both", ErrInvalidConfig)
	}
	// Without a client certificate the token is the only thing identifying this
	// gateway to the control plane, which refuses an unauthenticated internal
	// request. Failing here says so at startup instead of turning every authz
	// call into a 401 and every batch into a drop.
	if c.TLS.Insecure && c.InternalToken == "" && c.InternalTokenFile == "" {
		return fmt.Errorf("%w: tls.insecure requires internal_token or internal_token_file: "+
			"plaintext transport does not disable authentication on the control plane", ErrInvalidConfig)
	}
	for _, d := range []struct {
		name  string
		value time.Duration
	}{
		{"cache_ttl", c.CacheTTL},
		{"negative_cache_ttl", c.NegativeCacheTTL},
		{"timeout", c.Timeout},
	} {
		if d.value < 0 {
			return fmt.Errorf("%w: %s must not be negative", ErrInvalidConfig, d.name)
		}
	}
	return nil
}

// resolveInternalToken returns the token, reading the file form if that is what
// was configured. The trailing newline a mounted secret or `echo` leaves behind
// is trimmed: a token differing from the server's by one invisible byte fails
// every request with a 401, which is a miserable thing to debug.
func (c Config) resolveInternalToken() (string, error) {
	if c.InternalTokenFile == "" {
		return c.InternalToken, nil
	}
	b, err := os.ReadFile(c.InternalTokenFile)
	if err != nil {
		return "", fmt.Errorf("klaroauth: read internal_token_file: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// withDefaults returns a copy with the unset fields filled in.
func (c Config) withDefaults() Config {
	if c.Header == "" {
		c.Header = DefaultHeader
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = DefaultCacheTTL
	}
	if c.NegativeCacheTTL == 0 {
		c.NegativeCacheTTL = DefaultNegativeCacheTTL
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	return c
}
