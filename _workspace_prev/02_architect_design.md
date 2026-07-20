# klaro S1 부하 테스트 — 백엔드 MVP 아키텍처 설계

**작성** architect · **버전** v1 · **입력** `_workspace/01_spec-analyst_contract.md` (C1~C13)
**확정 스택(재논의 금지)** Control Plane = Go(Echo) · 로컬 = Docker Compose · 격리 = 공용 DB + RLS

---

## 스택 결정

### 확정 (재논의 금지 — 상위 지시)
- **Control Plane 언어 = Go (Echo)**: 고동시성 WS fan-out + gRPC 스트리밍 + 낮은 메모리. Echo는 미들웨어(인증/RLS 세션 주입)가 얇고 명시적. 트레이드오프: Gin 대비 생태계 작으나 성능/명시성 우위.
- **로컬 오케스트레이션 = Docker Compose**: 단일 머신 재현·부팅 단순. 트레이드오프: 동적 스케일아웃/네임스페이스 격리는 K8s보다 약하므로 `Orchestrator` 인터페이스 뒤로 추상화해 프로덕션 K8s Job 전환 비용을 흡수.
- **테넌트 격리 = 공용 DB + RLS**: `org_id` + 세션 GUC `app.current_org`. 트레이드오프: 정책 누락 시 크로스테넌트 노출 위험 → 마이그레이션에서 `FORCE ROW LEVEL SECURITY` + 앱은 non-superuser 롤로 접속해 우회 원천 차단.

### 결정 필요 목록 9개 (합리적 기본값으로 확정)

1. **잡 큐 = NATS JetStream** — 잡 라이프사이클 이벤트/과금 이벤트 스트림용 경량 퍼시스턴트 큐. 역할 분담: **큐(NATS)는 "잡 상태 전이 이벤트·usage 이벤트·abort 브로드캐스트"의 pub/sub**, **gRPC는 CP→특정 워커 직접 제어(DispatchJob/AbortJob) + 워커→CP 메트릭 스트림**. 즉 큐=비동기 팬아웃/이력, gRPC=동기 명령·스트림. 트레이드오프: Kafka 대비 장기 보존·리플레이 약하나 S1 규모엔 과잉 회피. `EventBus` 인터페이스 뒤로 두어 Kafka 교체 여지.
   - **[확인 필요]** 과금 이벤트를 감사·재처리 강하게 요구하면(SOC2 장기 보존) Kafka 재검토. S1은 NATS로 진행.

2. **TSDB = VictoriaMetrics** — 01 다이어그램 확정안 채택. `metrics_ref` 스키마 = `{"tsdb":"victoriametrics","series_prefix":"klaro_lt_<load_test_id>","from":<start_ts>,"to":<end_ts>}`(JSONB). 시계열 라벨: `load_test_id`, `endpoint_id`, `metric`(rps/latency_p95/error_rate/active_vu). 트레이드오프: Prometheus 원격쓰기 호환·저비용, 고카디널리티 라벨 남용 주의(endpoint_id까지만 태깅).

3. **메트릭 역류 경로 = 워커 gRPC `StreamMetrics` → CP 집계 → WS fan-out** — 워커 사이드카가 ≤2초 배치로 CP에 gRPC 스트림 push, CP가 (a) VictoriaMetrics에 원격쓰기, (b) 해당 load_test 구독 WS 세션에 fan-out. 트레이드오프: OTLP 대비 스키마 자체 통제·mTLS 단일 채널 재사용, 표준 텔레메트리 툴 연동은 후순위. WS는 클라이언트 방향 전용(워커는 WS 안 씀).

4. **서킷 브레이커 파라미터** — 슬라이딩 윈도 = **10초**, 트립 조건 = **윈도 내 요청≥20건 AND (5xx+conn_err 비율 > 0.80)** 또는 **대상 503이 연속 3개 윈도(30초) 지속**. 최소 요청수 게이트(≥20)로 초기 소표본 오탐 방지. 워커 사이드카가 로컬 판정 후 CP에 `CircuitTripped` 신호 → CP가 전 워커 `AbortJob` 브로드캐스트. 트레이드오프: 윈도 짧으면 민감·길면 지연 → 10초는 ≤2초 스트림 주기의 5배로 안정.

