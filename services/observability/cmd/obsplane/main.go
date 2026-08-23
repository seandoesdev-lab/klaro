// Command obsplane is the always-on observability control plane.
//
// Build-order steps 1-6 (design section 6): platform plumbing, migrations,
// tenancy with a durable backend-tenant mapping, observability keys with the
// Collector authz endpoint, the live path from Collector replica to WebSocket,
// and the Explorer read proxy. Alerting, retention and usage are later steps.
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
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/observability/internal/api"
	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/ingestkey"
	"github.com/klaro/observability/internal/live"
	"github.com/klaro/observability/internal/platform/audit"
	"github.com/klaro/observability/internal/platform/config"
	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/mtls"
	"github.com/klaro/observability/internal/platform/redisx"
	"github.com/klaro/observability/internal/tenancy"
	"github.com/klaro/observability/internal/tenants"
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
	explore := explorer.New(explorer.Config{
		VMSelectURL: cfg.VMSelectURL,
		TempoURL:    cfg.TempoURL,
		LokiURL:     cfg.LokiURL,
		Timeout:     cfg.ExplorerTimeout,
		MaxLimit:    cfg.ExplorerMaxRows,
	}, tenantMapper)

	public := &http.Server{
		Addr: cfg.Addr,
		Handler: api.NewRouter(api.Deps{
			DB:               database,
			Signal:           signaler,
			Tenants:          tenantMapper,
			Auth:             tenancy.DevTokenAuthenticator(os.Getenv("OBS_DEV_TOKEN"), os.Getenv("OBS_DEV_ORG_ID")),
			Keys:             keys,
			Authz:            authorizer,
			Audit:            audit.NewPG(database),
			RotationGrace:    cfg.RotationGrace,
			ActiveHostWindow: cfg.ActiveHostWindow,
			Live:             hub,
			Explorer:         explore,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	internal, err := internalServer(cfg, api.InternalDeps{Authz: authorizer, Signal: signaler})
	if err != nil {
		return err
	}

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

// internalServer builds the Collector-facing listener, or nil when it is turned
// off entirely.
//
// mTLS is the contract for this hop (CLAUDE.md), so a missing certificate
// bundle does not quietly become a plaintext port: it either disables the
// listener or, when the operator explicitly asked for it with
// OBS_INTERNAL_INSECURE, serves plaintext and says so in the log on every boot.
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
		log.Print("WARNING: internal plane is plaintext (OBS_INTERNAL_INSECURE); " +
			"set OBS_TLS_CA_FILE/CERT/KEY for the mTLS the design requires")
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
