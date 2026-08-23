package config

import (
	"errors"
	"testing"
	"time"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(envFrom(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":8090" {
		t.Errorf("Addr = %q, want :8090", c.Addr)
	}
	if c.DBMaxConns != 10 {
		t.Errorf("DBMaxConns = %d, want 10", c.DBMaxConns)
	}
	if c.ActiveHostWindow != 15*time.Minute {
		t.Errorf("ActiveHostWindow = %v, want 15m", c.ActiveHostWindow)
	}
	if c.TLS.Enabled() {
		t.Error("TLS should be disabled when no paths are set")
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(envFrom(map[string]string{
		"OBS_ADDR":                   ":9000",
		"OBS_DATABASE_URL":           "postgresql://u:p@db:5432/obs",
		"OBS_DB_MAX_CONNS":           "42",
		"OBS_ACTIVE_HOST_WINDOW_SEC": "60",
		"OBS_AUTO_MIGRATE":           "false",
		"OBS_TLS_CA_FILE":            "/ca.pem",
		"OBS_TLS_CERT_FILE":          "/cert.pem",
		"OBS_TLS_KEY_FILE":           "/key.pem",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":9000" || c.DBMaxConns != 42 || c.ActiveHostWindow != time.Minute {
		t.Errorf("overrides not applied: %+v", c)
	}
	if c.AutoMigrate {
		t.Error("AutoMigrate should be false")
	}
	if !c.TLS.Enabled() {
		t.Error("TLS should be enabled with a full bundle")
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"non-postgres dsn", map[string]string{"OBS_DATABASE_URL": "mysql://x"}},
		{"non-postgres migrate dsn", map[string]string{"OBS_MIGRATE_DATABASE_URL": "mysql://x"}},
		{"non-numeric conns", map[string]string{"OBS_DB_MAX_CONNS": "many"}},
		{"zero conns", map[string]string{"OBS_DB_MAX_CONNS": "0"}},
		{"negative host window", map[string]string{"OBS_ACTIVE_HOST_WINDOW_SEC": "-1"}},
		// A half-configured mTLS bundle must fail loudly: silently running the
		// internal listener without client-cert verification breaks the
		// "internal mTLS required" invariant.
		{"partial tls", map[string]string{"OBS_TLS_CA_FILE": "/ca.pem"}},
		{"partial tls 2", map[string]string{"OBS_TLS_CERT_FILE": "/c.pem", "OBS_TLS_KEY_FILE": "/k.pem"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(envFrom(tc.env)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}
