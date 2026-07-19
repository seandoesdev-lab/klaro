# klaro 부하 테스트 서비스 (MVP) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 도메인 검증 → 부하 테스트 생성 → 로컬 k6 워커 실행 → WebSocket 실시간 메트릭 → 결과 요약 저장까지 end-to-end로 동작하는 Go 서비스를 구현한다.

**Architecture:** Control Plane API(Gin)가 REST/WS를 제공하고 Postgres에 메타데이터를, Redis에 잡 큐(List)와 메트릭(Pub/Sub)을 둔다. Load Worker가 잡을 소비해 scenario JSON을 k6 스크립트로 생성·실행하고, k6의 NDJSON 출력을 1초 윈도로 집계해 메트릭을 발행하며 서킷브레이커로 안전 종료한다.

**Tech Stack:** Go 1.22, Gin, gorilla/websocket, jackc/pgx v5, redis/go-redis v9, grafana/k6(바이너리 shell-out), Docker Compose, testify.

## Global Constraints

- Go 모듈 경로: `github.com/klaro/load-test`. 모든 코드는 `services/load-test/` 아래.
- 순수 로직 패키지(`scenario`, `breaker`, `model`)는 외부 I/O 의존 금지 — 표 기반 단위 테스트만으로 검증 가능해야 한다.
- 상태 전이는 `model.CanTransition` 한 곳으로만 강제한다. 핸들러/워커가 상태를 직접 문자열로 바꾸지 않는다.
- 에러 응답은 항상 `{ "error": { "code", "message", "details" } }` 형식. 코드는 API 스펙 표준 집합만 사용.
- 도메인 미검증 대상 부하 잡은 절대 생성 금지 (403 `DOMAIN_NOT_VERIFIED`). 이 게이트는 어떤 경로로도 우회 불가.
- k6에 의존하는 통합 테스트는 `//go:build integration` 태그로 분리하고, k6 미설치 시 `t.Skip`.
- 커밋은 각 태스크 종료 시 1회, Conventional Commits 형식. 커밋 메시지 말미에 `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

---

## File Structure

```
services/load-test/
  go.mod
  cmd/api/main.go              # API 서버 부트스트랩
  cmd/worker/main.go           # 워커 부트스트랩
  internal/model/
    status.go                  # Status enum + 전이 맵
    types.go                   # Scenario/Step/LoadTest/Result/Job/VerifiedDomain
  internal/scenario/
    scenario.go                # Validate + Generate(k6 JS)
  internal/breaker/
    breaker.go                 # 슬라이딩 윈도 서킷브레이커
  internal/domainverify/
    verify.go                  # DNS TXT / 파일 챌린지 검증
  internal/queue/
    queue.go                   # JobQueue 인터페이스
    redis.go                   # Redis 구현
  internal/store/
    store.go                   # pgx 풀 + 리포지토리
  internal/api/
    errors.go                  # 에러 응답 헬퍼
    middleware.go              # 개발용 인증 스텁
    router.go                  # 라우트 등록
    domains.go                 # 도메인 핸들러
    loadtests.go               # 부하 테스트 핸들러
    ws.go                      # WS 허브 + 스트림 핸들러
  internal/worker/
    worker.go                  # 잡 루프
    k6runner.go                # k6 실행 + NDJSON 파싱 + 집계
  migrations/
    0001_init.sql
  testdata/
    target/main.go             # 통합 테스트용 스텁 대상 서버
  docker-compose.yml
  Dockerfile.api
  Dockerfile.worker
```

---

## Task 1: 모듈 스캐폴드 + model 패키지 (상태머신 · 타입)

**Files:**
- Create: `services/load-test/go.mod`
- Create: `services/load-test/internal/model/status.go`
- Create: `services/load-test/internal/model/types.go`
- Test: `services/load-test/internal/model/status_test.go`

**Interfaces:**
- Consumes: (없음)
- Produces:
  - `model.Status` (string) 상수: `StatusPending, StatusValidating, StatusQueued, StatusProvisioning, StatusRunning, StatusAggregating, StatusCompleted, StatusFailed, StatusAborted, StatusRejected`
  - `model.CanTransition(from, to Status) bool`
  - `model.Scenario{ VU int; DurationSec int; RampUpSec int; Steps []Step; Thresholds Thresholds }`
  - `model.Step{ Method, Path string; Body json.RawMessage }`
  - `model.Thresholds{ HTTPReqDurationP95Ms int; ErrorRate float64 }`
  - `model.Job{ LoadTestID, ProjectID, TargetURL string; Scenario Scenario }`
  - `model.LoadTest`, `model.LoadTestResult`, `model.VerifiedDomain` 구조체

- [ ] **Step 1: go.mod 생성**

`services/load-test/go.mod`:
```
module github.com/klaro/load-test

go 1.22
```

- [ ] **Step 2: 실패 테스트 작성** — `internal/model/status_test.go`

```go
package model

import "testing"

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to Status
		ok       bool
	}{
		{StatusPending, StatusValidating, true},
		{StatusValidating, StatusQueued, true},
		{StatusValidating, StatusRejected, true},
		{StatusQueued, StatusProvisioning, true},
		{StatusProvisioning, StatusRunning, true},
		{StatusRunning, StatusAggregating, true},
		{StatusRunning, StatusAborted, true},
		{StatusAggregating, StatusCompleted, true},
		{StatusRunning, StatusFailed, true},
		{StatusCompleted, StatusRunning, false},
		{StatusPending, StatusCompleted, false},
		{StatusAborted, StatusRunning, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.ok {
			t.Errorf("CanTransition(%s,%s)=%v want %v", c.from, c.to, got, c.ok)
		}
	}
}
```

- [ ] **Step 3: 테스트 실패 확인**

Run: `cd services/load-test && go test ./internal/model/`
Expected: FAIL (undefined: Status 등)

- [ ] **Step 4: status.go 구현**

`internal/model/status.go`:
```go
package model

type Status string

const (
	StatusPending      Status = "pending"
	StatusValidating   Status = "validating"
	StatusQueued       Status = "queued"
	StatusProvisioning Status = "provisioning"
	StatusRunning      Status = "running"
	StatusAggregating  Status = "aggregating"
	StatusCompleted    Status = "completed"
	StatusFailed       Status = "failed"
	StatusAborted      Status = "aborted"
	StatusRejected     Status = "rejected"
)

var allowed = map[Status]map[Status]bool{
	StatusPending:      {StatusValidating: true},
	StatusValidating:   {StatusQueued: true, StatusRejected: true},
	StatusQueued:       {StatusProvisioning: true, StatusAborted: true},
	StatusProvisioning: {StatusRunning: true, StatusFailed: true, StatusAborted: true},
	StatusRunning:      {StatusAggregating: true, StatusAborted: true, StatusFailed: true},
	StatusAggregating:  {StatusCompleted: true, StatusFailed: true},
}

// CanTransition reports whether moving from->to is a legal state change.
func CanTransition(from, to Status) bool {
	return allowed[from][to]
}
```

- [ ] **Step 5: types.go 구현**

`internal/model/types.go`:
```go
package model

import (
	"encoding/json"
	"time"
)

type Step struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type Thresholds struct {
	HTTPReqDurationP95Ms int     `json:"http_req_duration_p95_ms,omitempty"`
	ErrorRate            float64 `json:"error_rate,omitempty"`
}

type Scenario struct {
	VU          int        `json:"vu"`
	DurationSec int        `json:"duration_sec"`
	RampUpSec   int        `json:"ramp_up_sec"`
	Steps       []Step     `json:"steps"`
	Thresholds  Thresholds `json:"thresholds"`
}

type VerifiedDomain struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"-"`
	Domain     string     `json:"domain"`
	Method     string     `json:"method"`
	Token      string     `json:"-"`
	Status     string     `json:"status"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
}

