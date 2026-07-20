package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/scanner"
	"github.com/klaro/load-test/internal/store"
	"github.com/klaro/load-test/internal/worker"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
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

	// [EPHEM-01] startup guard: the scan work dir must be tmpfs (RAM). Bypass with
	// SCAN_WORK_TMPFS_ASSERT=0 for local `go run`/tests on a non-tmpfs path.
	workDir := env("SCAN_WORK_DIR", "/scan-work")
	if env("SCAN_WORK_TMPFS_ASSERT", "1") != "0" {
		if err := scanner.AssertTmpfs(workDir); err != nil {
			log.Fatalf("scan work dir is not tmpfs (set SCAN_WORK_TMPFS_ASSERT=0 to bypass in dev): %v", err)
		}
	}

	// Security-scan worker runs alongside the load-test worker on its own queue.
	scanDeps := &worker.ScanDeps{
		Queue: rd,
		Store: st,
		Semgrep: scanner.Semgrep{
			Bin:      env("SEMGREP_BIN", "semgrep"),
			RulesDir: env("SEMGREP_RULES_DIR", "/opt/semgrep-rules"),
		},
		OSV: scanner.OSV{
			Bin:         env("OSV_BIN", "osv-scanner"),
			Offline:     env("OSV_OFFLINE", "1") != "0", // [COST-05]/M-1: no runtime network by default
			LocalDBPath: env("OSV_LOCAL_DB_PATH", "/opt/osv-db"),
		},
		ZAP:            scanner.ZAP{Addr: env("ZAP_ADDR", "")},
		WorkDir:        workDir,
		SrcDir:         env("SCAN_SRC_DIR", "/scan-src"),
		MaxConcurrency: envInt("SCAN_MAX_CONCURRENCY", 2),
	}
	// ZAP is optional: when ZAP_ADDR is unset, DAST stays header-only (no daemon).
	if env("ZAP_ADDR", "") == "" {
		scanDeps.ZAP = nil
	}
	go worker.RunScan(ctx, scanDeps)
	worker.Run(ctx, worker.Deps{
		Queue:  rd,
		Signal: rd,
		Store:  st,
		K6Path: env("K6_PATH", "k6"),
	})
}
