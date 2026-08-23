package ingestkey

import (
	"errors"
	"testing"
	"time"
)

func TestNewAuthorizerFillsInDefaults(t *testing.T) {
	a := NewAuthorizer(nil, NewStore(nil), nil, AuthorizerOptions{})
	if a.opts.ActiveHostWindow != defaultActiveHostWindow {
		t.Errorf("ActiveHostWindow = %v", a.opts.ActiveHostWindow)
	}
	if a.opts.TouchWindow != defaultTouchWindow {
		t.Errorf("TouchWindow = %v", a.opts.TouchWindow)
	}
	if a.opts.CacheTTL != defaultCacheTTL {
		t.Errorf("CacheTTL = %v", a.opts.CacheTTL)
	}

	custom := NewAuthorizer(nil, NewStore(nil), nil, AuthorizerOptions{
		ActiveHostWindow: time.Hour, TouchWindow: 5 * time.Minute, CacheTTL: 2 * time.Second,
	})
	if custom.opts.CacheTTL != 2*time.Second {
		t.Errorf("explicit CacheTTL was overridden: %v", custom.opts.CacheTTL)
	}
}

// A header that is not shaped like a key must be turned away without a database
// round trip - otherwise every scanner probing the Collector costs a query. A
// nil *db.DB proves it: reaching the pool would panic.
func TestAuthorizeRejectsJunkWithoutQuerying(t *testing.T) {
	a := NewAuthorizer(nil, NewStore(nil), nil, AuthorizerOptions{})
	for _, secret := range []string{"", "hunter2", "Bearer obsk_x", "obsk_short"} {
		if _, err := a.Authorize(t.Context(), secret); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Authorize(%q) = %v, want ErrUnauthorized", secret, err)
		}
	}
}

// Every rejection reason collapses into one error so the ingest surface cannot
// be used to probe which keys exist.
func TestUnauthorizedIsIndistinguishable(t *testing.T) {
	if errors.Is(ErrUnauthorized, ErrNotFound) || errors.Is(ErrUnauthorized, ErrRevoked) {
		t.Error("ErrUnauthorized leaks the underlying reason")
	}
}
