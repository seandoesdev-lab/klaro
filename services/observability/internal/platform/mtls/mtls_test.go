package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pki writes a self-signed CA plus a leaf keypair into dir and returns Files.
// name becomes the leaf CN and DNS SAN.
func pki(t *testing.T, dir, name string) (Files, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "klaro-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	f := Files{
		CAFile:   filepath.Join(dir, "ca.pem"),
		CertFile: filepath.Join(dir, name+".pem"),
		KeyFile:  filepath.Join(dir, name+"-key.pem"),
	}
	writePEM(t, f.CAFile, "CERTIFICATE", caDER)
	leaf(t, f, name, caCert, caKey)
	return f, caCert, caKey
}

// leaf issues a cert usable as both client and server, signed by ca.
func leaf(t *testing.T, f Files, name string, ca *x509.Certificate, caKey *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, f.CertFile, "CERTIFICATE", der)
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, f.KeyFile, "EC PRIVATE KEY", kb)
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestServerRequiresVerifiedClientCert(t *testing.T) {
	dir := t.TempDir()
	f, _, _ := pki(t, dir, "localhost")

	srvCfg, err := ServerConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if srvCfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", srvCfg.ClientAuth)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.TLS = srvCfg
	srv.StartTLS()
	defer srv.Close()

	t.Run("client with cert succeeds", func(t *testing.T) {
		cliCfg, err := ClientConfig(f, "localhost")
		if err != nil {
			t.Fatal(err)
		}
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: cliCfg}}
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatalf("mTLS handshake failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("client without cert is rejected", func(t *testing.T) {
		cliCfg, err := ClientConfig(f, "localhost")
		if err != nil {
			t.Fatal(err)
		}
		cliCfg.Certificates = nil // anonymous client
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: cliCfg}}
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Fatal("anonymous client was accepted; internal mTLS is not enforced")
		}
	})

	t.Run("client from another CA is rejected", func(t *testing.T) {
		other, _, _ := pki(t, t.TempDir(), "localhost")
		cliCfg, err := ClientConfig(other, "localhost")
		if err != nil {
			t.Fatal(err)
		}
		cliCfg.RootCAs = mustPool(t, f.CAFile) // trust the real server...
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: cliCfg}}
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Fatal("client cert from an untrusted CA was accepted")
		}
	})
}

func mustPool(t *testing.T, caFile string) *x509.CertPool {
	t.Helper()
	b, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(b) {
		t.Fatal("bad CA pem")
	}
	return p
}

func TestMinVersionIsTLS13(t *testing.T) {
	dir := t.TempDir()
	f, _, _ := pki(t, dir, "localhost")
	s, err := ServerConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ClientConfig(f, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if s.MinVersion != tls.VersionTLS13 || c.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion server=%x client=%x, want TLS1.3", s.MinVersion, c.MinVersion)
	}
}

func TestLoadRejectsIncompleteOrBadInput(t *testing.T) {
	if _, err := ServerConfig(Files{}); err == nil {
		t.Error("empty Files should be rejected")
	}

	dir := t.TempDir()
	f, _, _ := pki(t, dir, "localhost")

	missing := f
	missing.CertFile = filepath.Join(dir, "nope.pem")
	if _, err := ServerConfig(missing); err == nil {
		t.Error("missing cert file should be rejected")
	}

	badCA := f
	badCA.CAFile = filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(badCA.CAFile, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ServerConfig(badCA)
	if !errors.Is(err, ErrNoCACerts) {
		t.Errorf("err = %v, want ErrNoCACerts", err)
	}
}
