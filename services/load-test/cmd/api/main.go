package main

import (
	"context"
	"log"
	"os"

	"github.com/klaro/load-test/internal/api"
	"github.com/klaro/load-test/internal/auth"
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

// mustDSNs resolves app/sys DSNs and enforces production invariants (F-4):
// APP_ENV!=dev 이면 SYSTEM_DATABASE_URL 이 명시적으로 설정되고 app DSN 과 달라야 한다
// (동일하면 klaro_app RLS 강제가 무력화될 위험 → fail-fast).
func mustDSNs(appEnv string) (appDSN, sysDSN string) {
	appDSN = env("DATABASE_URL", "postgres://klaro_app:klaro_app@localhost:5432/klaro?sslmode=disable")
	sysRaw := os.Getenv("SYSTEM_DATABASE_URL")
	if appEnv != "dev" {
		if sysRaw == "" {
			log.Fatal("SYSTEM_DATABASE_URL must be set in non-dev environments (F-4 fail-fast)")
		}
		if sysRaw == appDSN {
			log.Fatal("SYSTEM_DATABASE_URL must differ from DATABASE_URL (distinct klaro_system role; F-4)")
		}
	}
	sysDSN = sysRaw
	if sysDSN == "" {
		sysDSN = "postgres://klaro_system:klaro_system@localhost:5432/klaro?sslmode=disable"
	}
	return appDSN, sysDSN
}

func main() {
	ctx := context.Background()
	appEnv := env("APP_ENV", "dev")
	appDSN, sysDSN := mustDSNs(appEnv)
	st, err := store.New(ctx, appDSN, sysDSN)
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
		JWT:       auth.NewJWTManager(env("JWT_SECRET", "dev-insecure-jwt-secret")),
		Refresh:   auth.NewRefreshStore(rd.Client()),
		OAuth:     auth.NewOAuthManager(env("OAUTH_REDIRECT_BASE", "http://localhost:8080")),
		DevToken:  env("DEV_TOKEN", "dev"),
		AppEnv:    appEnv,
	})
	addr := env("API_ADDR", ":8080")
	log.Printf("api listening on %s", addr)
	log.Fatal(r.Run(addr))
}
