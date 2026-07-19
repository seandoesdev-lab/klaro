package domainverify

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const txtPrefix = "klaro-verify="
const challengePath = "/klaro-challenge.txt"

type Verifier struct {
	LookupTXT  func(string) ([]string, error)
	HTTPClient *http.Client
	scheme     string // "https" by default; overridable in tests
}

func New() *Verifier {
	return &Verifier{
		LookupTXT:  net.LookupTXT,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		scheme:     "https",
	}
}

func (v *Verifier) Verify(ctx context.Context, domain, method, token string) (bool, error) {
	switch method {
	case "dns_txt":
		return v.verifyDNS(domain, token)
	case "file":
		return v.verifyFile(ctx, domain, token)
	default:
		return false, fmt.Errorf("unknown verification method %q", method)
	}
}

func (v *Verifier) verifyDNS(domain, token string) (bool, error) {
	records, err := v.LookupTXT(domain)
	if err != nil {
		return false, err
	}
	want := txtPrefix + token
	for _, r := range records {
		if strings.TrimSpace(r) == want {
			return true, nil
		}
	}
	return false, nil
}

func (v *Verifier) verifyFile(ctx context.Context, domain, token string) (bool, error) {
	scheme := v.scheme
	if scheme == "" {
		scheme = "https"
	}
	url := scheme + "://" + domain + challengePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	client := v.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(body)) == token, nil
}
