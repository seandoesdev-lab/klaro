package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klaro/observability/internal/platform/jwtauth"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// devSecret is a 32-byte HS256 secret, the floor Load enforces.
const devSecret = "0123456789abcdef0123456789abcdef"

// baseEnv is the smallest environment that loads: a JWT key, and nothing else.
// Everything a laptop needs still has a default, but the verification key does
// not - there is no safe value for "how do we check signatures".
func baseEnv(extra map[string]string) map[string]string {
	m := map[string]string{"OBS_JWT_HS_SECRET": devSecret}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(envFrom(baseEnv(nil)))
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
	// The default profile is development, and JWT is the default credential -
	// the dev stub has to be asked for.
	if c.Env != EnvDevelopment {
		t.Errorf("Env = %q, want development", c.Env)
	}
	if c.Auth.DevStub {
		t.Error("the dev auth stub must not be on by default")
	}
	if c.Auth.JWTAlg != jwtauth.HS256 {
		t.Errorf("JWTAlg = %q, want HS256", c.Auth.JWTAlg)
	}
	if c.Auth.JWTLeeway != time.Minute {
		t.Errorf("JWTLeeway = %v, want 1m", c.Auth.JWTLeeway)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(envFrom(baseEnv(map[string]string{
		"OBS_ADDR":                   ":9000",
		"OBS_DATABASE_URL":           "postgresql://u:p@db:5432/obs",
		"OBS_DB_MAX_CONNS":           "42",
		"OBS_ACTIVE_HOST_WINDOW_SEC": "60",
		"OBS_AUTO_MIGRATE":           "false",
		"OBS_TLS_CA_FILE":            "/ca.pem",
		"OBS_TLS_CERT_FILE":          "/cert.pem",
		"OBS_TLS_KEY_FILE":           "/key.pem",
	})))
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
		{"unknown profile", map[string]string{"OBS_ENV": "staging"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(envFrom(baseEnv(tc.env))); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

// There is no "no authentication" mode: without a JWT key and without an
// explicit dev stub the process must refuse to start rather than serve
// something open.
func TestLoadRefusesToStartWithNoCredentialConfigured(t *testing.T) {
	_, err := Load(envFrom(nil))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if !strings.Contains(err.Error(), "OBS_JWT_HS_SECRET") {
		t.Errorf("error should name the missing key variable: %v", err)
	}
}

func TestLoadRejectsWeakOrUnusableJWTKeyMaterial(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"short secret", map[string]string{"OBS_JWT_HS_SECRET": "too-short"}},
		{"unknown alg", map[string]string{"OBS_JWT_ALG": "HS512"}},
		{"rs256 without key file", map[string]string{"OBS_JWT_ALG": "RS256"}},
		{"negative leeway", map[string]string{"OBS_JWT_LEEWAY_SEC": "-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(envFrom(baseEnv(tc.env))); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

// Both spellings of the secret at once is ambiguous: whichever one wins, half
// the operators reading the config will be wrong about which is in force.
func TestLoadRejectsBothSecretForms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jwt.secret")
	if err := os.WriteFile(path, []byte(devSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := baseEnv(map[string]string{"OBS_JWT_HS_SECRET_FILE": path})
	if _, err := Load(envFrom(env)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

// The file form is what a mounted Kubernetes secret looks like, trailing
// newline included.
func TestLoadReadsTheSecretFromAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jwt.secret")
	if err := os.WriteFile(path, []byte(devSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(envFrom(map[string]string{"OBS_JWT_HS_SECRET_FILE": path}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(c.Auth.JWTHSSecret) != devSecret {
		t.Errorf("secret = %q, want the file contents without the newline", c.Auth.JWTHSSecret)
	}
}

func TestDevAuthStubNeedsItsOwnConfiguration(t *testing.T) {
	// Enabled and fully configured: fine in development, and no JWT key needed.
	c, err := Load(envFrom(map[string]string{
		"OBS_DEV_AUTH":   "true",
		"OBS_DEV_TOKEN":  "dev",
		"OBS_DEV_ORG_ID": "00000000-0000-0000-0000-000000000001",
		"OBS_DEV_ROLE":   "admin",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Auth.DevStub || c.Auth.DevRole != "admin" {
		t.Errorf("auth = %+v", c.Auth)
	}

	// Enabled but incomplete, or with a role outside the four: refused.
	for name, env := range map[string]map[string]string{
		"no token": {"OBS_DEV_AUTH": "true", "OBS_DEV_ORG_ID": "00000000-0000-0000-0000-000000000001"},
		"no org":   {"OBS_DEV_AUTH": "true", "OBS_DEV_TOKEN": "dev"},
		"bad role": {
			"OBS_DEV_AUTH":   "true",
			"OBS_DEV_TOKEN":  "dev",
			"OBS_DEV_ORG_ID": "00000000-0000-0000-0000-000000000001",
			"OBS_DEV_ROLE":   "superadmin",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(envFrom(env)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

// F-3: plaintext transport on the internal plane is a development convenience.
// It must not also mean "unauthenticated", so it requires the internal token.
func TestInternalInsecureRequiresTheInternalToken(t *testing.T) {
	_, err := Load(envFrom(baseEnv(map[string]string{"OBS_INTERNAL_INSECURE": "true"})))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if !strings.Contains(err.Error(), "OBS_INTERNAL_TOKEN") {
		t.Errorf("error should name the missing token: %v", err)
	}

	c, err := Load(envFrom(baseEnv(map[string]string{
		"OBS_INTERNAL_INSECURE": "true",
		"OBS_INTERNAL_TOKEN":    "an-internal-token-long-enough-to-pass",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.InternalInsecure || c.InternalToken == "" {
		t.Errorf("config = %+v", c)
	}
}

func TestInternalTokenMustNotBeGuessable(t *testing.T) {
	if _, err := Load(envFrom(baseEnv(map[string]string{"OBS_INTERNAL_TOKEN": "short"}))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

// The production profile removes every development relaxation. Each of these
// would be a real exposure in a deployment serving customer telemetry, so each
// is a boot failure rather than a warning.
func TestProductionProfileRefusesDevelopmentRelaxations(t *testing.T) {
	prod := func(extra map[string]string) map[string]string {
		m := map[string]string{
			"OBS_ENV":                  "production",
			"OBS_JWT_HS_SECRET":        devSecret,
			"OBS_DATABASE_URL":         "postgres://u:p@db:5432/obs?sslmode=require",
			"OBS_MIGRATE_DATABASE_URL": "postgres://a:p@db:5432/obs?sslmode=require",
			"OBS_TLS_CA_FILE":          "/ca.pem",
			"OBS_TLS_CERT_FILE":        "/cert.pem",
			"OBS_TLS_KEY_FILE":         "/key.pem",
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	if _, err := Load(envFrom(prod(nil))); err != nil {
		t.Fatalf("a well-configured production environment must load: %v", err)
	}

	tests := map[string]map[string]string{
		"dev auth stub": {
			"OBS_DEV_AUTH":   "true",
			"OBS_DEV_TOKEN":  "dev",
			"OBS_DEV_ORG_ID": "00000000-0000-0000-0000-000000000001",
		},
		"plaintext internal plane": {
			"OBS_INTERNAL_INSECURE": "true",
			"OBS_INTERNAL_TOKEN":    "an-internal-token-long-enough-to-pass",
		},
		// F-5: this DSN carries every tenant's rows and the org scope that
		// separates them.
		"sslmode disable":         {"OBS_DATABASE_URL": "postgres://u:p@db:5432/obs?sslmode=disable"},
		"sslmode prefer":          {"OBS_DATABASE_URL": "postgres://u:p@db:5432/obs?sslmode=prefer"},
		"sslmode allow":           {"OBS_DATABASE_URL": "postgres://u:p@db:5432/obs?sslmode=allow"},
		"sslmode unset":           {"OBS_DATABASE_URL": "postgres://u:p@db:5432/obs"},
		"migrate sslmode disable": {"OBS_MIGRATE_DATABASE_URL": "postgres://a:p@db:5432/obs?sslmode=disable"},
	}
	for name, extra := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(envFrom(prod(extra))); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}

	// verify-ca and verify-full are stronger than require, so both are allowed.
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		t.Run("sslmode "+mode, func(t *testing.T) {
			env := prod(map[string]string{
				"OBS_DATABASE_URL": "postgres://u:p@db:5432/obs?sslmode=" + mode,
			})
			if _, err := Load(envFrom(env)); err != nil {
				t.Fatalf("sslmode=%s rejected: %v", mode, err)
			}
		})
	}
}

// F-5: the built-in default differs by profile, so a deployment that never sets
// a DSN does not get a plaintext one in production.
func TestDefaultDSNEncryptionFollowsTheProfile(t *testing.T) {
	dev, err := Load(envFrom(baseEnv(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got := sslMode(dev.DatabaseURL); got != "disable" {
		t.Errorf("development default sslmode = %q, want disable", got)
	}
	if got := sslMode(dev.MigrateDatabaseURL); got != "disable" {
		t.Errorf("development default migrate sslmode = %q, want disable", got)
	}

	// In production the default is require. It is still a localhost DSN nobody
	// should rely on, but it cannot be a plaintext one.
	for _, role := range []string{"klaro_obs_app", "klaro"} {
		if got := sslMode(defaultDSN(role, EnvProduction)); got != "require" {
			t.Errorf("production default sslmode for %s = %q, want require", role, got)
		}
	}
}
