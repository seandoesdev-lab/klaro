package scanner

import (
	"errors"
	"testing"
)

// TestValidateRepoURLRejectsInjectionAndSSRF proves C-1/H-1: git transport/argument
// injection and SSRF inputs are all rejected before any clone happens.
func TestValidateRepoURLRejects(t *testing.T) {
	bad := []string{
		"ext::sh -c 'touch /tmp/pwned'",         // git ext:: RCE transport
		"file:///etc/passwd",                    // local file disclosure
		"http://169.254.169.254/latest/meta-data/", // cloud metadata SSRF (also http)
		"https://169.254.169.254/latest/",       // metadata via https literal
		"https://10.0.0.5/x.git",                // RFC1918 private
		"https://192.168.1.1/x.git",             // RFC1918 private
		"https://172.16.0.1/x.git",              // RFC1918 private
		"https://127.0.0.1/x.git",               // loopback
		"https://[::1]/x.git",                   // ipv6 loopback
		"git://example.com/x.git",               // non-https scheme
		"ssh://git@example.com/x.git",           // non-https scheme
		"--upload-pack=touch /tmp/pwned",        // option injection
		"-oProxyCommand=evil",                   // leading dash / option
		"",                                      // empty
		"https:///nohost",                       // no host
	}
	for _, in := range bad {
		if err := ValidateRepoURL(in); err == nil {
			t.Errorf("ValidateRepoURL(%q) = nil, want rejection", in)
		} else if !errors.Is(err, ErrBlockedRepoURL) {
			t.Errorf("ValidateRepoURL(%q) err not ErrBlockedRepoURL: %v", in, err)
		}
	}
}

func TestValidateRepoURLAllowsPublicHTTPS(t *testing.T) {
	// A well-known public host over https should pass scheme/host checks. DNS
	// resolution is exercised; skip if the sandbox has no resolver.
	if err := ValidateRepoURL("https://github.com/OWASP/NodeGoat"); err != nil {
		if errors.Is(err, ErrBlockedRepoURL) && contains(err.Error(), "does not resolve") {
			t.Skip("no DNS in sandbox; scheme/host validation logic covered by reject cases")
		}
		t.Errorf("ValidateRepoURL(public https) = %v, want nil", err)
	}
}

func TestValidateRef(t *testing.T) {
	ok := []string{"", "main", "release/1.2", "v1.0.0", "a1b2c3d"}
	for _, r := range ok {
		if err := ValidateRef(r); err != nil {
			t.Errorf("ValidateRef(%q) = %v, want nil", r, err)
		}
	}
	bad := []string{"-x", "--upload-pack=evil", "a b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b"}
	for _, r := range bad {
		if err := ValidateRef(r); err == nil {
			t.Errorf("ValidateRef(%q) = nil, want rejection", r)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
