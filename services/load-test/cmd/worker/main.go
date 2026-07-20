package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/store"
	"github.com/klaro/load-test/internal/worker"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	appEnv := env("APP_ENV", "dev")
	appDSN := env("DATABASE_URL", "postgres://klaro_app:klaro_app@localhost:5432/klaro?sslmode=disable")
	sysRaw := os.Getenv("SYSTEM_DATABASE_URL")
	if appEnv != "dev" { // F-4 fail-fast: 프로덕션은 별도 klaro_system DSN 필수
		if sysRaw == "" {
			log.Fatal("SYSTEM_DATABASE_URL must be set in non-dev environments (F-4)")
		}
		if sysRaw == appDSN {
			log.Fatal("SYSTEM_DATABASE_URL must differ from DATABASE_URL (F-4)")
		}
	}
	sysDSN := sysRaw
	if sysDSN == "" {
		sysDSN = "postgres://klaro_system:klaro_system@localhost:5432/klaro?sslmode=disable"
	}
	st, err := store.New(ctx, appDSN, sysDSN)
	if err != nil {
		log.Fatal(err)
	}
	rd := queue.NewRedis(env("REDIS_ADDR", "localhost:6379"))
	log.Println("worker started")
	// Security-scan worker runs alongside the load-test worker on its own queue.
	go worker.RunScan(ctx, worker.ScanDeps{
		Queue: rd,
		Store: st,
	})
	worker.Run(ctx, worker.Deps{
		Queue:  rd,
		Signal: rd,
		Store:  st,
		K6Path: env("K6_PATH", "k6"),
	})
}