5. **k6 시나리오 = 선언형 JSON → k6 스크립트 생성** — CP가 scenario(JSON)를 검증·저장, 워커 엔트리포인트가 임베드된 Go 템플릿으로 k6 JS를 렌더해 실행. weighted = 단일 `default` 함수 내 가중 랜덤 선택(`__ENV`로 weight 테이블 주입), journey = 순차 `group` 반복. 트레이드오프: 사용자 임의 JS 대비 표현력 제한, 그러나 주입 공격/샌드박스 리스크 제거 + API 단위 태깅 강제 가능. k6 커스텀 JS는 후속 옵션.

6. **weight 분배 정밀도** — 배분은 **확률 기반(요청 시점 가중 랜덤)**을 기본으로 하고 VU를 정수 분할하지 않는다(총 VU는 k6 공유 풀). API별 목표 비율 = `weight_i / Σweight`. 검증 규칙: `Σweight > 0`, 각 `weight ≥ 1`. 수용 오차 = **실측 요청 비율이 목표 ±5%p 이내**(총 요청 ≥1000 기준). journey는 스텝 순서 고정이라 weight 무시. 트레이드오프: 정수 VU 분할 대비 저VU에서 편차 크나 오차 게이트로 관리.

7. **VU-Minutes 산정** — `vu_minutes = Σ(구간별 활성VU × 구간분)`, 램프업은 사다리꼴 근사(구간 평균 VU). ABORTED/실패 시 **`started_at`~`finished_at` 실측 활성구간까지만** 계량(중단 이후 미과금). REJECTED/PENDING/VALIDATING 종결은 발행 안 함. 발행 payload에 `vu_minutes`, `computation{method, ramp_profile, effective_sec}` 근거 포함. overage/amount는 Billing 책임(NULL로 발행). 트레이드오프: 실측 사다리꼴은 근사치나 CP가 스트림으로 이미 active_vu 시계열 보유해 계산 가능.

8. **알림 채널 스텁** — 개발 = **MailHog(SMTP) + Slack Incoming Webhook 스텁(로그 기록만, 실제 전송 no-op 가능)**. 트리거 = `job.aborted`(서킷/사용자), `job.failed`. 최소 payload = `{event, load_test_id, org_id, project_id, reason, at, dashboard_url}`. `Notifier` 인터페이스로 채널 추상화. 트레이드오프: 실제 라우팅/구독 설정은 후속, S1은 발화 지점·payload 계약만 고정.

9. **워커 격리(로컬)** — Docker Compose에서 워커를 **전용 브리지 네트워크 `klaro-workers`**에 배치, CP↔워커는 gRPC(mTLS)만 노출, 워커→외부(테스트 대상)는 egress 허용·워커↔워커 통신 차단(각 잡 컨테이너는 독립, 상호 링크 없음). 리소스 상한 = 컨테이너별 `--cpus/--memory` cap. 트레이드오프: K8s NetworkPolicy/네임스페이스만큼 강한 격리는 아니나, `Orchestrator` 구현체가 네트워크·라벨·리소스 정책을 캡슐화해 K8s 전환 시 정책만 이식.

---

## 모듈 경계 (Go 패키지 구조)

원칙: `internal/` 하위 도메인별 수직 슬라이스. 각 패키지는 하나의 책임 + 인터페이스로만 소통. 오케스트레이션은 포트/어댑터로 분리.

