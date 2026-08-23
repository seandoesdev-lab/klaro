// Command obsplane is the always-on observability control plane.
//
// The whole of design section 6: platform plumbing, migrations, tenancy with a
// durable backend-tenant mapping, observability keys, the live path from
// Collector replica to WebSocket, the Explorer read proxy, alerting through
// vmalert, plan retention with metric downsampling, usage metering, saved
// dashboards, and the report snapshot adapter.
//
// Two listeners, on purpose:
//
//   - the public one serves org-scoped REST under a bearer credential;
//   - the internal one serves the Collector and vmalert under mTLS.
//
// Keeping them apart means a request that lands on the public port can never
// reach /internal, whatever a future routing mistake looks like.
//
// Unlike the S1 load-test workers, this process is meant to stay up: the
// "worker idle=0" reclamation rule applies to job workers, not to the
// observability plane.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/observability/internal/alerting"
	"github.com/klaro/observability/internal/api"
	"github.com/klaro/observability/internal/dashboards"
	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/ingestkey"
	"github.com/klaro/observability/internal/live"
	"github.com/klaro/observability/internal/plans"
	"github.com/klaro/observability/internal/platform/audit"
	"github.com/klaro/observability/internal/platform/config"
	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/jwtauth"
	"github.com/klaro/observability/internal/platform/mtls"
	"github.com/klaro/observability/internal/platform/redisx"
	"github.com/klaro/observability/internal/retention"
	"github.com/klaro/observability/internal/snapshot"
	"github.com/klaro/observability/internal/tenancy"
	"github.com/klaro/observability/internal/tenants"
	"github.com/klaro/observability/internal/usage"
	"github.com/klaro/observability/migrations"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Built before anything is dialled: a process that cannot verify a
	// credential has no business opening a database pool.
	auth, err := authenticator(cfg)
	if err != nil {
		return err
	}

	if cfg.AutoMigrate {
		if err := migrate(ctx, cfg); err != nil {
			return err
		}
	}

	database, err := db.New(ctx, db.Options{DSN: cfg.DatabaseURL, MaxConns: cfg.DBMaxConns})
	if err != nil {
		return err
	}
	defer database.Close()

	// Refuse to serve traffic on a connection that can see through RLS. Every
	// isolation guarantee below this line depends on it.
	if err := database.AssertRLSEnforced(ctx); err != nil {
		return err
	}

	signaler := redisx.NewRedis(cfg.RedisAddr)
	defer func() { _ = signaler.Close() }()

	// PGMapper persists org -> VM AccountID (migration 0008). The Collector now
	// routes real series by that number, so an in-memory assignment that
	// renumbers on restart would reassign already-written data to another org.
	tenantMapper := tenants.NewPGMapper(database)
	keys := ingestkey.NewStore(database)
	authorizer := ingestkey.NewAuthorizer(database, keys, tenantMapper, ingestkey.AuthorizerOptions{
		ActiveHostWindow: cfg.ActiveHostWindow,
		TouchWindow:      cfg.KeyTouchWindow,
		CacheTTL:         cfg.AuthzCacheTTL,
	})

	// The hub multiplexes one Redis subscription per org+stream out to every
	// socket watching it, so a dashboard open on twenty screens is one
	// subscription rather than twenty.
	hub := live.NewHub(signaler)
	planStore := plans.New(database)
	// The Explorer enforces plan retention on the read path. It has to: no
	// backend here can delete a time range, so "not queryable past the plan" is
	// only true if the query itself is narrowed.
	explore := explorer.New(explorer.Config{
		VMSelectURL: cfg.VMSelectURL,
		TempoURL:    cfg.TempoURL,
		LokiURL:     cfg.LokiURL,
		Timeout:     cfg.ExplorerTimeout,
		MaxLimit:    cfg.ExplorerMaxRows,
	}, tenantMapper, func(ctx context.Context, orgID string) (explorer.Retention, error) {
		p, err := planStore.Get(ctx, orgID)
		if err != nil {
			return explorer.Retention{}, err
		}
		return explorer.Retention{
			Metrics: p.MetricsRetention,
			Traces:  p.TracesRetention,
			Logs:    p.LogsRetention,
			Rollup:  p.RollupRetention,
		}, nil
	})

	rules := alerting.NewStore(database)
	ruleSync := alerting.NewSyncer(rules, planStore, tenantMapper, alerting.SyncerOptions{
		Path:      cfg.RuleFile,
		ReloadURL: cfg.VMAlertReloadURL,
		Render: alerting.RenderOptions{
			Interval:         cfg.RuleSyncInterval,
			DashboardBaseURL: cfg.DashboardBaseURL,
		},
	})
	usageStore := usage.New(database)
	dashboardStore := dashboards.NewStore(database)
	// The snapshot adapter reads through the Explorer, so the report path
	// inherits the org enforcement, retention clamp and rollup fallback instead
	// of getting a second read path to forget them in.
	snapshots := snapshot.New(explore)

	public := &http.Server{
		Addr: cfg.Addr,
		Handler: api.NewRouter(api.Deps{
			DB:               database,
			Signal:           signaler,
			Tenants:          tenantMapper,
			Auth:             auth,
			Keys:             keys,
			Authz:            authorizer,
			Audit:            audit.NewPG(database),
			RotationGrace:    cfg.RotationGrace,
			ActiveHostWindow: cfg.ActiveHostWindow,
			Live:             hub,
			Explorer:         explore,
			Rules:            rules,
			RuleSync:         ruleSync,
			Dashboards:       dashboardStore,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	internal, err := internalServer(cfg, api.InternalDeps{
		Authz:     authorizer,
		Signal:    signaler,
		Alerts:    alerting.NewReceiver(rules, signaler),
		Usage:     usageStore,
		Snapshots: snapshots,
		// The internal plane authenticates independently of its transport, so
		// this stays set whether or not mTLS is in front of it.
		Token: cfg.InternalToken,
	})
	if err != nil {
		return err
	}

	// Background workers. Each one is a loop that logs its own failures: a
	// retention pass that cannot reach Loki must not take the API down with it.
	go ruleSync.Run(ctx, cfg.RuleSyncInterval)
	go alerting.RunNotifier(ctx, signaler, alerting.NewRouter(
		alerting.SMTPNotifier{Addr: cfg.SMTPAddr, From: cfg.SMTPFrom},
		alerting.SlackNotifier{WebhookURL: cfg.SlackWebhookURL},
	))
	go retention.New(retention.Config{
		VMSelectURL: cfg.VMSelectURL,
		LokiURL:     cfg.LokiURL,
		TempoURL:    cfg.TempoURL,
		DryRun:      cfg.RetentionDryRun,
	}, planStore, tenantMapper).Run(ctx, cfg.RetentionInterval)
	go usage.NewEmitter(usageStore, planStore, signaler, cfg.ActiveHostWindow).
		Run(ctx, cfg.UsageInterval)

	// Either listener failing is fatal: without the internal one no telemetry
	// can be authorised, so limping along on the public one only hides the
	// outage.
	serveErr := make(chan error, 2)
	go func() {
		log.Printf("obsplane public listener on %s", public.Addr)
		serveErr <- public.ListenAndServe()
	}()
	if internal != nil {
		go func() {
			if internal.TLSConfig != nil {
				log.Printf("obsplane internal listener on %s (mTLS)", internal.Addr)
				serveErr <- internal.ListenAndServeTLS("", "")
				return
			}
			log.Printf("obsplane internal listener on %s (PLAINTEXT - dev only)", internal.Addr)
			serveErr <- internal.ListenAndServe()
		}()
	}

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if internal != nil {
			if err := internal.Shutdown(shutdownCtx); err != nil {
				return err
			}
		}
		if err := public.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

// authenticator builds the public plane's credential from the configuration.
//
// JWT is the default and the only production path; the dev stub is reachable
// only when OBS_DEV_AUTH was set, which config.Load refuses outright in the
// production profile. Either way this returns an authenticator or an error -
// there is no "no authentication" branch to fall into.
func authenticator(cfg config.Config) (tenancy.Authenticator, error) {
	a := cfg.Auth
	if a.DevStub {
		role, ok := tenancy.ParseRole(a.DevRole)
		if !ok {
			// config.Load already checked this; keeping the check means a
			// future config change cannot smuggle an unusable role through.
			return nil, fmt.Errorf("OBS_DEV_ROLE %q is not a klaro role", a.DevRole)
		}
		log.Printf("WARNING: public plane is using the development auth stub "+
			"(OBS_DEV_AUTH): one static token authenticates as %s of org %s",
			role, a.DevOrgID)
		return tenancy.DevTokenAuthenticator(a.DevToken, a.DevOrgID, role), nil
	}

	opts := jwtauth.Options{
		Alg:      a.JWTAlg,
		HSSecret: a.JWTHSSecret,
		Issuer:   a.JWTIssuer,
		Audience: a.JWTAudience,
		Leeway:   a.JWTLeeway,
	}
	if a.JWTAlg == jwtauth.RS256 {
		key, err := jwtauth.ParseRSAPublicKeyFile(a.JWTPublicKeyFile)
		if err != nil {
			return nil, err
		}
		opts.RSAPublicKey = key
	}
	verifier, err := jwtauth.New(opts)
	if err != nil {
		return nil, err
	}
	// Neither iss nor aud is required, but without them any token this key
	// signs is accepted - including one minted for a different klaro service.
	// Worth saying out loud once at boot.
	if a.JWTIssuer == "" || a.JWTAudience == "" {
		log.Print("note: OBS_JWT_ISSUER/OBS_JWT_AUDIENCE are not both set; " +
			"any token signed by the configured key will be accepted")
	}
	log.Printf("public plane authenticating with %s bearer JWT", a.JWTAlg)
	return tenancy.JWTAuthenticator(verifier), nil
}

// internalServer builds the Collector-facing listener, or nil when it is turned
// off entirely.
//
// mTLS is the contract for this hop (CLAUDE.md), so a missing certificate
// bundle does not quietly become a plaintext port: it either disables the
// listener or, when the operator explicitly asked for it with
// OBS_INTERNAL_INSECURE, serves plaintext and says so in the log on every boot.
//
// Plaintext is not anonymous. api.InternalAuth still requires the internal
// token on every route, and config.Load refuses OBS_INTERNAL_INSECURE without
// one, so relaxing the transport no longer relaxes authentication with it.
func internalServer(cfg config.Config, deps api.InternalDeps) (*http.Server, error) {
	if cfg.InternalAddr == "" {
		return nil, nil
	}
	srv := &http.Server{
		Addr:              cfg.InternalAddr,
		Handler:           api.NewInternalRouter(deps),
		ReadHeaderTimeout: 10 * time.Second,
	}

	switch {
	case cfg.TLS.Enabled():
		tlsCfg, err := mtls.ServerConfig(mtls.Files{
			CAFile:   cfg.TLS.CAFile,
			CertFile: cfg.TLS.CertFile,
			KeyFile:  cfg.TLS.KeyFile,
		})
		if err != nil {
			return nil, err
		}
		srv.TLSConfig = tlsCfg
		return srv, nil
	case cfg.InternalInsecure:
		log.Print("WARNING: internal plane transport is plaintext " +
			"(OBS_INTERNAL_INSECURE); requests are still authenticated with " +
			"OBS_INTERNAL_TOKEN, but the token and every payload cross the " +
			"network in the clear. Set OBS_TLS_CA_FILE/CERT/KEY for the mTLS " +
			"the design requires.")
		return srv, nil
	default:
		log.Print("internal plane disabled: no mTLS bundle configured and " +
			"OBS_INTERNAL_INSECURE is not set; the Collector cannot authorize keys")
		return nil, nil
	}
}

// migrate applies the schema on a separate admin pool: the request-serving role
// is intentionally NOSUPERUSER and cannot run DDL or CREATE ROLE.
func migrate(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.MigrateDatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		log.Print("migrations: already up to date")
	} else {
		log.Printf("migrations: applied %v", applied)
	}
	return nil
}
