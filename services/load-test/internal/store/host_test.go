package store

import "testing"

func TestHostFromURL(t *testing.T) {
	h, err := HostFromURL("https://staging.example.com:8443/x")
	if err != nil || h != "staging.example.com" {
		t.Fatalf("got %q err %v", h, err)
	}
	if _, err := HostFromURL("::::"); err == nil {
		t.Fatal("expected error on malformed url")
	}
}