```
trevally/
├── cmd/
│   ├── controlplane/        # CP 서버 엔트리포인트 (Echo + gRPC 서버 + WS)
│   └── worker/              # k6 워커 사이드카 엔트리포인트 (gRPC 클라이언트 + k6 실행)
├── internal/
│   ├── platform/            # 횡단 관심사 (도메인 무관)
│   │   ├── config/          # env 로드, 시크릿
│   │   ├── db/              # pgx 풀, 트랜잭션, RLS 세션 주입(SET app.current_org)
│   │   ├── tsdb/            # VictoriaMetrics 클라이언트 (write/query 추상)
│   │   ├── eventbus/        # EventBus 인터페이스 + NATS JetStream 어댑터
│   │   ├── mtls/            # 인증서 로드, gRPC TLS credential 구성
│   │   ├── audit/           # audit_logs 기록 헬퍼
│   │   └── httpx/           # 에러 응답 규약(CONFLICT/VALIDATION_ERROR/... ), 미들웨어
│   ├── tenancy/             # org/project 컨텍스트, RLS 가드 미들웨어 (org_id 해석)
│   ├── domainverify/        # C1,C2 — 도메인 등록·토큰 발급·소유권 검증(DNS TXT/파일)
│   │   ├── service.go       # 등록/검증 로직, DNS·HTTP 프로버
│   │   ├── prober.go        # Prober 인터페이스(dns_txt/file) — 외부 조회 추상
│   │   ├── repo.go          # verified_domains CRUD
│   │   └── handler.go       # REST 핸들러
│   ├── catalog/             # C3,C4 — endpoints CRUD + import(openapi/har)
│   │   ├── service.go
│   │   ├── importer.go      # Importer 인터페이스 + openapi/har 파서
│   │   ├── repo.go
│   │   └── handler.go
│   ├── loadtest/            # C5,C6,C9,C11,C12 — 잡 생명주기 오케스트레이션(도메인 코어)
│   │   ├── service.go       # 생성 진입 게이트(도메인검증·endpoint소속·쿼터), abort
│   │   ├── statemachine.go  # 상태 전이 표·전이 검증(불변식)
│   │   ├── scenario.go      # scenario 검증(mode/vu/weight), k6 생성기 입력 빌드
│   │   ├── results.go       # C11 요약 결과 조회, C12 시계열 조회(tsdb 위임)
│   │   ├── repo.go          # load_tests, load_test_results
│   │   └── handler.go
│   ├── orchestrator/        # 워커 spawn/abort/cleanup 포트 (Docker↔K8s 교체점)
│   │   ├── orchestrator.go  # Orchestrator 인터페이스: Spawn/Abort/Cleanup/List
│   │   ├── docker.go        # DockerOrchestrator (Compose/docker SDK)
│   │   └── k8s.go           # K8sOrchestrator (스텁 — Job 매핑, S1 미구현)
│   ├── metrics/             # C7,C8,C10 — 워커 gRPC 수신·집계·WS fan-out
│   │   ├── grpc_server.go   # WorkerControl 서비스(StreamMetrics/Heartbeat/CircuitTripped 수신)
│   │   ├── aggregator.go    # 실시간 집계 + VictoriaMetrics write
│   │   ├── ws_hub.go        # load_test별 구독 허브, ≤2초 push, aborted 이벤트
│   │   └── ws_handler.go    # WS 업그레이드 + 인가(테넌트 스코프)
│   ├── circuit/             # C8 — 서킷 브레이커 판정(워커측) + CP측 abort 브로드캐스트
│   │   └── breaker.go       # 슬라이딩 윈도, 트립 조건
│   ├── billing/             # C13 — usage 이벤트 발행 지점(계산 상세 제외)
│   │   ├── emitter.go       # VU-Minutes 산정 + EventBus 발행
│   │   └── repo.go          # usage_records 삽입(overage/amount NULL)
│   └── k6gen/               # 선언형 scenario → k6 JS 렌더러(워커가 사용)
│       ├── template.go      # weighted/journey 템플릿
│       └── generate.go
├── proto/
│   └── worker/v1/           # WorkerControl protobuf (gRPC 계약)
├── migrations/              # SQL 마이그레이션(FK 의존 순, RLS 정책 포함)
└── deploy/
    └── docker-compose.yml   # CP, postgres, nats, victoriametrics, mailhog, worker 네트워크
```

**핵심 경계 규칙**
- `loadtest.service`가 **잡 생성 진입 게이트**: 도메인검증→endpoint소속→쿼터 순 검사 후에만 상태머신 진입(불변식 우회 불가).
- 워커 생성/회수는 `orchestrator.Orchestrator` 인터페이스로만 호출(loadtest·metrics는 구현체를 모름) → Docker↔K8s 교체점 단일화. idle=0은 `Cleanup`을 잡 종결 finalizer로 강제.
- `metrics` 패키지만 워커 gRPC를 종단(mTLS). WS fan-out과 TSDB write의 단일 소유자.
- `k6gen`은 워커 바이너리에 임베드(CP는 scenario JSON만 전달) → 샌드박스 경계.

---

## 데이터 모델 · 마이그레이션 초안 (FK 의존 순 · RLS 포함)