type LoadTest struct {
	ID            string     `json:"id"`
	ProjectID     string     `json:"-"`
	TargetURL     string     `json:"target_url"`
	Scenario      Scenario   `json:"scenario"`
	VU            int        `json:"vu"`
	DurationSec   int        `json:"duration_sec"`
	Status        Status     `json:"status"`
	AbortedReason *string    `json:"aborted_reason"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type LoadTestResult struct {
	RPSAvg                 float64 `json:"rps_avg"`
	LatencyP50             float64 `json:"latency_p50"`
	LatencyP95             float64 `json:"latency_p95"`
	LatencyP99             float64 `json:"latency_p99"`
	ErrorRate              float64 `json:"error_rate"`
	MaxVUBeforeDegradation int     `json:"max_vu_before_degradation"`
	BottleneckEndpoint     string  `json:"bottleneck_endpoint"`
	MetricsRef             string  `json:"-"`
}

type Job struct {
	LoadTestID string   `json:"load_test_id"`
	ProjectID  string   `json:"project_id"`
	TargetURL  string   `json:"target_url"`
	Scenario   Scenario `json:"scenario"`
}
```

- [ ] **Step 6: 테스트 통과 확인**

Run: `cd services/load-test && go test ./internal/model/`
Expected: PASS

- [ ] **Step 7: 커밋**

```bash
git add services/load-test/go.mod services/load-test/internal/model/
git commit -m "feat(load-test): model 패키지 - 상태머신과 도메인 타입"
```

---

## Task 2: scenario 패키지 (검증 + k6 스크립트 생성)

**Files:**
- Create: `services/load-test/internal/scenario/scenario.go`
- Test: `services/load-test/internal/scenario/scenario_test.go`

**Interfaces:**
- Consumes: `model.Scenario`, `model.Step`
- Produces:
  - `scenario.Validate(model.Scenario) error` — 위반 시 `scenario.ErrInvalid` 래핑 에러
  - `scenario.Generate(model.Scenario) (string, error)` — k6 JS 소스 반환
  - `scenario.ErrInvalid` (sentinel error)

- [ ] **Step 1: 실패 테스트 작성** — `internal/scenario/scenario_test.go`

```go
package scenario

import (
	"errors"
	"strings"
	"testing"

	"github.com/klaro/load-test/internal/model"
)

func valid() model.Scenario {
	return model.Scenario{
		VU: 10, DurationSec: 30, RampUpSec: 5,
		Steps:      []model.Step{{Method: "GET", Path: "/health"}},
		Thresholds: model.Thresholds{HTTPReqDurationP95Ms: 2000, ErrorRate: 0.05},
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(valid()); err != nil {
		t.Fatalf("valid scenario rejected: %v", err)
	}
	bad := []model.Scenario{
		func() model.Scenario { s := valid(); s.VU = 0; return s }(),
		func() model.Scenario { s := valid(); s.DurationSec = 0; return s }(),
		func() model.Scenario { s := valid(); s.Steps = nil; return s }(),
		func() model.Scenario { s := valid(); s.Steps = []model.Step{{Method: "FLY", Path: "/x"}}; return s }(),
		func() model.Scenario { s := valid(); s.Steps = []model.Step{{Method: "GET", Path: "x"}}; return s }(),
	}
	for i, s := range bad {
		if err := Validate(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: expected ErrInvalid, got %v", i, err)
		}
	}
}

func TestGenerate(t *testing.T) {
	src, err := Generate(valid())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"export const options", "stages:", "http.request", "/health", "thresholds"} {
		if !strings.Contains(src, want) {
			t.Errorf("generated script missing %q", want)
		}
	}
}

func TestGenerateInvalidReturnsError(t *testing.T) {
	s := valid()
	s.VU = 0
	if _, err := Generate(s); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd services/load-test && go test ./internal/scenario/`
Expected: FAIL (undefined: Validate/Generate/ErrInvalid)

- [ ] **Step 3: scenario.go 구현**

`internal/scenario/scenario.go`:
```go
package scenario

import (
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/klaro/load-test/internal/model"
)

var ErrInvalid = errors.New("invalid scenario")

var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true,
}

func Validate(s model.Scenario) error {
	if s.VU <= 0 {
		return fmt.Errorf("%w: vu must be > 0", ErrInvalid)
	}
	if s.DurationSec <= 0 {
		return fmt.Errorf("%w: duration_sec must be > 0", ErrInvalid)
	}
	if s.RampUpSec < 0 {
		return fmt.Errorf("%w: ramp_up_sec must be >= 0", ErrInvalid)
	}
	if len(s.Steps) == 0 {
		return fmt.Errorf("%w: steps must not be empty", ErrInvalid)
	}
	for i, st := range s.Steps {
		if !allowedMethods[strings.ToUpper(st.Method)] {
			return fmt.Errorf("%w: step %d method %q not allowed", ErrInvalid, i, st.Method)
		}
		if !strings.HasPrefix(st.Path, "/") {
			return fmt.Errorf("%w: step %d path must start with /", ErrInvalid, i)
		}
	}
	return nil
}

type tmplData struct {
	RampUpSec  int
	PlateauSec int
	VU         int
	P95Ms      int
	ErrorRate  float64
	Steps      []model.Step
}

const k6Template = `import http from 'k6/http';
import { sleep } from 'k6';

export const options = {
  stages: [
    { duration: '{{.RampUpSec}}s', target: {{.VU}} },
    { duration: '{{.PlateauSec}}s', target: {{.VU}} },
  ],
  thresholds: {
    http_req_duration: ['p(95)<{{.P95Ms}}'],
    http_req_failed: ['rate<{{.ErrorRate}}'],
  },
};

export default function () {
{{- range .Steps}}
  http.request('{{.Method}}', __ENV.TARGET + '{{.Path}}', {{if .Body}}JSON.stringify({{printf "%s" .Body}}){{else}}null{{end}}, { tags: { step: '{{.Path}}' } });
{{- end}}
  sleep(1);
}
`

// Generate returns a k6 JS script for the scenario. The target base URL is
// passed to k6 at runtime via the TARGET environment variable.
func Generate(s model.Scenario) (string, error) {
	if err := Validate(s); err != nil {
		return "", err
	}
	p95 := s.Thresholds.HTTPReqDurationP95Ms
	if p95 <= 0 {
		p95 = 2000
	}
	rate := s.Thresholds.ErrorRate
	if rate <= 0 {
		rate = 0.05
	}
	plateau := s.DurationSec - s.RampUpSec
	if plateau < 0 {
		plateau = 0
	}
	steps := make([]model.Step, len(s.Steps))
	for i, st := range s.Steps {
		st.Method = strings.ToUpper(st.Method)
		steps[i] = st
	}
	data := tmplData{
		RampUpSec: s.RampUpSec, PlateauSec: plateau, VU: s.VU,
		P95Ms: p95, ErrorRate: rate, Steps: steps,
	}
	t, err := template.New("k6").Parse(k6Template)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd services/load-test && go test ./internal/scenario/`
Expected: PASS

- [ ] **Step 5: 커밋**

```bash
git add services/load-test/internal/scenario/
git commit -m "feat(load-test): scenario 검증 및 k6 스크립트 생성"
```

---

## Task 3: breaker 패키지 (서킷브레이커)

**Files:**
- Create: `services/load-test/internal/breaker/breaker.go`
- Test: `services/load-test/internal/breaker/breaker_test.go`

**Interfaces:**
- Consumes: (없음)
- Produces:
  - `breaker.New(window time.Duration, threshold float64, minSamples int) *Breaker`
  - `(*Breaker).Observe(ts time.Time, ok bool)` — ok=false는 에러 요청
  - `(*Breaker).ShouldAbort(now time.Time) (bool, string)`

- [ ] **Step 1: 실패 테스트 작성** — `internal/breaker/breaker_test.go`

```go
package breaker

import (
	"testing"
	"time"
)

func TestBreakerTripsOnHighErrorRate(t *testing.T) {
	b := New(10*time.Second, 0.8, 10)
	base := time.Unix(0, 0)
	for i := 0; i < 20; i++ {
		b.Observe(base.Add(time.Duration(i)*100*time.Millisecond), false) // all errors
	}
	trip, reason := b.ShouldAbort(base.Add(2 * time.Second))
	if !trip {
		t.Fatal("expected breaker to trip")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestBreakerHoldsUnderThreshold(t *testing.T) {
	b := New(10*time.Second, 0.8, 10)
	base := time.Unix(0, 0)
	for i := 0; i < 20; i++ {
		b.Observe(base.Add(time.Duration(i)*100*time.Millisecond), i%2 == 0) // 50% error
	}
	if trip, _ := b.ShouldAbort(base.Add(2 * time.Second)); trip {
		t.Fatal("did not expect trip at 50% error")
	}
}

func TestBreakerWaitsForMinSamples(t *testing.T) {
	b := New(10*time.Second, 0.8, 10)
	base := time.Unix(0, 0)
	for i := 0; i < 3; i++ {
		b.Observe(base.Add(time.Duration(i)*100*time.Millisecond), false)
	}
	if trip, _ := b.ShouldAbort(base.Add(time.Second)); trip {
		t.Fatal("should not trip below minSamples")
	}
}

func TestBreakerEvictsOldSamples(t *testing.T) {
	b := New(10*time.Second, 0.8, 10)
	base := time.Unix(0, 0)
	for i := 0; i < 20; i++ {
		b.Observe(base.Add(time.Duration(i)*100*time.Millisecond), false)
	}
	// 30s later the old error samples fall out of the window
	if trip, _ := b.ShouldAbort(base.Add(30 * time.Second)); trip {
		t.Fatal("old samples should have been evicted")
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd services/load-test && go test ./internal/breaker/`
Expected: FAIL (undefined: New)

- [ ] **Step 3: breaker.go 구현**

`internal/breaker/breaker.go`:
```go
package breaker

import (
	"fmt"
	"time"
)

type sample struct {
	ts time.Time
	ok bool
}

// Breaker computes a sliding-window error rate and trips when the rate exceeds
// a threshold once enough samples are present.
type Breaker struct {
	window     time.Duration
	threshold  float64
	minSamples int
	samples    []sample
}

func New(window time.Duration, threshold float64, minSamples int) *Breaker {
	return &Breaker{window: window, threshold: threshold, minSamples: minSamples}
}

func (b *Breaker) Observe(ts time.Time, ok bool) {
	b.samples = append(b.samples, sample{ts: ts, ok: ok})
}

func (b *Breaker) evict(now time.Time) {
	cutoff := now.Add(-b.window)
	i := 0
	for i < len(b.samples) && b.samples[i].ts.Before(cutoff) {
		i++
	}
	if i > 0 {
		b.samples = b.samples[i:]
	}
}

// ShouldAbort returns true with a reason when the windowed error rate exceeds
// the threshold and at least minSamples are present.
func (b *Breaker) ShouldAbort(now time.Time) (bool, string) {
	b.evict(now)
	if len(b.samples) < b.minSamples {
		return false, ""
	}
	errCount := 0
	for _, s := range b.samples {
		if !s.ok {
			errCount++
		}
	}
	rate := float64(errCount) / float64(len(b.samples))
	if rate > b.threshold {
		return true, fmt.Sprintf("error_rate > %.2f (observed %.2f)", b.threshold, rate)
	}
	return false, ""
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd services/load-test && go test ./internal/breaker/`
Expected: PASS

- [ ] **Step 5: 커밋**

```bash
git add services/load-test/internal/breaker/
git commit -m "feat(load-test): 슬라이딩 윈도 서킷브레이커"
```

---

## Task 4: domainverify 패키지 (소유권 검증)

**Files:**
- Create: `services/load-test/internal/domainverify/verify.go`
- Test: `services/load-test/internal/domainverify/verify_test.go`

**Interfaces:**
- Consumes: (없음)
- Produces:
  - `domainverify.Verifier{ LookupTXT func(string)([]string,error); HTTPClient *http.Client; scheme string }`
  - `(*Verifier).Verify(ctx, domain, method, token) (bool, error)` — method: `dns_txt` | `file`
  - `domainverify.New() *Verifier` (기본: net.LookupTXT + http.Client, scheme="https")

- [ ] **Step 1: 실패 테스트 작성** — `internal/domainverify/verify_test.go`

```go
package domainverify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyDNSTXTMatch(t *testing.T) {
	v := &Verifier{LookupTXT: func(string) ([]string, error) {
		return []string{"unrelated", "klaro-verify=tok123"}, nil
	}}
	ok, err := v.Verify(context.Background(), "example.com", "dns_txt", "tok123")
	if err != nil || !ok {
		t.Fatalf("expected match, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyDNSTXTNoMatch(t *testing.T) {
	v := &Verifier{LookupTXT: func(string) ([]string, error) {
		return []string{"klaro-verify=other"}, nil
	}}
	ok, _ := v.Verify(context.Background(), "example.com", "dns_txt", "tok123")
	if ok {
		t.Fatal("expected no match")
	}
}

func TestVerifyFileMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/klaro-challenge.txt" {
			w.Write([]byte("tok123\n"))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	v := &Verifier{HTTPClient: srv.Client(), scheme: "http"}
	ok, err := v.Verify(context.Background(), host, "file", "tok123")
	if err != nil || !ok {
		t.Fatalf("expected file match, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyUnknownMethod(t *testing.T) {
	v := New()
	if _, err := v.Verify(context.Background(), "example.com", "carrier_pigeon", "x"); err == nil {
		t.Fatal("expected error for unknown method")
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd services/load-test && go test ./internal/domainverify/`
Expected: FAIL (undefined: Verifier/New)

- [ ] **Step 3: verify.go 구현**

`internal/domainverify/verify.go`:
```go
package domainverify

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const txtPrefix = "klaro-verify="
const challengePath = "/klaro-challenge.txt"

type Verifier struct {
	LookupTXT  func(string) ([]string, error)
	HTTPClient *http.Client
	scheme     string // "https" by default; overridable in tests
}

func New() *Verifier {
	return &Verifier{
		LookupTXT:  net.LookupTXT,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		scheme:     "https",
	}
}

func (v *Verifier) Verify(ctx context.Context, domain, method, token string) (bool, error) {
	switch method {
	case "dns_txt":
		return v.verifyDNS(domain, token)
	case "file":
		return v.verifyFile(ctx, domain, token)
	default:
		return false, fmt.Errorf("unknown verification method %q", method)
	}
}

func (v *Verifier) verifyDNS(domain, token string) (bool, error) {
	records, err := v.LookupTXT(domain)
	if err != nil {
		return false, err
	}
	want := txtPrefix + token
	for _, r := range records {
		if strings.TrimSpace(r) == want {
			return true, nil
		}
	}
	return false, nil
}

func (v *Verifier) verifyFile(ctx context.Context, domain, token string) (bool, error) {
	scheme := v.scheme
	if scheme == "" {
		scheme = "https"
	}
	url := scheme + "://" + domain + challengePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	client := v.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(body)) == token, nil
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd services/load-test && go test ./internal/domainverify/`
Expected: PASS

- [ ] **Step 5: 커밋**

```bash
git add services/load-test/internal/domainverify/
git commit -m "feat(load-test): 도메인 소유권 검증 (DNS TXT / 파일 챌린지)"
```

---

## Task 5: DB 마이그레이션 + store 패키지

**Files:**
- Create: `services/load-test/migrations/0001_init.sql`
- Create: `services/load-test/internal/store/store.go`
- Create: `services/load-test/internal/store/host_test.go`
- Create: `services/load-test/internal/store/store_test.go` (`//go:build integration`)

**Interfaces:**
- Consumes: `model.*`
- Produces:
  - `store.New(ctx, dsn string) (*Store, error)`, `(*Store).Close()`
  - `(*Store).CreateLoadTest(ctx, lt *model.LoadTest) error`
  - `(*Store).GetLoadTest(ctx, id string) (*model.LoadTest, error)`
  - `(*Store).ListLoadTests(ctx, projectID string) ([]model.LoadTest, error)`
  - `(*Store).UpdateStatus(ctx, id string, to model.Status, abortedReason *string) error` — `model.CanTransition` 위반 시 `store.ErrIllegalTransition`
  - `(*Store).MarkStarted(ctx, id string) error`, `(*Store).MarkFinished(ctx, id string) error`
  - `(*Store).SaveResult(ctx, loadTestID string, r model.LoadTestResult) error`
  - `(*Store).GetResult(ctx, loadTestID string) (*model.LoadTestResult, error)`
  - `(*Store).CreateDomain(ctx, d *model.VerifiedDomain) error`
  - `(*Store).GetDomain(ctx, id string) (*model.VerifiedDomain, error)`
  - `(*Store).ListDomains(ctx, projectID string) ([]model.VerifiedDomain, error)`
  - `(*Store).MarkDomainVerified(ctx, id string) error`
  - `(*Store).IsDomainVerified(ctx, projectID, host string) (bool, error)`
  - `store.HostFromURL(raw string) (string, error)`, `store.ErrNotFound`, `store.ErrIllegalTransition`

- [ ] **Step 1: 마이그레이션 SQL 작성**

`migrations/0001_init.sql`:
```sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE organizations (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE projects (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE verified_domains (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid NOT NULL REFERENCES projects(id),
  domain      text NOT NULL,
  method      text NOT NULL CHECK (method IN ('dns_txt','file')),
  token       text NOT NULL,
  status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','verified','failed')),
  verified_at timestamptz,
  UNIQUE (project_id, domain)
);

CREATE TABLE load_tests (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id     uuid NOT NULL REFERENCES projects(id),
  target_url     text NOT NULL,
  scenario       jsonb NOT NULL,
  vu             int NOT NULL,
  duration_sec   int NOT NULL,
  status         text NOT NULL,
  aborted_reason text,
  started_at     timestamptz,
  finished_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_load_tests_project ON load_tests(project_id, status, created_at);

CREATE TABLE load_test_results (
  id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  load_test_id               uuid NOT NULL REFERENCES load_tests(id),
  rps_avg                    numeric NOT NULL DEFAULT 0,
  latency_p50                numeric NOT NULL DEFAULT 0,
  latency_p95                numeric NOT NULL DEFAULT 0,
  latency_p99                numeric NOT NULL DEFAULT 0,
  error_rate                 numeric NOT NULL DEFAULT 0,
  max_vu_before_degradation  int NOT NULL DEFAULT 0,
  bottleneck_endpoint        text,
  metrics_ref                text,
  created_at                 timestamptz NOT NULL DEFAULT now(),
  UNIQUE (load_test_id)
);

-- 개발 스텁이 참조하는 고정 org/project 시드
INSERT INTO organizations (id, name)
  VALUES ('00000000-0000-0000-0000-000000000001', 'dev-org');
INSERT INTO projects (id, org_id, name)
  VALUES ('00000000-0000-0000-0000-000000000002',
          '00000000-0000-0000-0000-000000000001', 'dev-project');
```

- [ ] **Step 2: go.mod 의존성 추가**

Run:
```bash
cd services/load-test && go get github.com/jackc/pgx/v5@latest && go get github.com/jackc/pgx/v5/pgxpool@latest
```
Expected: go.mod/go.sum 업데이트

- [ ] **Step 3: store.go 구현**

`internal/store/store.go`:
```go
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/klaro/load-test/internal/model"
)

var ErrIllegalTransition = errors.New("illegal status transition")
var ErrNotFound = errors.New("not found")

type Store struct{ pool *pgxpool.Pool }

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) CreateLoadTest(ctx context.Context, lt *model.LoadTest) error {
	scn, err := json.Marshal(lt.Scenario)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO load_tests (project_id, target_url, scenario, vu, duration_sec, status)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, created_at`,
		lt.ProjectID, lt.TargetURL, scn, lt.VU, lt.DurationSec, lt.Status,
	).Scan(&lt.ID, &lt.CreatedAt)
}

