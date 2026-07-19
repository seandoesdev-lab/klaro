# klaro 부하 테스트 서비스 — MVP 설계 (수직 슬라이스)

**버전** v0.1.0-dev · **작성일** 2026-07-19 · **상태** 구현 착수 승인됨

관련 문서: [기술 스택](../../klaro/00-tech-stack.md) · [기술 설계서](../../klaro/01-technical-design.md) · [데이터 모델](../../klaro/02-data-model.md) · [API 스펙](../../klaro/03-api-spec.md)

---

## 1. 목표 & 범위

### 목표
`도메인 검증 → 부하 테스트 생성 → 로컬 k6 워커 실행 → WebSocket 실시간 메트릭 → 결과 요약 저장`까지 **end-to-end로 실제 동작하는** 최소 수직 슬라이스를 구현한다.

### 확정된 결정 (착수 전 합의)
- **범위**: MVP 수직 슬라이스 (전체 스펙 아님).
- **Control Plane 언어**: Go (Gin).
- **오케스트레이션**: Docker Compose.
- **인증**: 개발용 스텁 (고정 org/user 주입).
- **잡 큐**: Redis List (BLPOP), `JobQueue` 인터페이스 뒤에 추상화.
- **메트릭 전송**: k6 `--out json=-` NDJSON을 worker가 파싱.

### 이번 범위에서 제외 (자리만 확보)
서브스크립션·과금(VU-Minutes)·보안 스캔(S2)·APM(S3)·리포트/AI 요약(S4)·gRPC mTLS·K8s Job 오케스트레이션·RLS 멀티테넌시·JWT/OAuth/RBAC. 스키마 컬럼(`org_id` 등)과 미들웨어 자리는 남겨 확장 가능하게 둔다.

---

## 2. 아키텍처

```
[client] ──REST/WS──▶ Control Plane API (Go/Gin)
                          │  ├─ Postgres  (orgs, projects, verified_domains, load_tests, load_test_results)
                          │  └─ Redis     (job queue: List(BLPOP)  +  metrics fan-in: Pub/Sub)
                          ▼ enqueue job
                      Load Worker (Go) ──shell──▶ k6 (--out json=-) ──HTTP부하──▶ [테스트 대상]
                          │ 1s 윈도 집계 + 서킷브레이커
                          └─ publish metrics:<id> ──▶ Control Plane WS fan-out ──▶ client
```

Docker Compose 서비스: `postgres`, `redis`, `api`, `worker` (+ 통합 테스트용 `target` 스텁 서버는 테스트 전용).

---

## 3. 컴포넌트 경계 (각각 독립 테스트 가능)

| 컴포넌트 | 책임 | 의존 | 인터페이스 |
|----------|------|------|-----------|
| **api** (Control Plane) | REST 핸들러, 잡 상태머신, WS 허브, 도메인 검증 게이트 | Postgres, Redis | HTTP/WS |
| **worker** | 잡 소비 → k6 실행 → NDJSON 집계 → 메트릭 발행 → 요약 저장 | Redis, Postgres, k6 바이너리 | JobQueue 소비 |
| **scenario** (공유 pkg) | scenario JSON ↔ k6 JS 생성 + 검증 | 없음 (순수 함수) | `Generate(Scenario) (string, error)`, `Validate(Scenario) error` |
| **domainverify** (api 모듈) | DNS TXT / 파일 챌린지 실제 조회 | net(DNS), http | `Verify(domain, method, token) (bool, error)` |
| **breaker** (worker pkg) | 슬라이딩 윈도 에러율 계산, 임계 초과 시 abort 신호 | 없음 (순수 로직) | `Observe(point)`, `ShouldAbort() (bool, reason)` |

**설계 원칙**: `scenario`와 `breaker`는 외부 의존이 없는 순수 로직으로 분리하여 표 기반 단위 테스트가 쉽도록 한다. `JobQueue`는 인터페이스로 두어 Redis→NATS 교체 시 소비자 코드가 바뀌지 않게 한다.

---

## 4. 데이터 모델 (이번 구현분)

문서 [데이터 모델](../../klaro/02-data-model.md) 스키마를 준수하되 이번 슬라이스에 필요한 테이블만 생성한다. `org_id` 컬럼은 유지하되 RLS 정책은 이번 제외.

