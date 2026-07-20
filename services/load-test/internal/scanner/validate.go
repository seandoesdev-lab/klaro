package scanner

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrBlockedRepoURL is returned when a SAST repo_url fails the security gate.
var ErrBlockedRepoURL = errors.New("repo_url rejected")

// ValidateRepoURL hardens the SAST repo clone input against git transport/argument
// injection (git ext:: RCE, --upload-pack injection) and SSRF (C-1/H-1):
//   - scheme MUST be https (http/git/ssh/ext/file/ftp all rejected)
//   - a host is required and must not resolve to a private/loopback/link-local or
//     cloud-metadata address (169.254.169.254 etc.)
//
// It performs DNS resolution for hostnames as defense-in-depth; the git clone call
// additionally disables the ext/file protocols so a TOCTOU rebind can't reach them.
func ValidateRepoURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%w: repo_url required", ErrBlockedRepoURL)
	}
	// Reject a leading '-' so the URL can never be read as a git option.
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("%w: must not start with '-'", ErrBlockedRepoURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: invalid url", ErrBlockedRepoURL)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w: scheme must be https (got %q)", ErrBlockedRepoURL, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: host required", ErrBlockedRepoURL)
	}
	if err := assertHostNotInternal(host); err != nil {
		return fmt.Errorf("%w: %v", ErrBlockedRepoURL, err)
	}
	return nil
}

// ValidateRef rejects git refs that could be interpreted as options or contain
// unsafe characters. Empty ref (default HEAD) is allowed.
func ValidateRef(ref string) error {
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("%w: ref must not start with '-'", ErrBlockedRepoURL)
	}
	if len(ref) > 255 {
		return fmt.Errorf("%w: ref too long", ErrBlockedRepoURL)
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: ref contains a control character", ErrBlockedRepoURL)
		}
	}
	if strings.ContainsAny(ref, " ~^:?*[\\") {
		return fmt.Errorf("%w: ref contains an invalid character", ErrBlockedRepoURL)
	}
	return nil
}

// assertHostNotInternal blocks IP literals and resolved hostnames that fall in
// private, loopback, link-local (incl. 169.254.169.254 metadata), or unspecified
// ranges (SSRF gate).
func assertHostNotInternal(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("host is a blocked address: %s", ip)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("host does not resolve: %s", host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("host resolves to a blocked address: %s", ip)
		}
	}
	return nil
}

// isBlockedIP reports whether ip is in a range disallowed for outbound clone.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || // 127.0.0.0/8, ::1
		ip.IsPrivate() || // RFC1918 (10/8,172.16/12,192.168/16) + ULA fc00::/7
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16 (incl. metadata), fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() // 0.0.0.0, ::
}
