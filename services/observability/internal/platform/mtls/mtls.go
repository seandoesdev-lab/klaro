// Package mtls builds the TLS configs for the internal plane.
//
// The Collector -> CP and vmalert -> CP hops carry org resolution and alert
// state, so both ends authenticate with certificates (CLAUDE.md: "Control
// Plane <-> Worker ... mTLS 필수"). The server therefore requires AND verifies a
// client certificate; there is no anonymous-client mode to fall back to.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// ErrNoCACerts is returned when the CA bundle contains no usable certificate.
var ErrNoCACerts = errors.New("no CA certificates found in bundle")

// Files locates a PEM keypair plus the CA bundle used to verify the peer.
type Files struct {
	CAFile   string
	CertFile string
	KeyFile  string
}

// ServerConfig returns a TLS config that requires and verifies client certs.
func ServerConfig(f Files) (*tls.Config, error) {
	cert, pool, err := load(f)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		// RequireAndVerifyClientCert is the whole point: VerifyClientCertIfGiven
		// would let an unauthenticated Collector through.
		ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS13,
	}, nil
}

// ClientConfig returns a TLS config that presents a client cert and verifies
// the server against the same CA. serverName must match the server cert SAN.
func ClientConfig(f Files, serverName string) (*tls.Config, error) {
	cert, pool, err := load(f)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func load(f Files) (tls.Certificate, *x509.CertPool, error) {
	if f.CAFile == "" || f.CertFile == "" || f.KeyFile == "" {
		return tls.Certificate{}, nil, errors.New("mtls: CA, cert and key files are all required")
	}
	cert, err := tls.LoadX509KeyPair(f.CertFile, f.KeyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("mtls: load keypair: %w", err)
	}
	pem, err := os.ReadFile(f.CAFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("mtls: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return tls.Certificate{}, nil, fmt.Errorf("mtls: %w: %s", ErrNoCACerts, f.CAFile)
	}
	return cert, pool, nil
}
