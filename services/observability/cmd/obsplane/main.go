// Command obsplane is the always-on observability control plane.
//
// Build-order steps 1-2 only (design section 6): platform plumbing, migrations,
// tenancy and the backend tenant mapping. Ingest-key handling, the Collector
// authz endpoint, Explorer, alerting and live WebSocket fan-out land in the
// later steps.
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
	"github.com/klaro/observability/internal/platform/config"
	"github.com/klaro/observability/internal/platform/db"
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

	r := api.NewRouter(api.Deps{
		DB:      database,
		Signal:  signaler,
		Tenants: tenants.NewMemoryMapper(),
		Auth:    tenancy.DevTokenAuthenticator(os.Getenv("OBS_DEV_TOKEN"), os.Getenv("OBS_DEV_ORG_ID")),
	})

	srv := &http.Server{Addr: cfg.Addr, Handler: r, ReadHeaderTimeout: 10 * time.Second}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("obsplane listening on %s", cfg.Addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
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
