// Package config loads obsplane settings from the environment.
//
// Two rules shape everything here:
//
//   - Load reports the values it had to reject rather than silently falling
//     back, because a mistyped DSN or a missing mTLS bundle must not degrade
//     into an insecure default.
//   - the deployment profile (OBS_ENV) decides which conveniences exist at all.
//     Local development gets plaintext transport, a stub credential and an
//     unencrypted database connection; production gets none of them, and asking
//     for one there is a boot failure rather than a log line nobody reads.
//
// Development still has a working default for everything except the two secrets
// that cannot have one - the JWT verification key, and the internal token when
// the internal plane is served in plaintext. Those fail closed.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/klaro/observability/internal/platform/jwtauth"
)

// Environment is the deployment profile.
type Environment string

// The two profiles. There is no "staging": a staging deployment is production
// with different data, and giving it its own relaxations is how a relaxation
// reaches production.
const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
)

// IsProduction reports whether the hardened profile is in force.
func (e Environment) IsProduction() bool { return e == EnvProduction }

// Config is the fully resolved obsplane configuration.
type Config struct {
	// Env is the deployment profile every guard below keys off.
	Env Environment

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

	// Auth is how a caller on the public plane is identified.
	Auth AuthConfig

	// Internal mTLS listener (Collector -> CP, vmalert -> CP). Empty paths mean
	// the internal listener is disabled, which is only acceptable in dev.
	InternalAddr string
	TLS          TLSPaths

	// InternalInsecure serves the internal plane in plaintext. mTLS is the
	// contract for Collector -> CP (CLAUDE.md), so this exists only so a local
	// docker-compose run can work before certificates are minted, and it has to
	// be asked for explicitly - a missing bundle never silently downgrades.
	//
	// It relaxes the transport ONLY. Requests are still authenticated with
	// InternalToken (api.InternalAuth); turning off encryption and turning off
	// authentication used to be the same switch, and no longer is.
	InternalInsecure bool
	// InternalToken is the shared secret the Collector and vmalert present to
	// the internal plane. Required whenever that plane runs without mTLS.
	InternalToken string

	// RotationGrace is how long a rotated observability key keeps being accepted
	// alongside its replacement (design HOW-4).
	RotationGrace time.Duration
	// AuthzCacheTTL is how long the Collector may reuse an ingest-key grant. It
	// is also the worst-case delay before a revocation takes effect.
	AuthzCacheTTL time.Duration
	// KeyTouchWindow rate-limits observability_keys.last_used_at writes.
	KeyTouchWindow time.Duration

	// Alerting. RuleFile is what vmalert reads; empty disables rule syncing so a
	// deployment without vmalert stays quiet instead of failing every interval.
	RuleFile         string
	VMAlertReloadURL string
	RuleSyncInterval time.Duration
	// DashboardBaseURL is prefixed to the link carried in a notification.
	DashboardBaseURL string
	// SMTPAddr and SMTPFrom point at MailHog in development, which accepts
	// anything and shows it in a UI, so no real mail is ever sent by accident.
	SMTPAddr        string
	SMTPFrom        string
	SlackWebhookURL string

	// Retention and usage job cadence.
	RetentionInterval time.Duration
	RetentionDryRun   bool
	UsageInterval     time.Duration

	// Telemetry backends the Explorer proxies to. An empty URL disables that
	// signal rather than dialling nothing: a deployment without Tempo should
	// say "no traces backend", not time out.
	VMSelectURL string
	TempoURL    string
	LokiURL     string
	// ExplorerTimeout bounds one backend query.
	ExplorerTimeout time.Duration
	// ExplorerMaxRows caps rows per query, so one request cannot pull a
	// retention window into memory.
	ExplorerMaxRows int

	// AutoMigrate applies migrations/*.sql on boot (dev convenience).
	AutoMigrate bool

	// ActiveHostWindow is how recently observability_hosts.last_seen_at must be
	// for a host to count against the host quota.
	ActiveHostWindow time.Duration
}