func (s *Store) GetLoadTest(ctx context.Context, id string) (*model.LoadTest, error) {
	var lt model.LoadTest
	var scn []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, target_url, scenario, vu, duration_sec, status,
		        aborted_reason, started_at, finished_at, created_at
		 FROM load_tests WHERE id=$1`, id,
	).Scan(&lt.ID, &lt.ProjectID, &lt.TargetURL, &scn, &lt.VU, &lt.DurationSec,
		&lt.Status, &lt.AbortedReason, &lt.StartedAt, &lt.FinishedAt, &lt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(scn, &lt.Scenario); err != nil {
		return nil, err
	}
	return &lt, nil
}

func (s *Store) ListLoadTests(ctx context.Context, projectID string) ([]model.LoadTest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, target_url, vu, duration_sec, status, created_at
		 FROM load_tests WHERE project_id=$1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.LoadTest
	for rows.Next() {
		var lt model.LoadTest
		if err := rows.Scan(&lt.ID, &lt.TargetURL, &lt.VU, &lt.DurationSec, &lt.Status, &lt.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, lt)
	}
	return out, rows.Err()
}

// UpdateStatus enforces the state machine before writing.
func (s *Store) UpdateStatus(ctx context.Context, id string, to model.Status, abortedReason *string) error {
	var cur model.Status
	if err := s.pool.QueryRow(ctx, `SELECT status FROM load_tests WHERE id=$1`, id).Scan(&cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !model.CanTransition(cur, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, cur, to)
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE load_tests SET status=$2, aborted_reason=COALESCE($3, aborted_reason) WHERE id=$1`,
		id, to, abortedReason)
	return err
}

