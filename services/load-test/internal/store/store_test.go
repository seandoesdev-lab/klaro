//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/load-test/internal/model"
)

// testStore opens app+sys pools. TEST_DATABASE_URL MUST point to the klaro_app
// (NOBYPASSRLS) role for RLS to be exercised; TEST_SYSTEM_DATABASE_URL to
// klaro_system (BYPASSRLS). If sys is unset it reuses the app DSN.
func testStore(t *testing.T) *Store {
	appDSN := os.Getenv("TEST_DATABASE_URL")
	if appDSN == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	sysDSN := os.Getenv("TEST_SYSTEM_DATABASE_URL")
	s, err := New(context.Background(), appDSN, sysDSN)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	devOrg     = "00000000-0000-0000-0000-000000000001"
	devProject = "00000000-0000-0000-0000-000000000002"
)

func newLoadTest() *model.LoadTest {
	return &model.LoadTest{
		OrgID: devOrg, ProjectID: devProject, TargetURL: "https://staging.example.com",
		Scenario: model.Scenario{VU: 5, DurationSec: 5, Steps: []model.Step{{Method: "GET", Path: "/"}}},
		VU:       5, DurationSec: 5, Status: model.StatusValidating,
	}
}

func TestLoadTestLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	lt := newLoadTest()

	// org 스코프 tx 안에서 생성 + 상태 전이 (RLS 관통).
	err := s.RunInOrg(ctx, devOrg, func(tx pgx.Tx) error {
		if e := s.CreateLoadTest(ctx, tx, lt); e != nil {
			return e
		}
		return s.UpdateStatus(ctx, tx, lt.ID, model.StatusQueued, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	// 불법 전이 거부
	err = s.RunInOrg(ctx, devOrg, func(tx pgx.Tx) error {
		return s.UpdateStatus(ctx, tx, lt.ID, model.StatusCompleted, nil)
	})
	if err == nil {
		t.Fatal("expected illegal transition error")
	}
}

// TENANT-02: 세션 미설정 상태(app.current_org 없음)에서 org 스코프 SELECT 는 0건.
func TestRLSSessionUnsetReturnsZero(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// app 풀 직접 사용 = current_org 미설정.
	var n int
	if err := s.App().QueryRow(ctx, `SELECT count(*) FROM load_tests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("세션 미설정 SELECT 는 0건이어야 함(RLS), got %d — TEST_DATABASE_URL 이 klaro_app 인지 확인", n)
	}
}

// TENANT-03 / RBAC-04: 크로스 org 조회 차단(IDOR). devOrg 에서 만든 리소스를
// 다른 org 스코프에서는 id 로도 볼 수 없어야 한다.
func TestRLSCrossOrgBlocked(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	lt := newLoadTest()
	if err := s.RunInOrg(ctx, devOrg, func(tx pgx.Tx) error {
		return s.CreateLoadTest(ctx, tx, lt)
	}); err != nil {
		t.Fatal(err)
	}

	// 다른(임의) org 스코프로 조회 → ErrNotFound.
	const otherOrg = "11111111-1111-1111-1111-111111111111"
	err := s.RunInOrg(ctx, otherOrg, func(tx pgx.Tx) error {
		_, e := s.GetLoadTest(ctx, tx, lt.ID)
		return e
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("크로스 org 조회는 ErrNotFound 여야 함, got %v", err)
	}

	// 동일 org 스코프 조회 → 정상.
	err = s.RunInOrg(ctx, devOrg, func(tx pgx.Tx) error {
		got, e := s.GetLoadTest(ctx, tx, lt.ID)
		if e != nil {
			return e
		}
		if got.ID != lt.ID {
			t.Fatalf("in-org 조회 불일치: %s != %s", got.ID, lt.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("in-org 조회 실패: %v", err)
	}
}