- **organizations** (id, name, created_at) — 개발 스텁이 참조할 고정 org 시드.
- **projects** (id, org_id, name, created_at).
- **verified_domains** (id, project_id, domain, method[dns_txt|file], token, status[pending|verified|failed], verified_at, UNIQUE(project_id, domain)).
- **load_tests** (id, project_id, target_url, scenario jsonb, vu, duration_sec, status, aborted_reason, started_at, finished_at, created_by, created_at).
- **load_test_results** (id, load_test_id, rps_avg, latency_p50/p95/p99, error_rate, max_vu_before_degradation, bottleneck_endpoint, metrics_ref, created_at).

마이그레이션: `migrations/` 아래 순번 SQL 파일. 초기 시드로 고정 org/project 1건.

---

## 5. 상태머신

```
PENDING → VALIDATING → QUEUED → PROVISIONING → RUNNING → AGGREGATING → COMPLETED
              │                       │            │
              ▼                       ▼            ▼
           REJECTED                FAILED       ABORTED
```

- `VALIDATING`: target_url 도메인이 `verified_domains`에 verified로 존재하는지 확인. 미검증 → `REJECTED` (403 `DOMAIN_NOT_VERIFIED`).
- `QUEUED`: Redis 잡 큐에 push.
- `PROVISIONING→RUNNING`: worker가 잡 픽업, k6 시작.
- `RUNNING`: 1s 윈도 메트릭 스트림, 서킷브레이커 감시.
- `AGGREGATING`: k6 종료 후 요약 계산.
- `ABORTED`: 서킷브레이커(에러율>0.8) 또는 사용자 `/abort`. 부분 결과 보존.
- `FAILED`: k6 실행 자체 실패(프로세스 오류 등).

상태 전이는 api와 worker가 공유하는 허용 전이 맵으로 강제(불법 전이 거부).

---

## 6. 핵심 흐름

### 6.1 부하 테스트 생성 (정상)
1. `POST /projects/:id/load-tests` → scenario 검증 → target 도메인 verified 확인 → `load_tests` insert(status=queued) → Redis 큐 push → `202 { id, status: "validating" }`.
2. worker BLPOP → status=provisioning→running, started_at 기록.
3. worker: scenario JSON → k6 스크립트 생성 → `k6 run --out json=- script.js` 실행.
4. worker: NDJSON stdout 스트림을 라인 파싱 → 1초 윈도로 rps/latency_p95/error_rate/active_vu 집계 → `metrics:<id>` 채널 발행 + breaker.Observe.
5. api WS `/load-tests/:id/stream`: `metrics:<id>` 구독 → 연결 클라이언트로 fan-out.
6. k6 종료 → status=aggregating → 전체 요약(rps_avg, p50/p95/p99, error_rate, max_vu_before_degradation, bottleneck_endpoint) 계산 → `load_test_results` insert → status=completed, finished_at.

### 6.2 서킷브레이커 abort
- worker breaker가 슬라이딩 윈도(10초)에서 에러율 계산 → `>0.8` 지속 시 k6 프로세스 kill → status=aborted, aborted_reason="error_rate > 0.8" → `metrics:<id>`로 `{event:"aborted"}` 발행 → 부분 요약 보존.

### 6.3 사용자 abort
- `POST /load-tests/:id/abort` → api가 `abort:<id>` 신호(Redis) 발행 → worker가 감지 후 k6 kill → status=aborted.

---

## 7. 엔드포인트 (이번 구현분)

| 메서드·경로 | 설명 |
|------------|------|
| `POST /projects/:id/domains` | 도메인 등록 + 검증 토큰/방법 발급 |
| `POST /projects/:id/domains/:domainId/verify` | 소유권 검증 실행(DNS TXT/파일) |
| `GET /projects/:id/domains` | 검증 도메인 목록 |
| `POST /projects/:id/load-tests` | 부하 테스트 생성/실행 |
| `GET /projects/:id/load-tests` | 목록 |
| `GET /load-tests/:id` | 상세/상태/진행률 |
| `POST /load-tests/:id/abort` | 강제 종료 |
| `GET /load-tests/:id/results` | 요약 결과 |
| `WS /load-tests/:id/stream` | 실시간 메트릭 구독(≤2초 주기) |
| `GET /healthz` | 헬스체크 |