func (s *Store) MarkStarted(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE load_tests SET started_at=now() WHERE id=$1 AND started_at IS NULL`, id)
	return err
}

func (s *Store) MarkFinished(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE load_tests SET finished_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) SaveResult(ctx context.Context, loadTestID string, r model.LoadTestResult) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO load_test_results
		   (load_test_id, rps_avg, latency_p50, latency_p95, latency_p99,
		    error_rate, max_vu_before_degradation, bottleneck_endpoint, metrics_ref)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (load_test_id) DO UPDATE SET
		   rps_avg=EXCLUDED.rps_avg, latency_p50=EXCLUDED.latency_p50,
		   latency_p95=EXCLUDED.latency_p95, latency_p99=EXCLUDED.latency_p99,
		   error_rate=EXCLUDED.error_rate,
		   max_vu_before_degradation=EXCLUDED.max_vu_before_degradation,
		   bottleneck_endpoint=EXCLUDED.bottleneck_endpoint`,
		loadTestID, r.RPSAvg, r.LatencyP50, r.LatencyP95, r.LatencyP99,
		r.ErrorRate, r.MaxVUBeforeDegradation, nullStr(r.BottleneckEndpoint), nullStr(r.MetricsRef))
	return err
}

func (s *Store) GetResult(ctx context.Context, loadTestID string) (*model.LoadTestResult, error) {
	var r model.LoadTestResult
	var bottleneck, ref *string
	err := s.pool.QueryRow(ctx,
		`SELECT rps_avg, latency_p50, latency_p95, latency_p99, error_rate,
		        max_vu_before_degradation, bottleneck_endpoint, metrics_ref
		 FROM load_test_results WHERE load_test_id=$1`, loadTestID,
	).Scan(&r.RPSAvg, &r.LatencyP50, &r.LatencyP95, &r.LatencyP99, &r.ErrorRate,
		&r.MaxVUBeforeDegradation, &bottleneck, &ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if bottleneck != nil {
		r.BottleneckEndpoint = *bottleneck
	}
	if ref != nil {
		r.MetricsRef = *ref
	}
	return &r, nil
}

func (s *Store) CreateDomain(ctx context.Context, d *model.VerifiedDomain) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO verified_domains (project_id, domain, method, token, status)
		 VALUES ($1,$2,$3,$4,'pending') RETURNING id, status`,
		d.ProjectID, d.Domain, d.Method, d.Token).Scan(&d.ID, &d.Status)
}

func (s *Store) GetDomain(ctx context.Context, id string) (*model.VerifiedDomain, error) {
	var d model.VerifiedDomain
	err := s.pool.QueryRow(ctx,
		`SELECT id, project_id, domain, method, token, status, verified_at
		 FROM verified_domains WHERE id=$1`, id,
	).Scan(&d.ID, &d.ProjectID, &d.Domain, &d.Method, &d.Token, &d.Status, &d.VerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

func (s *Store) ListDomains(ctx context.Context, projectID string) ([]model.VerifiedDomain, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, domain, method, status, verified_at FROM verified_domains WHERE project_id=$1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.VerifiedDomain
	for rows.Next() {
		var d model.VerifiedDomain
		if err := rows.Scan(&d.ID, &d.Domain, &d.Method, &d.Status, &d.VerifiedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) MarkDomainVerified(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE verified_domains SET status='verified', verified_at=now() WHERE id=$1`, id)
	return err
}