전제: `orgs`, `projects`, `plans`, `users`는 선행 의존성(본 세션 대상 아님)으로 존재 가정. 모든 org-스코프 테이블에 `org_id uuid NOT NULL` + RLS.

**RLS 공통 패턴** (모든 스코프 테이블에 적용)
```sql
ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;
ALTER TABLE <t> FORCE ROW LEVEL SECURITY;
CREATE POLICY <t>_isolation ON <t>
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);
```
앱은 non-superuser 롤로 접속(RLS 우회 불가). 요청 진입 시 `tenancy` 미들웨어가 트랜잭션 시작 직후 `SET LOCAL app.current_org = <uuid>` 실행.

### 마이그레이션 순서

**001_verified_domains** (C1,C2) — projects(가정) FK
```
verified_domains(
  id uuid PK, org_id uuid NOT NULL, project_id uuid NOT NULL REFERENCES projects,
  domain text NOT NULL, method text NOT NULL CHECK (method IN ('dns_txt','file')),
  token text NOT NULL, status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','verified','failed')),
  verified_at timestamptz, created_at timestamptz DEFAULT now())
UNIQUE(project_id, domain)          -- C1 재등록 409
INDEX(org_id, project_id)
+ RLS
```

**002_endpoints** (C3,C4) — verified_domains FK
```
endpoints(
  id uuid PK, org_id uuid NOT NULL,
  domain_id uuid NOT NULL REFERENCES verified_domains ON DELETE CASCADE,
  name text, method text NOT NULL
    CHECK (method IN ('GET','POST','PUT','PATCH','DELETE','HEAD','OPTIONS')),
  path text NOT NULL, query jsonb, headers jsonb, body_template jsonb,
  expected_status int, default_weight int NOT NULL DEFAULT 1 CHECK (default_weight >= 1),
  tags text[], created_at timestamptz DEFAULT now())
UNIQUE(domain_id, method, path)     -- C3 중복 409
INDEX(org_id, domain_id)
+ RLS
```

**003_load_tests** (C5,C6,C9) — verified_domains FK
```
load_tests(
  id uuid PK, org_id uuid NOT NULL, project_id uuid NOT NULL REFERENCES projects,
  domain_id uuid NOT NULL REFERENCES verified_domains,
  target_url text NOT NULL,          -- domain에서 파생(스킴 https 기본) — 모호#6 해소
  scenario jsonb NOT NULL,           -- {mode,vu,duration_sec,ramp_up,thresholds,apis[]}
  vu int NOT NULL, duration_sec int NOT NULL, region text,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN
    ('pending','validating','queued','provisioning','running',
     'aggregating','completed','rejected','failed','aborted')),  -- 10개
  aborted_reason text, created_by uuid,
  started_at timestamptz, finished_at timestamptz, created_at timestamptz DEFAULT now())
INDEX(org_id, project_id, status)
+ RLS
```
`target_url` = `https://<domain>` (스킴 https 고정, 클라이언트는 domain_id만 전송) — 모호#6 확정.

