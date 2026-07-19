//go:build integration

package queue

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/klaro/load-test/internal/model"
)

func TestEnqueueDequeue(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set")
	}
	r := NewRedis(addr)
	ctx := context.Background()
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