// IsDomainVerified checks whether host (as parsed from a target URL) is a
// verified domain for the project.
func (s *Store) IsDomainVerified(ctx context.Context, projectID, host string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM verified_domains
		 WHERE project_id=$1 AND domain=$2 AND status='verified'`, projectID, host).Scan(&n)
	return n > 0, err
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// HostFromURL extracts the hostname (no port) from a target URL.
func HostFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host in url %q", raw)
	}
	return u.Hostname(), nil
}
```

- [ ] **Step 4: 단위 테스트(HostFromURL) 작성** — `internal/store/host_test.go`

```go
package store

import "testing"

func TestHostFromURL(t *testing.T) {
	h, err := HostFromURL("https://staging.example.com:8443/x")
	if err != nil || h != "staging.example.com" {
		t.Fatalf("got %q err %v", h, err)
	}
	if _, err := HostFromURL("::::"); err == nil {
		t.Fatal("expected error on malformed url")
	}
}
```

- [ ] **Step 5: 통합 테스트 작성** — `internal/store/store_test.go`

```go
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
```

- [ ] **Step 6: 빌드 + 단위 테스트 확인**

Run: `cd services/load-test && go test ./internal/store/`
Expected: PASS (통합 테스트는 태그로 제외됨)

- [ ] **Step 7: 커밋**

```bash
git add services/load-test/migrations/ services/load-test/internal/store/ services/load-test/go.mod services/load-test/go.sum
git commit -m "feat(load-test): Postgres 마이그레이션 및 store 리포지토리"
```

---

## Task 6: queue 패키지 (JobQueue 인터페이스 + Redis 구현)

**Files:**
- Create: `services/load-test/internal/queue/queue.go`
- Create: `services/load-test/internal/queue/redis.go`
- Create: `services/load-test/internal/queue/queue_test.go` (`//go:build integration`)

**Interfaces:**
- Consumes: `model.Job`
- Produces:
  - `queue.JobQueue` 인터페이스: `Enqueue(ctx, model.Job) error`, `Dequeue(ctx) (model.Job, error)`
  - `queue.Signaler` 인터페이스: `PublishAbort(ctx, loadTestID) error`, `SubscribeAbort(ctx, loadTestID) (<-chan struct{}, func())`, `PublishMetric(ctx, loadTestID string, payload []byte) error`, `SubscribeMetrics(ctx, loadTestID string) (<-chan []byte, func())`
  - `queue.NewRedis(addr string) *Redis` — 두 인터페이스 모두 구현

- [ ] **Step 1: go.mod 의존성 추가**

Run: `cd services/load-test && go get github.com/redis/go-redis/v9@latest`
Expected: go.mod 업데이트

- [ ] **Step 2: queue.go (인터페이스) 작성**

`internal/queue/queue.go`:
```go
package queue

import (
	"context"

	"github.com/klaro/load-test/internal/model"
)

type JobQueue interface {
	Enqueue(ctx context.Context, j model.Job) error
	Dequeue(ctx context.Context) (model.Job, error)
}

type Signaler interface {
	PublishAbort(ctx context.Context, loadTestID string) error
	SubscribeAbort(ctx context.Context, loadTestID string) (<-chan struct{}, func())
	PublishMetric(ctx context.Context, loadTestID string, payload []byte) error
	SubscribeMetrics(ctx context.Context, loadTestID string) (<-chan []byte, func())
}
```

- [ ] **Step 3: redis.go 구현**

`internal/queue/redis.go`:
```go
package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/klaro/load-test/internal/model"
)

const jobListKey = "klaro:jobs"

type Redis struct{ c *redis.Client }

func NewRedis(addr string) *Redis {
	return &Redis{c: redis.NewClient(&redis.Options{Addr: addr})}
}

func (r *Redis) Enqueue(ctx context.Context, j model.Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return r.c.LPush(ctx, jobListKey, b).Err()
}

func (r *Redis) Dequeue(ctx context.Context) (model.Job, error) {
	// BRPop blocks until a job is available or the 5s timeout elapses.
	res, err := r.c.BRPop(ctx, 5*time.Second, jobListKey).Result()
	if err != nil {
		return model.Job{}, err // includes redis.Nil on timeout
	}
	var j model.Job
	err = json.Unmarshal([]byte(res[1]), &j)
	return j, err
}

func abortChan(id string) string   { return "klaro:abort:" + id }
func metricsChan(id string) string { return "klaro:metrics:" + id }

func (r *Redis) PublishAbort(ctx context.Context, id string) error {
	return r.c.Publish(ctx, abortChan(id), "1").Err()
}

func (r *Redis) SubscribeAbort(ctx context.Context, id string) (<-chan struct{}, func()) {
	sub := r.c.Subscribe(ctx, abortChan(id))
	out := make(chan struct{}, 1)
	go func() {
		for range sub.Channel() {
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out, func() { _ = sub.Close() }
}

func (r *Redis) PublishMetric(ctx context.Context, id string, payload []byte) error {
	return r.c.Publish(ctx, metricsChan(id), payload).Err()
}

func (r *Redis) SubscribeMetrics(ctx context.Context, id string) (<-chan []byte, func()) {
	sub := r.c.Subscribe(ctx, metricsChan(id))
	out := make(chan []byte, 32)
	go func() {
		for msg := range sub.Channel() {
			select {
			case out <- []byte(msg.Payload):
			default: // drop-oldest backpressure: skip if consumer is slow
			}
		}
		close(out)
	}()
	return out, func() { _ = sub.Close() }
}

var _ JobQueue = (*Redis)(nil)
var _ Signaler = (*Redis)(nil)
```

- [ ] **Step 4: 통합 테스트 작성** — `internal/queue/queue_test.go`

```go
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
```

- [ ] **Step 5: 빌드/컴파일 확인**

Run: `cd services/load-test && go build ./internal/queue/`
Expected: 성공(에러 없음)

- [ ] **Step 6: 커밋**

```bash
git add services/load-test/internal/queue/ services/load-test/go.mod services/load-test/go.sum
git commit -m "feat(load-test): JobQueue 인터페이스와 Redis 큐/시그널 구현"
```

---

## Task 7: API — 에러 헬퍼 · 인증 스텁 · 도메인/부하 핸들러 · 라우터

**Files:**
- Create: `services/load-test/internal/api/errors.go`
- Create: `services/load-test/internal/api/middleware.go`
- Create: `services/load-test/internal/api/domains.go`
- Create: `services/load-test/internal/api/loadtests.go`
- Create: `services/load-test/internal/api/router.go`
- Create: `services/load-test/internal/api/ws.go` (임시 스텁, Task 8에서 교체)
- Test: `services/load-test/internal/api/errors_test.go`

**Interfaces:**
- Consumes: `store.Store`, `queue.JobQueue`, `queue.Signaler`, `domainverify.Verifier`, `scenario.*`, `model.*`
- Produces:
  - `api.Deps{ Store *store.Store; Queue queue.JobQueue; Signal queue.Signaler; Verifier *domainverify.Verifier; DevToken string }`
  - `api.NewRouter(Deps) *gin.Engine`
  - `writeError(c, httpStatus int, code, msg string, details any)` (패키지 내부)
  - 개발용 인증: `Authorization: Bearer <DevToken>` → 고정 project 컨텍스트

- [ ] **Step 1: go.mod 의존성 추가**

Run: `cd services/load-test && go get github.com/gin-gonic/gin@latest`
Expected: go.mod 업데이트

- [ ] **Step 2: 실패 테스트 작성** — `internal/api/errors_test.go`

```go
package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWriteError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeError(c, 403, "DOMAIN_NOT_VERIFIED", "nope", nil)
	if w.Code != 403 {
		t.Fatalf("code=%d", w.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error.Code != "DOMAIN_NOT_VERIFIED" {
		t.Fatalf("code=%q", body.Error.Code)
	}
}
```

- [ ] **Step 3: 테스트 실패 확인**

Run: `cd services/load-test && go test ./internal/api/`
Expected: FAIL (undefined: writeError)

- [ ] **Step 4: errors.go 구현**

`internal/api/errors.go`:
```go
package api

import "github.com/gin-gonic/gin"

type errBody struct {
	Error errPayload `json:"error"`
}
type errPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func writeError(c *gin.Context, status int, code, msg string, details any) {
	c.AbortWithStatusJSON(status, errBody{Error: errPayload{Code: code, Message: msg, Details: details}})
}
```

- [ ] **Step 5: middleware.go 구현**

`internal/api/middleware.go`:
```go
package api

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// devProjectID is the fixed project seeded by migration 0001.
const devProjectID = "00000000-0000-0000-0000-000000000002"

// authStub accepts a single dev bearer token and injects the fixed project.
// Replace with JWT/OAuth/RBAC in production (swap point).
func authStub(devToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		tok := strings.TrimPrefix(h, "Bearer ")
		if tok == "" || tok != devToken {
			writeError(c, 401, "UNAUTHENTICATED", "missing or invalid token", nil)
			return
		}
		c.Set("project_id", devProjectID)
		c.Next()
	}
}

func projectID(c *gin.Context) string {
	v, _ := c.Get("project_id")
	s, _ := v.(string)
	return s
}
```

- [ ] **Step 6: domains.go 구현**

`internal/api/domains.go`:
```go
package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/store"
)

func randToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (d Deps) createDomain(c *gin.Context) {
	var req struct {
		Domain string `json:"domain"`
		Method string `json:"method"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Domain == "" {
		writeError(c, 400, "VALIDATION_ERROR", "domain required", nil)
		return
	}
	if req.Method != "dns_txt" && req.Method != "file" {
		writeError(c, 400, "VALIDATION_ERROR", "method must be dns_txt or file", nil)
		return
	}
	dom := &model.VerifiedDomain{
		ProjectID: projectID(c), Domain: req.Domain, Method: req.Method, Token: randToken(),
	}
	if err := d.Store.CreateDomain(c, dom); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id": dom.ID, "domain": dom.Domain, "method": dom.Method, "status": dom.Status,
		"verification": gin.H{
			"record_name":  "_klaro." + dom.Domain,
			"record_value": "klaro-verify=" + dom.Token,
			"file_path":    "/klaro-challenge.txt",
			"file_content": dom.Token,
		},
	})
}

func (d Deps) listDomains(c *gin.Context) {
	items, err := d.Store.ListDomains(c, projectID(c))
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) verifyDomain(c *gin.Context) {
	dom, err := d.Store.GetDomain(c, c.Param("domainId"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "domain not found", nil)
		return
	}
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	ok, verr := d.Verifier.Verify(c, dom.Domain, dom.Method, dom.Token)
	if verr != nil {
		writeError(c, 422, "VALIDATION_ERROR", verr.Error(), nil)
		return
	}
	if !ok {
		writeError(c, 422, "VALIDATION_ERROR", "verification token not found", nil)
		return
	}
	if err := d.Store.MarkDomainVerified(c, dom.ID); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": dom.ID, "status": "verified"})
}
```

- [ ] **Step 7: loadtests.go 구현**

`internal/api/loadtests.go`:
```go
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/scenario"
	"github.com/klaro/load-test/internal/store"
)

