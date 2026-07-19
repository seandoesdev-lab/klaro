package main

import (
	"context"
	"log"
	"os"

	"github.com/klaro/load-test/internal/api"
	"github.com/klaro/load-test/internal/domainverify"
	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/store"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	ctx := context.Background()
	st, err := store.New(ctx, env("DATABASE_URL", "postgres://klaro:klaro@localhost:5432/klaro?sslmode=disable"))
	if err != nil {
		log.Fatal(err)
	}
	rd := queue.NewRedis(env("REDIS_ADDR", "localhost:6379"))
	r := api.NewRouter(api.Deps{
		Store:     st,
		Queue:     rd,
		ScanQueue: rd,
		Signal:    rd,
		Verifier:  domainverify.New(),
		DevToken:  env("DEV_TOKEN", "dev"),
	})
	addr := env("API_ADDR", ":8080")
	log.Printf("api listening on %s", addr)
	log.Fatal(r.Run(addr))
}
