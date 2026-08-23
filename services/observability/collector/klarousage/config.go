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
	return nil
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