func (d Deps) createLoadTest(c *gin.Context) {
	var req struct {
		TargetURL string         `json:"target_url"`
		Scenario  model.Scenario `json:"scenario"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid body", nil)
		return
	}
	if err := scenario.Validate(req.Scenario); err != nil {
		writeError(c, 400, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	host, err := store.HostFromURL(req.TargetURL)
	if err != nil {
		writeError(c, 400, "VALIDATION_ERROR", "invalid target_url", nil)
		return
	}
	pid := projectID(c)
	verified, err := d.Store.IsDomainVerified(c, pid, host)
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	if !verified {
		writeError(c, 403, "DOMAIN_NOT_VERIFIED", "target domain must be verified", nil)
		return
	}
	lt := &model.LoadTest{
		ProjectID: pid, TargetURL: req.TargetURL, Scenario: req.Scenario,
		VU: req.Scenario.VU, DurationSec: req.Scenario.DurationSec, Status: model.StatusValidating,
	}
	if err := d.Store.CreateLoadTest(c, lt); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	if err := d.Store.UpdateStatus(c, lt.ID, model.StatusQueued, nil); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	job := model.Job{LoadTestID: lt.ID, ProjectID: pid, TargetURL: lt.TargetURL, Scenario: lt.Scenario}
	if err := d.Queue.Enqueue(c, job); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": lt.ID, "status": model.StatusValidating})
}

func (d Deps) getLoadTest(c *gin.Context) {
	lt, err := d.Store.GetLoadTest(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "load test not found", nil)
		return
	}
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, lt)
}

func (d Deps) listLoadTests(c *gin.Context) {
	items, err := d.Store.ListLoadTests(c, projectID(c))
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (d Deps) abortLoadTest(c *gin.Context) {
	id := c.Param("id")
	if _, err := d.Store.GetLoadTest(c, id); errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "load test not found", nil)
		return
	}
	if err := d.Signal.PublishAbort(c, id); err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": id, "status": "aborting"})
}

func (d Deps) getResults(c *gin.Context) {
	r, err := d.Store.GetResult(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c, 404, "NOT_FOUND", "results not ready", nil)
		return
	}
	if err != nil {
		writeError(c, 500, "INTERNAL", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, r)
}
```

- [ ] **Step 8: router.go 구현**

`internal/api/router.go`:
```go
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/klaro/load-test/internal/domainverify"
	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/store"
)

type Deps struct {
	Store    *store.Store
	Queue    queue.JobQueue
	Signal   queue.Signaler
	Verifier *domainverify.Verifier
	DevToken string
}

func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	// WS는 인증 그룹 밖에 등록(Task 8에서 실제 구현으로 교체)
	registerWS(r, d)

	auth := r.Group("/", authStub(d.DevToken))
	{
		auth.POST("/projects/:id/domains", d.createDomain)
		auth.GET("/projects/:id/domains", d.listDomains)
		auth.POST("/projects/:id/domains/:domainId/verify", d.verifyDomain)
		auth.POST("/projects/:id/load-tests", d.createLoadTest)
		auth.GET("/projects/:id/load-tests", d.listLoadTests)
		auth.GET("/load-tests/:id", d.getLoadTest)
		auth.POST("/load-tests/:id/abort", d.abortLoadTest)
		auth.GET("/load-tests/:id/results", d.getResults)
	}
	return r
}
```

- [ ] **Step 9: WS 임시 스텁 추가(컴파일용)** — `internal/api/ws.go`

```go
package api

import "github.com/gin-gonic/gin"

// registerWS is replaced with a real implementation in Task 8.
func registerWS(r *gin.Engine, d Deps) {}
```

- [ ] **Step 10: 테스트/빌드 확인**

Run: `cd services/load-test && go test ./internal/api/ && go build ./...`
Expected: PASS + 빌드 성공

- [ ] **Step 11: 커밋**

```bash
git add services/load-test/internal/api/ services/load-test/go.mod services/load-test/go.sum
git commit -m "feat(load-test): API 라우터, 인증 스텁, 도메인/부하 핸들러"
```

---

## Task 8: WebSocket 허브 + 실시간 스트림 엔드포인트

**Files:**
- Modify: `services/load-test/internal/api/ws.go` (Step 9 스텁 교체)
- Test: `services/load-test/internal/api/ws_test.go`

**Interfaces:**
- Consumes: `queue.Signaler.SubscribeMetrics`
- Produces:
  - `registerWS(r *gin.Engine, d Deps)` — `GET /load-tests/:id/stream` 등록
  - WS 연결마다 `Signal.SubscribeMetrics(ctx, id)`를 구독해 payload를 그대로 클라이언트에 전송

- [ ] **Step 1: go.mod 의존성 추가**

Run: `cd services/load-test && go get github.com/gorilla/websocket@latest`
Expected: go.mod 업데이트

- [ ] **Step 2: 실패 테스트 작성** — `internal/api/ws_test.go`

```go
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeSignal implements queue.Signaler for streaming tests.
type fakeSignal struct{ ch chan []byte }

func (f *fakeSignal) PublishAbort(context.Context, string) error { return nil }
func (f *fakeSignal) SubscribeAbort(context.Context, string) (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}
func (f *fakeSignal) PublishMetric(_ context.Context, _ string, p []byte) error {
	f.ch <- p
	return nil
}
func (f *fakeSignal) SubscribeMetrics(context.Context, string) (<-chan []byte, func()) {
	return f.ch, func() {}
}

func TestWSStream(t *testing.T) {
	fs := &fakeSignal{ch: make(chan []byte, 4)}
	r := NewRouter(Deps{Signal: fs, DevToken: "dev"})
	srv := httptest.NewServer(r)
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/load-tests/abc/stream"
	c, _, err := websocket.DefaultDialer.Dial(url, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	fs.ch <- []byte(`{"rps":10}`)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "rps") {
		t.Fatalf("unexpected msg %s", msg)
	}
}
```

- [ ] **Step 3: 테스트 실패 확인**

Run: `cd services/load-test && go test ./internal/api/ -run TestWSStream`
Expected: FAIL (no-op registerWS라 연결/메시지 없음)

- [ ] **Step 4: ws.go 실제 구현 (스텁 교체)**

`internal/api/ws.go` 전체 교체:
```go
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // dev: allow all origins
}

func registerWS(r *gin.Engine, d Deps) {
	r.GET("/load-tests/:id/stream", func(c *gin.Context) {
		id := c.Param("id")
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		ctx := c.Request.Context()
		metrics, cancel := d.Signal.SubscribeMetrics(ctx, id)
		defer cancel()

		// reader goroutine detects client close
		closed := make(chan struct{})
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					close(closed)
					return
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case <-closed:
				return
			case msg, ok := <-metrics:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			}
		}
	})
}
```

- [ ] **Step 5: 테스트 통과 확인**

Run: `cd services/load-test && go test ./internal/api/ -run TestWSStream`
Expected: PASS

- [ ] **Step 6: 커밋**

```bash
git add services/load-test/internal/api/ws.go services/load-test/internal/api/ws_test.go services/load-test/go.mod services/load-test/go.sum
git commit -m "feat(load-test): WebSocket 실시간 메트릭 스트림"
```

---

## Task 9: Worker — k6 실행 · NDJSON 집계 · 요약 · 서킷브레이커

**Files:**
- Create: `services/load-test/internal/worker/k6runner.go`
- Create: `services/load-test/internal/worker/worker.go`
- Test: `services/load-test/internal/worker/aggregate_test.go`

**Interfaces:**
- Consumes: `queue.JobQueue`, `queue.Signaler`, `store.Store`, `scenario.Generate`, `breaker.Breaker`, `model.*`
- Produces:
  - `worker.Aggregator` — NDJSON `http_req_duration` 포인트를 받아 최종 요약 계산 (`NewAggregator()`, `Add(metricPoint)`, `Summary(vu int) model.LoadTestResult`)
  - `worker.parseLine([]byte) (metricPoint, bool)` — k6 NDJSON 한 줄 파싱
  - `worker.metricPoint{ metric string; value float64; step, status string; ts time.Time }`
  - `worker.isError(status string) bool`, `worker.percentile([]float64, float64) float64`
  - `worker.Run(ctx, Deps)` — 잡 소비 루프
  - `worker.Deps{ Queue queue.JobQueue; Signal queue.Signaler; Store *store.Store; K6Path string }`

- [ ] **Step 1: 집계 실패 테스트 작성** — `internal/worker/aggregate_test.go`

```go
package worker

import (
	"math"
	"testing"
)

func TestParseLineDuration(t *testing.T) {
	line := []byte(`{"type":"Point","metric":"http_req_duration","data":{"time":"2026-07-19T00:00:00Z","value":123.4,"tags":{"step":"/health","status":"200"}}}`)
	p, ok := parseLine(line)
	if !ok || p.metric != "http_req_duration" || math.Abs(p.value-123.4) > 0.001 {
		t.Fatalf("parse failed: %+v ok=%v", p, ok)
	}
	if p.step != "/health" || p.status != "200" {
		t.Fatalf("tags not parsed: %+v", p)
	}
}

func TestParseLineIgnoresNonPoint(t *testing.T) {
	if _, ok := parseLine([]byte(`{"type":"Metric","metric":"vus"}`)); ok {
		t.Fatal("should ignore non-Point")
	}
}

func TestAggregatorSummary(t *testing.T) {
	a := NewAggregator()
	// 10 requests, durations 100..1000ms, 2 of them 500 errors
	for i := 1; i <= 10; i++ {
		status := "200"
		if i > 8 {
			status = "500"
		}
		a.Add(metricPoint{metric: "http_req_duration", value: float64(i * 100), step: "/x", status: status})
	}
	s := a.Summary(5) // vu=5
	if s.ErrorRate < 0.19 || s.ErrorRate > 0.21 {
		t.Fatalf("error rate %.3f", s.ErrorRate)
	}
	if s.LatencyP50 <= 0 || s.LatencyP95 < s.LatencyP50 {
		t.Fatalf("bad percentiles: %+v", s)
	}
	if s.BottleneckEndpoint != "/x" {
		t.Fatalf("bottleneck %q", s.BottleneckEndpoint)
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd services/load-test && go test ./internal/worker/`
Expected: FAIL (undefined: parseLine/NewAggregator/metricPoint)

- [ ] **Step 3: k6runner.go 구현 (파싱 + 집계 + 실행)**

`internal/worker/k6runner.go`:
```go
package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/klaro/load-test/internal/breaker"
	"github.com/klaro/load-test/internal/model"
)

type metricPoint struct {
	metric string
	value  float64
	step   string
	status string
	ts     time.Time
}

type k6Line struct {
	Type   string `json:"type"`
	Metric string `json:"metric"`
	Data   struct {
		Time  time.Time `json:"time"`
		Value float64   `json:"value"`
		Tags  struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"tags"`
	} `json:"data"`
}

