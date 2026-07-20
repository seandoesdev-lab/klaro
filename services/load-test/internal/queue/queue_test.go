//go:build integration

package queue

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/klaro/load-test/internal/model"
)

// testRedisDB isolates the queue test onto a dedicated Redis logical DB so a
// live worker (which consumes klaro:jobs on DB 0) can never steal the job and
// cause a false-negative (QA 운영 이슈: queue 경쟁 소비자).
const testRedisDB = 15

func TestEnqueueDequeue(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set")
	}
	r := &Redis{c: redis.NewClient(&redis.Options{Addr: addr, DB: testRedisDB})}
	ctx := context.Background()
	if err := r.c.FlushDB(ctx).Err(); err != nil { // 격리 DB를 깨끗한 상태로
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.c.FlushDB(context.Background()).Err() })

	want := model.Job{LoadTestID: "lt1", TargetURL: "https://x", Scenario: model.Scenario{VU: 1, DurationSec: 1}}
	if err := r.Enqueue(ctx, want); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	got, err := r.Dequeue(cctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.LoadTestID != "lt1" {
		t.Fatalf("got %q", got.LoadTestID)
	}
}