// AuthConfig is the public plane's credential configuration.
//
// JWT is the default and the only production option. The dev stub is a
// separate, explicitly requested mode rather than a fallback, so a deployment
// cannot end up on it by leaving something unset.
type AuthConfig struct {
	// DevStub replaces JWT verification with a single fixed bearer token. Only
	// reachable with OBS_DEV_AUTH=true, and never in production.
	DevStub bool
	// DevToken, DevOrgID and DevRole configure that stub.
	DevToken string
	DevOrgID string
	DevRole  string

	// JWTAlg is the one algorithm accepted (HS256 or RS256). Accepting whatever
	// a token declares is the classic JWT break, so this is a deployment
	// decision rather than a per-token one.
	JWTAlg jwtauth.Alg
	// JWTHSSecret is the HS256 shared secret, from OBS_JWT_HS_SECRET or
	// OBS_JWT_HS_SECRET_FILE.
	JWTHSSecret []byte
	// JWTPublicKeyFile is the RS256 verification key (PEM).
	JWTPublicKeyFile string
	// JWTIssuer and JWTAudience, when set, must match the token's iss and aud.
	JWTIssuer   string
	JWTAudience string
	// JWTLeeway absorbs clock skew between the issuer and this process.
	JWTLeeway time.Duration
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

// minInternalToken is the shortest internal token accepted. It is a machine
// credential with no rate limiting in front of it, so a human-memorable value
// is not good enough.
const minInternalToken = 24

// minJWTSecret mirrors jwtauth.New's floor, so a weak secret is reported at
// boot with the name of the variable that carries it rather than as an opaque
// verifier error later.
const minJWTSecret = 32

// Load reads the environment and validates it.
func Load(getenv func(string) string) (Config, error) {
	env, envErr := environment(getenv)

	c := Config{
		Env:                env,
		Addr:               str(getenv, "OBS_ADDR", ":8090"),
		DatabaseURL:        str(getenv, "OBS_DATABASE_URL", defaultDSN("klaro_obs_app", env)),
		MigrateDatabaseURL: str(getenv, "OBS_MIGRATE_DATABASE_URL", defaultDSN("klaro", env)),
		RedisAddr:          str(getenv, "OBS_REDIS_ADDR", "localhost:6379"),
		InternalAddr:       str(getenv, "OBS_INTERNAL_ADDR", ":8443"),
		AutoMigrate:        boolean(getenv, "OBS_AUTO_MIGRATE", true),
		InternalInsecure:   boolean(getenv, "OBS_INTERNAL_INSECURE", false),
		InternalToken:      getenv("OBS_INTERNAL_TOKEN"),
		VMSelectURL:        getenv("OBS_VMSELECT_URL"),
		TempoURL:           getenv("OBS_TEMPO_URL"),
		LokiURL:            getenv("OBS_LOKI_URL"),
		RuleFile:           getenv("OBS_RULE_FILE"),
		VMAlertReloadURL:   getenv("OBS_VMALERT_RELOAD_URL"),
		DashboardBaseURL:   getenv("OBS_DASHBOARD_BASE_URL"),
		SMTPAddr:           getenv("OBS_SMTP_ADDR"),
		SMTPFrom:           str(getenv, "OBS_SMTP_FROM", "alerts@klaro.local"),
		SlackWebhookURL:    getenv("OBS_SLACK_WEBHOOK_URL"),
		RetentionDryRun:    boolean(getenv, "OBS_RETENTION_DRY_RUN", false),
		DBConnectTimeout:   10 * time.Second,
		TLS: TLSPaths{
			CAFile:   getenv("OBS_TLS_CA_FILE"),
			CertFile: getenv("OBS_TLS_CERT_FILE"),
			KeyFile:  getenv("OBS_TLS_KEY_FILE"),
		},
	}

	var errs []string
	if envErr != "" {
		errs = append(errs, envErr)
	}

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
		{"OBS_EXPLORER_TIMEOUT_SEC", 30, &c.ExplorerTimeout},
		{"OBS_RULE_SYNC_INTERVAL_SEC", 60, &c.RuleSyncInterval},
		{"OBS_RETENTION_INTERVAL_SEC", 21600, &c.RetentionInterval},
		{"OBS_USAGE_INTERVAL_SEC", 3600, &c.UsageInterval},
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

	rows, err := integer(getenv, "OBS_EXPLORER_MAX_ROWS", 1000)
	switch {
	case err != nil:
		errs = append(errs, err.Error())
	case rows < 1:
		errs = append(errs, "OBS_EXPLORER_MAX_ROWS must be >= 1")
	default:
		c.ExplorerMaxRows = rows
	}

	auth, authErrs := loadAuth(getenv, env)
	c.Auth = auth
	errs = append(errs, authErrs...)

	errs = append(errs, validateDSNs(c, env)...)

	if c.TLS.partial() {
		errs = append(errs, "OBS_TLS_CA_FILE, OBS_TLS_CERT_FILE and OBS_TLS_KEY_FILE must be set together")
	}
	errs = append(errs, validateInternalPlane(c, env)...)

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("%w: %s", ErrInvalid, strings.Join(errs, "; "))
	}
	return c, nil
}