func parseLine(b []byte) (metricPoint, bool) {
	var l k6Line
	if err := json.Unmarshal(b, &l); err != nil {
		return metricPoint{}, false
	}
	if l.Type != "Point" {
		return metricPoint{}, false
	}
	if l.Metric != "http_req_duration" && l.Metric != "http_req_failed" {
		return metricPoint{}, false
	}
	return metricPoint{
		metric: l.Metric, value: l.Data.Value,
		step: l.Data.Tags.Step, status: l.Data.Tags.Status, ts: l.Data.Time,
	}, true
}

// Aggregator accumulates request durations and error counts to compute the
// final summary.
type Aggregator struct {
	durations    []float64
	total        int
	errors       int
	stepDuration map[string]float64
	stepCount    map[string]int
}

func NewAggregator() *Aggregator {
	return &Aggregator{stepDuration: map[string]float64{}, stepCount: map[string]int{}}
}

// isError treats missing tags and 4xx/5xx statuses as failed requests.
func isError(status string) bool {
	return status == "" || strings.HasPrefix(status, "5") || strings.HasPrefix(status, "4")
}

func (a *Aggregator) Add(p metricPoint) {
	if p.metric != "http_req_duration" {
		return
	}
	a.durations = append(a.durations, p.value)
	a.total++
	if isError(p.status) {
		a.errors++
	}
	a.stepDuration[p.step] += p.value
	a.stepCount[p.step]++
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func (a *Aggregator) Summary(vu int) model.LoadTestResult {
	sorted := append([]float64(nil), a.durations...)
	sort.Float64s(sorted)
	var rate float64
	if a.total > 0 {
		rate = float64(a.errors) / float64(a.total)
	}
	// slowest average step is the bottleneck
	bottleneck := ""
	var worst float64
	for step, sum := range a.stepDuration {
		avg := sum / float64(a.stepCount[step])
		if avg > worst {
			worst = avg
			bottleneck = step
		}
	}
	maxVU := vu
	if rate > 0.5 {
		maxVU = 0 // degraded across the board
	}
	return model.LoadTestResult{
		RPSAvg:                 float64(a.total),
		LatencyP50:             percentile(sorted, 0.50),
		LatencyP95:             percentile(sorted, 0.95),
		LatencyP99:             percentile(sorted, 0.99),
		ErrorRate:              rate,
		MaxVUBeforeDegradation: maxVU,
		BottleneckEndpoint:     bottleneck,
	}
}

// runK6 executes k6, streaming NDJSON points to onPoint until the process
// exits or ctx is cancelled. Cancelling ctx kills k6 (abort / circuit breaker).
func runK6(ctx context.Context, k6Path, script, target string, onPoint func(metricPoint)) error {
	cmd := exec.CommandContext(ctx, k6Path, "run", "--out", "json=-", "-e", "TARGET="+target, "-")
	cmd.Stdin = strings.NewReader(script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		if p, ok := parseLine(scanner.Bytes()); ok {
			onPoint(p)
		}
	}
	return cmd.Wait()
}

// newBreaker returns the default circuit breaker used during a run.
func newBreaker() *breaker.Breaker {
	return breaker.New(10*time.Second, 0.8, 20)
}
```

- [ ] **Step 4: worker.go 구현**

`internal/worker/worker.go`:
```go
package worker

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"time"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/scenario"
	"github.com/klaro/load-test/internal/store"
)

type Deps struct {
	Queue  queue.JobQueue
	Signal queue.Signaler
	Store  *store.Store
	K6Path string
}

// Run consumes jobs until ctx is cancelled.
func Run(ctx context.Context, d Deps) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		job, err := d.Queue.Dequeue(ctx)
		if err != nil {
			continue // timeout or transient; loop again
		}
		if err := d.process(ctx, job); err != nil {
			log.Printf("job %s failed: %v", job.LoadTestID, err)
		}
	}
}

func (d Deps) process(ctx context.Context, job model.Job) error {
	id := job.LoadTestID
	_ = d.Store.UpdateStatus(ctx, id, model.StatusProvisioning, nil)
	script, err := scenario.Generate(job.Scenario)
	if err != nil {
		_ = d.Store.UpdateStatus(ctx, id, model.StatusFailed, nil)
		return err
	}
	_ = d.Store.UpdateStatus(ctx, id, model.StatusRunning, nil)
	_ = d.Store.MarkStarted(ctx, id)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// abort signal (user-triggered)
	abortCh, abortCancel := d.Signal.SubscribeAbort(ctx, id)
	defer abortCancel()
	var abortReason string
	go func() {
		select {
		case <-abortCh:
			abortReason = "aborted by user"
			cancel()
		case <-runCtx.Done():
		}
	}()

	agg := NewAggregator()
	brk := newBreaker()
	win := newWindow()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case now := <-ticker.C:
				snap := win.flush()
				payload, _ := json.Marshal(snap.toWSMessage(now, job.Scenario.VU))
				_ = d.Signal.PublishMetric(ctx, id, payload)
				if trip, reason := brk.ShouldAbort(now); trip {
					abortReason = reason
					cancel()
				}
			}
		}
	}()

	runErr := runK6(runCtx, d.K6Path, script, job.TargetURL, func(p metricPoint) {
		agg.Add(p)
		if p.metric == "http_req_duration" {
			win.add(p)
			brk.Observe(p.ts, !isError(p.status))
		}
	})
	_ = runErr // k6 exits non-zero on threshold breach; we still aggregate below

	if abortReason != "" {
		reason := abortReason
		_ = d.Store.UpdateStatus(ctx, id, model.StatusAborted, &reason)
		payload, _ := json.Marshal(map[string]any{"event": "aborted", "reason": reason})
		_ = d.Signal.PublishMetric(ctx, id, payload)
	} else {
		_ = d.Store.UpdateStatus(ctx, id, model.StatusAggregating, nil)
	}

	summary := agg.Summary(job.Scenario.VU)
	if err := d.Store.SaveResult(ctx, id, summary); err != nil {
		return err
	}
	_ = d.Store.MarkFinished(ctx, id)
	if abortReason == "" {
		_ = d.Store.UpdateStatus(ctx, id, model.StatusCompleted, nil)
	}
	return nil
}

// --- per-window accumulation for the live stream ---

type window struct {
	durations []float64
	total     int
	errors    int
}

func newWindow() *window { return &window{} }

func (w *window) add(p metricPoint) {
	w.durations = append(w.durations, p.value)
	w.total++
	if isError(p.status) {
		w.errors++
	}
}

type windowSnap struct {
	count   int
	errRate float64
	p95     float64
}

func (w *window) flush() windowSnap {
	snap := windowSnap{count: w.total}
	if w.total > 0 {
		snap.errRate = float64(w.errors) / float64(w.total)
		sorted := append([]float64(nil), w.durations...)
		sort.Float64s(sorted)
		snap.p95 = percentile(sorted, 0.95)
	}
	w.durations = nil
	w.total = 0
	w.errors = 0
	return snap
}

