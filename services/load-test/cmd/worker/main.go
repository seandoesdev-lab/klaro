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

	st, err := store.New(ctx, env("DATABASE_URL", "postgres://klaro:klaro@localhost:5432/klaro?sslmode=disable"))
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