요청/응답 포맷은 [API 스펙 §3.2](../../klaro/03-api-spec.md) 준수. 에러 포맷 `{ "error": { "code", "message", "details" } }`.

---

## 8. 시나리오 → k6 스크립트 생성

입력 scenario JSON:
```json
{
  "vu": 1000,
  "duration_sec": 1800,
  "ramp_up_sec": 60,
  "steps": [ { "method": "GET", "path": "/api/v1/health" } ],
  "thresholds": { "http_req_duration_p95_ms": 2000, "error_rate": 0.05 }
}
```
→ k6 JS 생성: `options.stages`(ramp-up→plateau), `options.thresholds`, `default function`에서 steps 순회 요청. 각 요청에 스텝 식별 태그(`tags: { step: path }`)를 붙여 병목 엔드포인트 산출 근거로 사용.

검증 규칙: vu>0, duration_sec>0, steps 비어있지 않음, method 화이트리스트(GET/POST/PUT/PATCH/DELETE/HEAD), path는 `/`로 시작. 위반 시 `VALIDATION_ERROR`.

---

## 9. 인증 (MVP 스텁)

`Authorization: Bearer <dev-token>` 헤더를 검사하는 미들웨어. 유효 시 고정 org/user 컨텍스트 주입. 토큰 불일치 → `401 UNAUTHENTICATED`. JWT/OAuth/RBAC/RLS는 미들웨어 교체 지점만 남기고 이번 제외.

---

## 10. 테스트 전략

- **단위**: scenario 생성/검증(표 기반), 상태머신 전이(허용/거부), breaker 집계(에러율 임계), 도메인 검증 파서(TXT 매칭/파일 매칭).
- **통합**: 로컬 테스트 HTTP 서버(`target` 스텁)를 대상으로 **실제 소규모 k6 잡(5 VU / 5s)** 실행 → WS 메트릭 수신 확인 → `load_test_results` 저장 검증.
- **서킷브레이커 통합**: 항상 500 반환하는 대상으로 abort 발동 및 status=aborted 검증.
- **도메인 게이트**: 미검증 도메인으로 load-test 생성 시 403 `DOMAIN_NOT_VERIFIED` 검증.

Postgres/Redis는 Compose 또는 testcontainers로 기동. k6 바이너리는 워커 이미지(`grafana/k6` 기반)에 포함.

---

## 11. 디렉터리 구조 (제안)

```
services/
  load-test/
    cmd/
      api/main.go          # Control Plane API 엔트리
      worker/main.go       # Load Worker 엔트리
    internal/
      api/                 # 핸들러, 라우터, WS 허브, 미들웨어
      worker/              # 잡 소비, k6 실행 supervisor, 집계
      scenario/            # k6 스크립트 생성/검증 (순수)
      breaker/             # 서킷브레이커 (순수)
      domainverify/        # 도메인 소유권 검증
      store/               # Postgres 리포지토리
      queue/               # JobQueue 인터페이스 + Redis 구현
      model/               # 공유 도메인 타입 + 상태머신
    migrations/            # 순번 SQL
    testdata/              # 통합 테스트용 스텁 대상
    docker-compose.yml
    Dockerfile.api
    Dockerfile.worker
    go.mod
```

---

## 12. 리스크 & 대응

- **k6 NDJSON 볼륨**: 고 VU에서 라인 폭증 → worker에서 즉시 1초 윈도 집계(원본 미보존)로 메모리 방어.
- **worker 크래시 시 잡 유실**: MVP는 at-most-once(단순). 재처리는 NATS JetStream 승격 시 확보(문서 반영).
- **k6 미설치 환경**: 통합 테스트는 k6 존재를 전제로 스킵 가드(빌드 태그/환경변수)로 분기.
- **WS 백프레셔**: 느린 구독자용 버퍼 채널 + drop-oldest 정책.

---

## 13. 완료 기준 (Definition of Done)

1. `docker compose up` 후 검증된 도메인 대상 부하 테스트를 생성하면 상태가 `completed`까지 진행된다.
2. WS 스트림으로 ≤2초 주기 메트릭이 수신된다.
3. 500 대상에서 서킷브레이커가 `aborted`로 안전 종료한다.
4. 미검증 도메인 요청이 403으로 거부된다.
5. 단위·통합 테스트가 통과한다.
