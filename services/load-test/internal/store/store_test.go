//go:build integration

package store

import (
	"context"
	"os"
	"testing"

	"github.com/klaro/load-test/internal/model"
)

func testStore(t *testing.T) *Store {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	s, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const devProject = "00000000-0000-0000-0000-000000000002"

func TestLoadTestLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	lt := &model.LoadTest{
		ProjectID: devProject, TargetURL: "https://staging.example.com",
		Scenario: model.Scenario{VU: 5, DurationSec: 5, Steps: []model.Step{{Method: "GET", Path: "/"}}},
		VU:       5, DurationSec: 5, Status: model.StatusValidating,
	}
	if err := s.CreateLoadTest(ctx, lt); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatus(ctx, lt.ID, model.StatusQueued, nil); err != nil {
		t.Fatal(err)
	}
	// illegal jump rejected
	if err := s.UpdateStatus(ctx, lt.ID, model.StatusCompleted, nil); err == nil {
		t.Fatal("expected illegal transition error")
	}
}