// environment resolves OBS_ENV, returning a message rather than an error so
// Load can collect it alongside everything else that is wrong.
func environment(getenv func(string) string) (Environment, string) {
	switch v := strings.ToLower(strings.TrimSpace(getenv("OBS_ENV"))); v {
	case "":
		// Defaulting to development is the safe direction: the guards it skips
		// are the ones a laptop needs, and every one of them still has to be
		// asked for explicitly (OBS_DEV_AUTH, OBS_INTERNAL_INSECURE) - so the
		// default profile on its own relaxes nothing. The compose file sets
		// this explicitly, as a production deployment should.
		return EnvDevelopment, ""
	case string(EnvDevelopment), "dev":
		return EnvDevelopment, ""
	case string(EnvProduction), "prod":
		return EnvProduction, ""
	default:
		return EnvDevelopment, fmt.Sprintf("OBS_ENV must be development or production, got %q", v)
	}
}

// loadAuth resolves the public plane credential.
func loadAuth(getenv func(string) string, env Environment) (AuthConfig, []string) {
	a := AuthConfig{
		DevStub:          boolean(getenv, "OBS_DEV_AUTH", false),
		DevToken:         getenv("OBS_DEV_TOKEN"),
		DevOrgID:         getenv("OBS_DEV_ORG_ID"),
		DevRole:          str(getenv, "OBS_DEV_ROLE", "owner"),
		JWTAlg:           jwtauth.Alg(strings.ToUpper(str(getenv, "OBS_JWT_ALG", string(jwtauth.HS256)))),
		JWTPublicKeyFile: getenv("OBS_JWT_PUBLIC_KEY_FILE"),
		JWTIssuer:        getenv("OBS_JWT_ISSUER"),
		JWTAudience:      getenv("OBS_JWT_AUDIENCE"),
	}

	var errs []string

	leeway, err := integer(getenv, "OBS_JWT_LEEWAY_SEC", 60)
	switch {
	case err != nil:
		errs = append(errs, err.Error())
	case leeway < 0:
		errs = append(errs, "OBS_JWT_LEEWAY_SEC must be >= 0")
	default:
		a.JWTLeeway = time.Duration(leeway) * time.Second
	}

	if a.DevStub {
		// The stub is a development affordance. In production it would mean one
		// static string authenticating as an org owner, so it is refused there
		// outright rather than warned about.
		if env.IsProduction() {
			errs = append(errs, "OBS_DEV_AUTH must not be set in the production profile")
		}
		if a.DevToken == "" {
			errs = append(errs, "OBS_DEV_AUTH requires OBS_DEV_TOKEN")
		}
		if a.DevOrgID == "" {
			errs = append(errs, "OBS_DEV_AUTH requires OBS_DEV_ORG_ID")
		}
		switch a.DevRole {
		case "owner", "admin", "member", "viewer":
		default:
			errs = append(errs, fmt.Sprintf("OBS_DEV_ROLE must be owner, admin, member or viewer, got %q", a.DevRole))
		}
		// No JWT key is needed on the stub path, and demanding one would push
		// deployments into configuring a secret they do not use.
		return a, errs
	}

	// JWT is the default, so key material is required. There is no
	// "authentication disabled" mode to fall through to.
	switch a.JWTAlg {
	case jwtauth.HS256:
		secret, secretErrs := hsSecret(getenv)
		a.JWTHSSecret = secret
		errs = append(errs, secretErrs...)
	case jwtauth.RS256:
		if a.JWTPublicKeyFile == "" {
			errs = append(errs, "OBS_JWT_ALG=RS256 requires OBS_JWT_PUBLIC_KEY_FILE")
		}
	default:
		errs = append(errs, fmt.Sprintf("OBS_JWT_ALG must be HS256 or RS256, got %q", a.JWTAlg))
	}
	return a, errs
}

