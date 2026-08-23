// Package klarousage meters telemetry volume at the gateway and reports it to
// the klaro control plane (BILL-03, design HOW-6).
//
// It lives here rather than in the control plane because the gateway is the only
// component every signal passes through: metrics are also replicated to the
// control plane for the live view, but traces and logs go straight to storage.
// Metering there would silently bill for one signal out of three.
//
// Counting never blocks or drops data. Crossing a quota is an invoice line, not
// a rejection (design section 7-1), so this processor only observes.
package klarousage

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is the klarousage processor configuration.
type Config struct {
	// Endpoint is the control plane usage route, e.g.
	// https://obsplane:8443/internal/usage
	Endpoint string `mapstructure:"endpoint"`
	// FlushInterval is how often counters are reported. Longer means fewer
	// requests and a coarser view of a spike; the control plane accumulates
	// either way, so this trades request volume against reporting lag.
	FlushInterval time.Duration `mapstructure:"flush_interval"`
	// Timeout bounds one report.
	Timeout time.Duration `mapstructure:"timeout"`
	// MaxHosts caps how many host identities one report carries, so a runaway
	// instance id cannot turn metering into an unbounded payload.
	MaxHosts int `mapstructure:"max_hosts"`

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
	// Insecure disables TLS. mTLS is the contract for this hop (CLAUDE.md), so
	// it must be asked for explicitly - an absent bundle never silently
	// downgrades.
	Insecure bool `mapstructure:"insecure"`
}

// Defaults.
const (
	DefaultFlushInterval = 30 * time.Second
	DefaultTimeout       = 10 * time.Second
	DefaultMaxHosts      = 2000
)

// ErrInvalidConfig is returned by Validate.
var ErrInvalidConfig = errors.New("invalid klarousage configuration")

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("%w: endpoint is required", ErrInvalidConfig)
	}
	secure := strings.HasPrefix(c.Endpoint, "https://")
	if !secure && !strings.HasPrefix(c.Endpoint, "http://") {
		return fmt.Errorf("%w: endpoint must be an http(s) URL, got %q", ErrInvalidConfig, c.Endpoint)
	}
	if !secure && !c.TLS.Insecure {
		return fmt.Errorf("%w: a plaintext endpoint requires tls.insecure: true", ErrInvalidConfig)
	}
	if !c.TLS.Insecure && (c.TLS.CAFile == "" || c.TLS.CertFile == "" || c.TLS.KeyFile == "") {
		return fmt.Errorf("%w: tls.ca_file, tls.cert_file and tls.key_file are all required for mTLS", ErrInvalidConfig)
	}
	if c.FlushInterval < 0 || c.Timeout < 0 {
		return fmt.Errorf("%w: durations must not be negative", ErrInvalidConfig)
	}
	if c.InternalToken != "" && c.InternalTokenFile != "" {
		return fmt.Errorf("%w: set internal_token or internal_token_file, not both", ErrInvalidConfig)
	}
	// Without a client certificate the token is the only thing identifying this
	// gateway, and the control plane refuses an unauthenticated internal
	// request. Metering would then fail silently every flush - it is
	// deliberately fire-and-forget - so the misconfiguration has to be caught
	// here instead.
	if c.TLS.Insecure && c.InternalToken == "" && c.InternalTokenFile == "" {
		return fmt.Errorf("%w: tls.insecure requires internal_token or internal_token_file: "+
			"plaintext transport does not disable authentication on the control plane", ErrInvalidConfig)
	}
	return nil
}

// resolveInternalToken returns the token, reading the file form if that is what
// was configured. The trailing newline a mounted secret leaves behind is
// trimmed: a token differing from the server's by one invisible byte fails every
// flush with a 401.
func (c Config) resolveInternalToken() (string, error) {
	if c.InternalTokenFile == "" {
		return c.InternalToken, nil
	}
	b, err := os.ReadFile(c.InternalTokenFile)
	if err != nil {
		return "", fmt.Errorf("klarousage: read internal_token_file: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func (c Config) withDefaults() Config {
	if c.FlushInterval == 0 {
		c.FlushInterval = DefaultFlushInterval
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxHosts == 0 {
		c.MaxHosts = DefaultMaxHosts
	}
	return c
}
