package domainverify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyDNSTXTMatch(t *testing.T) {
	v := &Verifier{LookupTXT: func(string) ([]string, error) {
		return []string{"unrelated", "klaro-verify=tok123"}, nil
	}}
	ok, err := v.Verify(context.Background(), "example.com", "dns_txt", "tok123")
	if err != nil || !ok {
		t.Fatalf("expected match, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyDNSTXTNoMatch(t *testing.T) {
	v := &Verifier{LookupTXT: func(string) ([]string, error) {
		return []string{"klaro-verify=other"}, nil
	}}
	ok, _ := v.Verify(context.Background(), "example.com", "dns_txt", "tok123")
	if ok {
		t.Fatal("expected no match")
	}
}

func TestVerifyFileMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/klaro-challenge.txt" {
			w.Write([]byte("tok123\n"))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	v := &Verifier{HTTPClient: srv.Client(), scheme: "http"}
	ok, err := v.Verify(context.Background(), host, "file", "tok123")
	if err != nil || !ok {
		t.Fatalf("expected file match, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyUnknownMethod(t *testing.T) {
	v := New()
	if _, err := v.Verify(context.Background(), "example.com", "carrier_pigeon", "x"); err == nil {
		t.Fatal("expected error for unknown method")
	}
}