**004_load_test_results** (C11,C12) — load_tests + endpoints FK
```
load_test_results(
  id uuid PK, org_id uuid NOT NULL,
  load_test_id uuid NOT NULL REFERENCES load_tests ON DELETE CASCADE,
  endpoint_id uuid REFERENCES endpoints,   -- NULL = 전체 집계 행
  rps_avg numeric, latency_p50 numeric, latency_p95 numeric, latency_p99 numeric,
  error_rate numeric, max_vu_before_degradation int,
  bottleneck_endpoint uuid,                -- 전체행에만 채움
  metrics_ref jsonb,                       -- TSDB 쿼리 키(원본 미저장 불변식)
  created_at timestamptz DEFAULT now())
UNIQUE(load_test_id, endpoint_id)          -- endpoint별 1행 + NULL 전체행
INDEX(org_id, load_test_id)
+ RLS
```
`max_vu_before_degradation`은 S1 워커가 집계 시 휴리스틱(에러율/p95 급증 지점)으로 산출, AI 근거는 S4 위임(모호#8 → S1 휴리스틱 값 저장).

**005_usage_records** (C13) — load_tests FK
```
usage_records(
  id uuid PK, org_id uuid NOT NULL,
  load_test_id uuid NOT NULL REFERENCES load_tests,
  vu_minutes numeric NOT NULL,
  overage_vu_minutes numeric,      -- NULL(Billing 책임)
  amount_cents bigint,             -- NULL(Billing 책임)
  computation jsonb,               -- 산정 근거
  created_at timestamptz DEFAULT now())
INDEX(org_id, load_test_id)
+ RLS
```

**006_audit_logs** (불변식) — 잡 create/abort 등 기록
```
audit_logs(
  id uuid PK, org_id uuid NOT NULL, actor_id uuid,
  action text NOT NULL,            -- 'load_test.create'|'load_test.abort'|'domain.verify'...
  target_type text, target_id uuid, metadata jsonb,
  created_at timestamptz DEFAULT now())
INDEX(org_id, created_at)
+ RLS
```

---

## 서비스 인터페이스

### REST (클라이언트 ↔ CP) — Echo
공통 에러 규약(httpx): `{code, message, details}` — `CONFLICT`(409) / `VALIDATION_ERROR`(422) / `DOMAIN_NOT_VERIFIED`(403) / `QUOTA_EXCEEDED`(402) / `FORBIDDEN`(403) / `NOT_FOUND`(404).

| 메서드 | 경로 | 계약 | 응답 |
|---|---|---|---|
| GET/POST | `/projects/:id/domains` | C1 | 201 `{id, domain, method, status:"pending", verification:{record_name,record_value}\|{file_path,file_content}}` |
| DELETE | `/projects/:id/domains/:domainId` | C1 | 204 |
| POST | `/projects/:id/domains/:domainId/verify` | C2 | 200 `{status:"verified", verified_at}` / 422 실패(pending 유지, failed 기록) |
| GET/POST | `/projects/:id/domains/:domainId/endpoints` | C3 | 201 `{id, method, path, ...}` |
| GET/PATCH/DELETE | `/endpoints/:id` | C3 | 200/204 (진행중 참조 시 DELETE 409) |
| POST | `/projects/:id/domains/:domainId/endpoints/import` | C4 | 200 `{imported, skipped_duplicates}` (`source: openapi\|har`) |
| GET/POST | `/projects/:id/load-tests` | C5 | 202 `{id, status:"validating"}` (비동기) |
| GET | `/load-tests/:id` | C6 | `{status, progress:{elapsed_sec,current_vu}, aborted_reason}` |
| POST | `/load-tests/:id/abort` | C9 | 200 (종료된 잡 409) |
| GET | `/load-tests/:id/results` | C11 | `{summary{rps_avg,latency{p50,p95,p99},error_rate,max_vu_before_degradation,bottleneck_endpoint}, per_api[]}` |
| GET | `/load-tests/:id/metrics` | C12 | 시계열 포인트(TSDB `metrics_ref` 조회) |

POST `/load-tests` 요청 바디: `{domain_id, scenario:{mode:"weighted"|"journey", vu, duration_sec, ramp_up?, thresholds?, apis:[{endpoint_id, weight}]}, region?}`. 게이트 순서: 도메인 verified → 모든 endpoint가 domain_id 소속 → plan max_vu 검사.

### WebSocket (클라이언트 ↔ CP) — C10
- `WS /load-tests/:id/stream` — 접속 시 테넌트 스코프 인가(밖이면 close 4403).
- 주기 메시지(≤2초): `{ts, rps, latency_p95_ms, error_rate, active_vu}`
- abort 이벤트(1회 후 종료): `{event:"aborted", reason, at}`

### gRPC (CP ↔ Worker) — mTLS 전 구간 · `proto/worker/v1`
```proto
service WorkerControl {
  rpc DispatchJob(DispatchRequest) returns (DispatchAck);      // CP→Worker: 잡 시작
  rpc AbortJob(AbortRequest) returns (AbortAck);               // CP→Worker: 즉시 중단
  rpc StreamMetrics(stream MetricBatch) returns (StreamAck);   // Worker→CP: ≤2초 배치
  rpc Heartbeat(stream Ping) returns (stream Pong);            // 양방향 생존 확인
}
message DispatchRequest { string load_test_id; string target_url; Scenario scenario;
                          repeated EndpointDef endpoints; }   // scenario=선언형, 워커가 k6gen
message MetricBatch { string load_test_id; int64 ts;
  repeated ApiMetric per_api;                                  // endpoint_id 태깅
  double rps; double latency_p95_ms; double error_rate; int32 active_vu;
  CircuitState circuit; }                                      // 서킷 상태 동봉
message CircuitState { bool tripped; string reason; }          // C8: 트립 시 CP가 AbortJob 브로드캐스트
```
스트림 단절(Heartbeat 미수신) → CP가 잡을 ABORTED 안전종료 + Cleanup.

### 큐 (NATS JetStream) — 비동기 이벤트
| Subject | 발행자 | 구독자 | payload |
|---|---|---|---|
| `klaro.jobs.lifecycle` | loadtest.service | metrics/audit | `{load_test_id, org_id, status, at}` |
| `klaro.jobs.abort` | circuit/loadtest | 워커 팬아웃 게이트 | `{load_test_id, reason, at}` |
| `klaro.usage.emitted` | billing.emitter | (Billing 서비스, S1 외) | `{org_id, load_test_id, vu_minutes, computation}` |
| `klaro.notify` | Notifier | mailhog/slack 스텁 | `{event, load_test_id, org_id, reason, at, dashboard_url}` |

---

## 빌드 순서

의존 그래프 기반. 각 단계는 다음 단계의 선행.

1. **platform 기반** — `config`, `db`(pgx + RLS 세션 주입), `httpx`(에러 규약), `mtls`. 마이그레이션 001~006 실행 파이프라인. → 이후 전 모듈의 토대.
2. **tenancy + RLS 가드** — org_id 해석 미들웨어 + `SET LOCAL app.current_org`. RLS 격리 통합 테스트(크로스테넌트 차단 검증).
3. **domainverify (C1,C2)** — 등록/토큰/검증 게이트. Prober 인터페이스(dns_txt/file). → 잡 생성 선행 게이트.
4. **catalog (C3,C4)** — endpoints CRUD + import(openapi/har). domainverify 의존.
5. **orchestrator 포트 + DockerOrchestrator** — Spawn/Abort/Cleanup/List. k8s 스텁. → 워커 실행 토대.
6. **proto/worker/v1 + worker 바이너리 + k6gen** — gRPC 계약 생성, k6 렌더러, k6 실행. mTLS.
7. **loadtest 코어 (C5,C6,C9)** — statemachine, scenario 검증, 생성 진입 게이트, abort. eventbus 발행. orchestrator·catalog·domainverify 의존.
8. **metrics (C7,C10) + circuit (C8)** — gRPC StreamMetrics 수신, aggregator, VictoriaMetrics write, WS hub fan-out, 서킷 트립→abort 브로드캐스트.
9. **results (C11,C12)** — 요약 집계(per_api + 전체행), TSDB 시계열 조회.
10. **billing.emitter (C13)** — VU-Minutes 산정 + `klaro.usage.emitted` 발행(COMPLETED/ABORTED 훅).
11. **audit + Notifier 스텁** — 잡 이벤트 감사, MailHog/Slack 발화.
12. **deploy/docker-compose** — CP·postgres·nats·victoriametrics·mailhog + 워커 격리 네트워크 통합 기동 · E2E.

**병렬 가능**: 3·4(도메인·카탈로그)는 2 이후 병렬. 5·6(오케스트레이터·워커 계약)은 2 이후 병렬. 8·9는 7 이후 병렬.

---

## 미해결 · 확인 필요 (spec-analyst/PO 판단 필요)
- **[확인 필요] 큐 기술**: 과금 이벤트 장기 보존·리플레이 강제 시 Kafka 재검토(S1은 NATS 진행).
- **모호#2 권한**: 도메인 등록/검증 권한(member vs admin) — DDoS 게이트 성격상 admin 권장, 확인 필요.
- **모호#3 verify 실패**: 실패 시 `failed` 기록 후 재시도 시 `pending` 복귀로 확정(본 설계 채택), 정책 확인.
- **모호#7 쿼터 초과**: 402 항상 차단으로 확정(overage 진행은 Billing 범위 밖). PO 확인 필요.
- **모호#4 import 포맷**: HAR 필드 매핑·인라인 spec 상세는 catalog.importer 구현 단계에서 spec-analyst 보강 필요.
- **모호#5 journey 결과 구조**: journey 모드 per_api 매핑(스텝별) 근거 부족 — S1은 weighted 우선, journey는 스텝=엔드포인트 1:1로 임시 매핑.