// hsSecret reads the HS256 secret from the environment or from a file. The file
// form exists because a Kubernetes secret is mounted rather than exported, and
// a secret in an env var is readable by anything that can see the process
// environment.
func hsSecret(getenv func(string) string) ([]byte, []string) {
	inline := getenv("OBS_JWT_HS_SECRET")
	path := getenv("OBS_JWT_HS_SECRET_FILE")

	switch {
	case inline != "" && path != "":
		return nil, []string{"set OBS_JWT_HS_SECRET or OBS_JWT_HS_SECRET_FILE, not both"}
	case path != "":
		secret, err := jwtauth.ReadSecretFile(path)
		if err != nil {
			return nil, []string{fmt.Sprintf("OBS_JWT_HS_SECRET_FILE: %v", err)}
		}
		return secret, weakSecret(secret)
	case inline != "":
		return []byte(inline), weakSecret([]byte(inline))
	default:
		return nil, []string{
			"OBS_JWT_ALG=HS256 requires OBS_JWT_HS_SECRET or OBS_JWT_HS_SECRET_FILE " +
				"(set OBS_DEV_AUTH=true for the development stub instead)",
		}
	}
}

func weakSecret(secret []byte) []string {
	if len(secret) < minJWTSecret {
		return []string{fmt.Sprintf("the HS256 JWT secret must be at least %d bytes, got %d",
			minJWTSecret, len(secret))}
	}
	return nil
}

// validateDSNs checks both connection strings, including transport encryption.
func validateDSNs(c Config, env Environment) []string {
	var errs []string
	for _, d := range []struct {
		key string
		dsn string
	}{
		{"OBS_DATABASE_URL", c.DatabaseURL},
		{"OBS_MIGRATE_DATABASE_URL", c.MigrateDatabaseURL},
	} {
		if !strings.HasPrefix(d.dsn, "postgres://") && !strings.HasPrefix(d.dsn, "postgresql://") {
			errs = append(errs, d.key+" must be a postgres:// DSN")
			continue
		}
		if !env.IsProduction() {
			continue
		}
		// In production this connection carries every tenant's data and the org
		// scope that isolates it. sslmode=disable sends it in the clear, and
		// prefer/allow fall back to plaintext without saying so - which is
		// worse than refusing to start (finding F-5).
		switch mode := sslMode(d.dsn); mode {
		case "require", "verify-ca", "verify-full":
		case "":
			errs = append(errs, d.key+" must set sslmode explicitly in the production profile "+
				"(require, verify-ca or verify-full)")
		default:
			errs = append(errs, fmt.Sprintf("%s has sslmode=%s, which the production profile does not allow; "+
				"use require, verify-ca or verify-full", d.key, mode))
		}
	}
	return errs
}

// validateInternalPlane enforces the split between transport and authentication
// on the Collector-facing listener (finding F-3).
func validateInternalPlane(c Config, env Environment) []string {
	if c.InternalAddr == "" {
		// The listener is off entirely; there is nothing to secure.
		return nil
	}
	var errs []string
	if c.InternalInsecure && env.IsProduction() {
		errs = append(errs, "OBS_INTERNAL_INSECURE must not be set in the production profile; "+
			"configure OBS_TLS_CA_FILE/CERT/KEY for the mTLS the design requires")
	}
	// Without mTLS the only thing identifying a caller is the token, so it is
	// required rather than optional. This is the fix for a plaintext switch that
	// used to disable authentication as a side effect.
	if c.InternalInsecure && c.InternalToken == "" {
		errs = append(errs, "OBS_INTERNAL_INSECURE requires OBS_INTERNAL_TOKEN: "+
			"plaintext transport relaxes encryption, not authentication")
	}
	if c.InternalToken != "" && len(c.InternalToken) < minInternalToken {
		errs = append(errs, fmt.Sprintf("OBS_INTERNAL_TOKEN must be at least %d characters, got %d",
			minInternalToken, len(c.InternalToken)))
	}
	return errs
}

// sslMode extracts sslmode from a DSN, returning "" when it is absent.
//
// libpq defaults to prefer, which silently accepts plaintext, so an absent
// sslmode is treated as "not stated" rather than as a safe value.
func sslMode(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Query().Get("sslmode"))
}

// defaultDSN is the local-development connection string for a role.
//
// The sslmode differs by profile: a compose Postgres has no certificates, while
// a production database must not be spoken to in the clear. Production has no
// business using this default at all - validateDSNs is what makes sure a
// deployment that leans on it gets told.
func defaultDSN(role string, env Environment) string {
	mode := "disable"
	if env.IsProduction() {
		mode = "require"
	}
	return fmt.Sprintf("postgres://%s:%s@localhost:5432/klaro_obs?sslmode=%s", role, role, mode)
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