func (s windowSnap) toWSMessage(now time.Time, vu int) map[string]any {
	return map[string]any{
		"ts":             now.UTC().Format(time.RFC3339),
		"rps":            s.count,
		"latency_p95_ms": s.p95,
		"error_rate":     s.errRate,
		"active_vu":      vu,
	}
}
```

- [ ] **Step 5: 집계 테스트 통과 확인**

Run: `cd services/load-test && go test ./internal/worker/`
Expected: PASS

- [ ] **Step 6: 커밋**

```bash
git add services/load-test/internal/worker/
git commit -m "feat(load-test): 워커 - k6 실행, NDJSON 집계, 서킷브레이커, 요약"
```

---

## Task 10: 엔트리포인트 · Docker Compose · 통합 테스트 · README

**Files:**
- Create: `services/load-test/cmd/api/main.go`
- Create: `services/load-test/cmd/worker/main.go`
- Create: `services/load-test/testdata/target/main.go`
- Create: `services/load-test/Dockerfile.api`
- Create: `services/load-test/Dockerfile.worker`
- Create: `services/load-test/docker-compose.yml`
- Create: `services/load-test/README.md`

**Interfaces:**
- Consumes: 모든 이전 태스크
- Produces: 실행 가능한 `api`/`worker` 바이너리, `docker compose up` 전체 스택, 수동 e2e 검증 절차

- [ ] **Step 1: cmd/api/main.go 구현**

```go
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
		Store:    st,
		Queue:    rd,
		Signal:   rd,
		Verifier: domainverify.New(),
		DevToken: env("DEV_TOKEN", "dev"),
	})
	addr := env("API_ADDR", ":8080")
	log.Printf("api listening on %s", addr)
	log.Fatal(r.Run(addr))
}
```

- [ ] **Step 2: cmd/worker/main.go 구현**

```go
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
	worker.Run(ctx, worker.Deps{
		Queue:  rd,
		Signal: rd,
		Store:  st,
		K6Path: env("K6_PATH", "k6"),
	})
}
```

- [ ] **Step 3: 테스트용 스텁 대상 서버** — `testdata/target/main.go`

```go
package main

import (
	"net/http"
	"os"
)

// A tiny target server for manual e2e. If MODE=fail it always 500s,
// exercising the circuit breaker.
func main() {
	mode := os.Getenv("MODE")
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if mode == "fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":9000"
	}
	_ = http.ListenAndServe(addr, nil)
}
```

- [ ] **Step 4: Dockerfile.api 작성**

```dockerfile
FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/api /api
EXPOSE 8080
ENTRYPOINT ["/api"]
```

- [ ] **Step 5: Dockerfile.worker 작성 (k6 포함)**

```dockerfile
FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/worker ./cmd/worker

FROM grafana/k6:latest
USER root
COPY --from=build /out/worker /usr/local/bin/worker
ENV K6_PATH=k6
ENTRYPOINT ["worker"]
```

- [ ] **Step 6: docker-compose.yml 작성**

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: klaro
      POSTGRES_PASSWORD: klaro
      POSTGRES_DB: klaro
    ports: ["5432:5432"]
    volumes:
      - ./migrations/0001_init.sql:/docker-entrypoint-initdb.d/0001_init.sql:ro
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U klaro"]
      interval: 2s
      timeout: 3s
      retries: 20

  redis:
    image: redis:7
    ports: ["6379:6379"]

  api:
    build: { context: ., dockerfile: Dockerfile.api }
    environment:
      DATABASE_URL: postgres://klaro:klaro@postgres:5432/klaro?sslmode=disable
      REDIS_ADDR: redis:6379
      DEV_TOKEN: dev
    ports: ["8080:8080"]
    depends_on:
      postgres: { condition: service_healthy }
      redis: { condition: service_started }

  worker:
    build: { context: ., dockerfile: Dockerfile.worker }
    environment:
      DATABASE_URL: postgres://klaro:klaro@postgres:5432/klaro?sslmode=disable
      REDIS_ADDR: redis:6379
    extra_hosts:
      - "host.docker.internal:host-gateway"
    depends_on:
      postgres: { condition: service_healthy }
      redis: { condition: service_started }
```

- [ ] **Step 7: README.md 작성 (수동 e2e 절차)**

`README.md`:
````markdown
# klaro 부하 테스트 서비스 (MVP)

도메인 검증 → 부하 테스트 생성 → 로컬 k6 워커 실행 → WebSocket 실시간 메트릭 → 결과 요약까지의
end-to-end 수직 슬라이스. 설계: `docs/superpowers/specs/2026-07-19-load-test-service-design.md`.

## 구성
- `api` — Control Plane(REST/WS), Go/Gin
- `worker` — 잡 소비 + k6 실행 + 집계, Go
- `postgres` — 메타데이터, `redis` — 잡 큐 + 메트릭 pub/sub

## 로컬 실행
```bash
cd services/load-test
docker compose up --build -d
curl -s localhost:8080/healthz   # {"status":"ok"}
```

## 수동 e2e 스모크
1. 스텁 대상 서버 기동(정상): `ADDR=:9000 go run ./testdata/target`
2. 대상 호스트를 검증 도메인으로 시드(개발 편의):
   ```sql
   INSERT INTO verified_domains (project_id, domain, method, token, status, verified_at)
   VALUES ('00000000-0000-0000-0000-000000000002','host.docker.internal','file','x','verified', now());
   ```
3. 부하 테스트 생성:
   ```bash
   curl -s -X POST localhost:8080/projects/00000000-0000-0000-0000-000000000002/load-tests \
     -H 'Authorization: Bearer dev' -H 'Content-Type: application/json' \
     -d '{"target_url":"http://host.docker.internal:9000","scenario":{"vu":5,"duration_sec":5,"ramp_up_sec":1,"steps":[{"method":"GET","path":"/"}],"thresholds":{"error_rate":0.5}}}'
   ```
4. 상태가 `completed`가 될 때까지 폴링:
   `curl localhost:8080/load-tests/<id> -H 'Authorization: Bearer dev'`
5. 결과: `curl localhost:8080/load-tests/<id>/results -H 'Authorization: Bearer dev'`
6. 실시간 스트림: `websocat ws://localhost:8080/load-tests/<id>/stream`

## 서킷브레이커 확인
대상 서버를 `MODE=fail ADDR=:9000 go run ./testdata/target`로 기동한 뒤 위 절차 반복 →
상태가 `aborted`, `aborted_reason`에 `error_rate > 0.80` 기록.

## 테스트
```bash
go test ./...                                   # 단위 테스트
go test -tags integration ./...                 # 통합(환경변수 TEST_DATABASE_URL / TEST_REDIS_ADDR 필요)
```

## 알려진 한계 (MVP 의도)
- 워커 잡 소비는 at-most-once(크래시 시 유실). NATS JetStream 승격 시 해결.
- 인증은 개발용 스텁(고정 org/user). JWT/OAuth/RBAC/RLS는 후속.
- 에러 판정은 status 태그 4xx/5xx 근사.
````

- [ ] **Step 8: 전체 빌드 + 단위 테스트 확인**

Run: `cd services/load-test && go build ./... && go test ./...`
Expected: 빌드 성공 + 모든 유닛 테스트 PASS (integration 태그 테스트는 제외됨)

- [ ] **Step 9: 수동 e2e 검증 실행**

Run:
```bash
cd services/load-test && docker compose up --build -d && sleep 20 && curl -s localhost:8080/healthz
```
Expected: `{"status":"ok"}`. 이어서 README의 수동 e2e 스모크 절차로 `completed` 결과와 서킷브레이커 `aborted`를 확인한다.

- [ ] **Step 10: 커밋**

```bash
git add services/load-test/cmd services/load-test/testdata services/load-test/Dockerfile.* services/load-test/docker-compose.yml services/load-test/README.md
git commit -m "feat(load-test): 엔트리포인트, Docker Compose, README 및 수동 e2e"
```

---

## Self-Review 체크 결과

**Spec coverage:**
- §2 아키텍처 → Task 5/6/7/8/9/10 (Postgres, Redis 큐/pubsub, API, WS, worker, compose) ✅
- §3 컴포넌트 경계 → model(T1), scenario(T2), breaker(T3), domainverify(T4), store(T5), queue(T6), api(T7/8), worker(T9) ✅
- §4 데이터 모델 → Task 5 마이그레이션 ✅
- §5 상태머신 → Task 1 `CanTransition` + Task 5 `UpdateStatus` 강제 ✅
- §6 핵심 흐름(정상/서킷브레이커/사용자 abort) → Task 9 process ✅
- §7 엔드포인트 10종 → Task 7/8 라우터 ✅
- §8 시나리오 생성 → Task 2 ✅
- §9 인증 스텁 → Task 7 middleware ✅
- §10 테스트 전략 → 각 태스크 단위 + Task 5/6 integration + Task 10 수동 e2e ✅
- §13 완료 기준 5항목 → Task 10 Step 9 수동 e2e(completed/서킷브레이커) + 도메인 게이트(Task 7 loadtests) + 유닛/통합 ✅

**Placeholder scan:** 모든 코드 스텝에 실제 코드 포함. e2e는 compose 의존이라 자동 테스트 대신 README의 수동 절차(Task 10 Step 9)로 완결 기준 커버 — 이는 의도된 설계이며 플레이스홀더 아님.

**Type consistency:** `metricPoint`/`Aggregator`/`parseLine`/`isError`/`percentile`(worker), `Deps`(api·worker 각 패키지), `Signaler`/`JobQueue`(queue), `model.Status` 상수, `store` 메서드 시그니처, `Verifier{scheme}` 필드가 태스크 간 일관됨을 확인. Task 7의 `registerWS` 스텁 → Task 8 실제 구현 교체 경로 명시.

> 알려진 한계(MVP 의도): (1) 워커 잡 소비 at-most-once. (2) `isError`는 status 태그 근사. (3) e2e 자동화는 수동 절차로 대체.
