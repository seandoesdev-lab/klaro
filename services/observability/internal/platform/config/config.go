// Package config loads obsplane settings from the environment.
//
// Everything has a local-development default so `go run ./cmd/obsplane` works
// against docker-compose without a .env file. Load reports the values it had to
// reject rather than silently falling back, because a mistyped DSN or a missing
// mTLS bundle must not degrade into an insecure default.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved obsplane configuration.
type Config struct {
	// HTTP
	Addr string

	// Postgres. The DSN must point at a NON-superuser role: FORCE ROW LEVEL
	// SECURITY does not apply to superusers or BYPASSRLS roles, so connecting
	// as one silently disables tenant isolation.
	DatabaseURL string
	// MigrateDatabaseURL is a separate admin DSN: the request-serving role is
	// NOSUPERUSER and cannot run DDL or CREATE ROLE.
	MigrateDatabaseURL string
	DBMaxConns         int32
	DBConnectTimeout   time.Duration

	// Redis (live metric fan-out, S1 Signaler pattern).
	RedisAddr string

	// Internal mTLS listener (Collector -> CP, vmalert -> CP). Empty paths mean
	// the internal listener is disabled, which is only acceptable in dev.
	InternalAddr string
	TLS          TLSPaths

	// InternalInsecure serves the internal plane in plaintext. mTLS is the
	// contract for Collector -> CP (CLAUDE.md), so this exists only so a local
	// docker-compose run can work before certificates are minted, and it has to
	// be asked for explicitly - a missing bundle never silently downgrades.
	InternalInsecure bool

	// RotationGrace is how long a rotated observability key keeps being accepted
	// alongside its replacement (design HOW-4).
	RotationGrace time.Duration
	// AuthzCacheTTL is how long the Collector may reuse an ingest-key grant. It
	// is also the worst-case delay before a revocation takes effect.
	AuthzCacheTTL time.Duration
	// KeyTouchWindow rate-limits observability_keys.last_used_at writes.
	KeyTouchWindow time.Duration

	// AutoMigrate applies migrations/*.sql on boot (dev convenience).
	AutoMigrate bool

	// ActiveHostWindow is how recently observability_hosts.last_seen_at must be
	// for a host to count against the host quota.
	ActiveHostWindow time.Duration
}

// TLSPaths locates the mTLS material for the internal listener.
type TLSPaths struct {
	CAFile   string
	CertFile string
	KeyFile  string
}

// Enabled reports whether a complete mTLS bundle was configured.
func (t TLSPaths) Enabled() bool {
	return t.CAFile != "" && t.CertFile != "" && t.KeyFile != ""
}

// partial reports whether some but not all TLS paths were set - always a
// misconfiguration, never a valid "disabled" state.
func (t TLSPaths) partial() bool {
	n := 0
	for _, p := range []string{t.CAFile, t.CertFile, t.KeyFile} {
		if p != "" {
			n++
		}
	}
	return n > 0 && n < 3
}

// ErrInvalid is returned by Load when the environment is unusable.
var ErrInvalid = errors.New("invalid configuration")

// Load reads the environment and validates it.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:               str(getenv, "OBS_ADDR", ":8090"),
		DatabaseURL:        str(getenv, "OBS_DATABASE_URL", "postgres://klaro_obs_app:klaro_obs_app@localhost:5432/klaro_obs?sslmode=disable"),
		MigrateDatabaseURL: str(getenv, "OBS_MIGRATE_DATABASE_URL", "postgres://klaro:klaro@localhost:5432/klaro_obs?sslmode=disable"),
		RedisAddr:          str(getenv, "OBS_REDIS_ADDR", "localhost:6379"),
		InternalAddr:       str(getenv, "OBS_INTERNAL_ADDR", ":8443"),
		AutoMigrate:        boolean(getenv, "OBS_AUTO_MIGRATE", true),
		InternalInsecure:   boolean(getenv, "OBS_INTERNAL_INSECURE", false),
		DBConnectTimeout:   10 * time.Second,
		TLS: TLSPaths{
			CAFile:   getenv("OBS_TLS_CA_FILE"),
			CertFile: getenv("OBS_TLS_CERT_FILE"),
			KeyFile:  getenv("OBS_TLS_KEY_FILE"),
		},
	}

	var errs []string

	n, err := integer(getenv, "OBS_DB_MAX_CONNS", 10)
	if err != nil {
		errs = append(errs, err.Error())
	} else if n < 1 {
		errs = append(errs, "OBS_DB_MAX_CONNS must be >= 1")
	} else {
		c.DBMaxConns = int32(n)
	}

	// Seconds-valued settings. Each rejects zero rather than falling back: a
	// zero window would silently mean "no hosts are active" or "no grace at
	// all", both of which look like working configuration.
	for _, d := range []struct {
		key    string
		def    int
		target *time.Duration
	}{
		{"OBS_ACTIVE_HOST_WINDOW_SEC", 900, &c.ActiveHostWindow},
		{"OBS_KEY_ROTATION_GRACE_SEC", 86400, &c.RotationGrace},
		{"OBS_AUTHZ_CACHE_TTL_SEC", 30, &c.AuthzCacheTTL},
		{"OBS_KEY_TOUCH_WINDOW_SEC", 60, &c.KeyTouchWindow},
	} {
		secs, err := integer(getenv, d.key, d.def)
		switch {
		case err != nil:
			errs = append(errs, err.Error())
		case secs < 1:
			errs = append(errs, d.key+" must be >= 1")
		default:
			*d.target = time.Duration(secs) * time.Second
		}
	}

	if !strings.HasPrefix(c.DatabaseURL, "postgres://") && !strings.HasPrefix(c.DatabaseURL, "postgresql://") {
		errs = append(errs, "OBS_DATABASE_URL must be a postgres:// DSN")
	}
	if !strings.HasPrefix(c.MigrateDatabaseURL, "postgres://") && !strings.HasPrefix(c.MigrateDatabaseURL, "postgresql://") {
		errs = append(errs, "OBS_MIGRATE_DATABASE_URL must be a postgres:// DSN")
	}
	if c.TLS.partial() {
		errs = append(errs, "OBS_TLS_CA_FILE, OBS_TLS_CERT_FILE and OBS_TLS_KEY_FILE must be set together")
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("%w: %s", ErrInvalid, strings.Join(errs, "; "))
	}
	return c, nil
}

// LoadFromEnv is Load bound to the process environment.
func LoadFromEnv() (Config, error) { return Load(os.Getenv) }

func str(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

func integer(getenv func(string) string, key string, def int) (int, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, v)
	}
	return n, nil
}

func boolean(getenv func(string) string, key string, def bool) bool {
	v := getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
