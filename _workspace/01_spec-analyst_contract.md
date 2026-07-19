# klaro S1 부하 테스트 — 백엔드 MVP 요구사항 계약

**작성** spec-analyst · **버전** v1 · **근거** docs/klaro/01, 02, 03 (보조: 04, 05)
**확정 스택(재논의 금지)** Control Plane = Go(Gin/Echo) · 로컬 오케스트레이션 = Docker Compose(워커 컨테이너 spawn, K8s Job은 인터페이스 추상화) · 테넌트 격리 = 공용 DB + RLS

---

## 범위

이번 세션 한정. **Control Plane(Go) S1 백엔드 + k6 Load Worker**.

포함:
- 도메인 등록/토큰 발급/소유권 검증 (선행 게이트)
- API 카탈로그(endpoints) CRUD + 일괄 import
- 부하 테스트 생성/상태조회/요약결과/시계열조회/중단
- 실시간 메트릭 WebSocket 스트림
- k6 워커: 다중 API 가중치 혼합 트래픽 실행, 잡 상태머신, 서킷 브레이커
- usage 이벤트(VU-Minutes) **발행 지점만** 표시

제외: 프론트엔드, S2(스캔)/S3(APM)/S4(리포트), Billing 상세 로직(Stripe 계량·초과 과금 계산·구독). Auth/Org/Project CRUD는 전제 의존성으로 가정하되 본 계약의 대상 아님.

ID 추적: `[LG-01]` 부하테스트 생명주기·실행, `[LG-02]` 실시간 메트릭 스트림, `[LG-03]` 서킷 브레이커, `[LG-04]` 다중 API 가중치 혼합, `[SC-01]` 도메인 소유권 게이트, `[CAT-01]` API 카탈로그, `[BILL-01]` VU-Minutes 계량, `[BILL-02]` 쿼터/초과.
> 주의: LG-01/LG-02는 docs에 명시 번호가 없어 본 계약이 부여한 추적 ID다(하단 모호/모순 참조).

---

## 요구사항 계약

### C1. 도메인 등록 + 검증 토큰 발급 [SC-01] [CAT-01의 전제]
- **설명**: 프로젝트에 테스트 대상 사이트(도메인)를 등록하면 소유권 검증용 토큰과 검증 방식(DNS TXT / 파일)을 발급한다. 등록 직후 상태는 `pending`.
- **수용 기준**:
  - `POST /projects/:id/domains` → 201, 응답에 `verification.record_name/record_value`(dns_txt) 또는 `file_path/file_content`(file) 포함, `status=pending`.
  - 동일 프로젝트에 동일 domain 재등록 시 409 `CONFLICT` (UNIQUE(project_id, domain)).
  - method는 `dns_txt` | `file`만 허용, 그 외 422 `VALIDATION_ERROR`.
- **데이터**: verified_domains(id, project_id, domain, method, token, status, verified_at)
- **API**: GET/POST `/projects/:id/domains`, DELETE `/projects/:id/domains/:domainId`

### C2. 도메인 소유권 검증 실행 (선행 게이트) [SC-01] · MUST
- **설명**: 발급 토큰을 대상 도메인에서 확인(DNS TXT `klaro-verify=<token>` 조회 또는 `https://<domain>/klaro-challenge.txt` == token)해 통과 시에만 `verified`로 전환. 부하 잡은 검증된 도메인에만 허용.
- **수용 기준**:
  - `POST /projects/:id/domains/:domainId/verify` 성공 → 200 `{status:"verified", verified_at}`.
  - 토큰 불일치/미발견 → 422 `VALIDATION_ERROR` 및 상태 `pending` 유지(또는 `failed` 기록).
  - 검증되지 않은 domain으로 부하 테스트 생성 시도 → 403 `DOMAIN_NOT_VERIFIED` (C6 참조).
- **데이터**: verified_domains(status, verified_at)
- **API**: POST `/projects/:id/domains/:domainId/verify`

### C3. API 카탈로그 CRUD [CAT-01]
- **설명**: 검증된(또는 등록된) 사이트별로 테스트 대상 API를 카탈로그로 등록·조회·수정·삭제. method/path/query/headers/body_template/expected_status/default_weight/tags 보유. 부하 테스트가 재사용.
- **수용 기준**:
  - `POST /projects/:id/domains/:domainId/endpoints` → 201, 응답에 endpoint id·method·path.
  - 동일 사이트 내 (method, path) 중복 → 409 `CONFLICT` (UNIQUE(domain_id, method, path)).
  - method는 GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS 열거값만 허용.
  - `DELETE /endpoints/:id` 대상이 **진행 중(pending~running)** 부하 테스트 scenario.apis[]에 참조되면 409.
- **데이터**: endpoints(id, domain_id, name, method, path, query, headers, body_template, expected_status, default_weight, tags)
- **API**: GET/POST `/projects/:id/domains/:domainId/endpoints`, GET/PATCH/DELETE `/endpoints/:id`

### C4. API 카탈로그 일괄 import [CAT-01]
- **설명**: OpenAPI 스펙 또는 브라우저 HAR로 다수 endpoint를 일괄 등록. 중복은 스킵.
- **수용 기준**:
  - `POST .../endpoints/import` → 200 `{imported:<n>, skipped_duplicates:<m>}`.
  - `source`는 `openapi`|`har`. 중복(method+path)은 skip 카운트에 반영, 실패시 부분 성공 허용.
- **데이터**: endpoints
- **API**: POST `/projects/:id/domains/:domainId/endpoints/import`
- **주의**: import 소스 포맷 상세(HAR 필드 매핑, spec_url vs 인라인)는 문서 근거 얕음 → 모호/모순 참조.

### C5. 부하 테스트 생성 — 다중 API 가중치 혼합 [LG-04] [LG-01] [CAT-01] [SC-01] · MUST
- **설명**: 검증된 사이트 1개에 등록된 여러 API를 하나의 시나리오에서 가중치(weight) 비율로 동시 실행. 총 VU/RPS를 API별 weight로 분배(예 60/30/10). 대안 mode `journey`(순차 여정 반복)는 선택형.
- **수용 기준**:
  - `POST /projects/:id/load-tests` → 202 `{id, status:"validating"}`(비동기).
  - 요청 domain_id 미검증 → 403 `DOMAIN_NOT_VERIFIED`.
  - scenario.apis[]의 endpoint 중 하나라도 해당 domain_id 소속이 아니면 → 422 `VALIDATION_ERROR` ("all endpoints must belong to domain_id").
  - scenario 필수 필드: `mode`(weighted|journey), `vu`, `duration_sec`, `apis:[{endpoint_id, weight}]`. weighted 모드에서 apis 비어있으면 422.
  - plan max_vu 초과 → 402 `QUOTA_EXCEEDED` (details.overage_billing 여부 포함) [BILL-02].
- **데이터**: load_tests(project_id, domain_id, target_url, scenario{mode,vu,duration,ramp_up,thresholds,apis[]}, vu, duration_sec, status, region, created_by)
- **API**: GET/POST `/projects/:id/load-tests`

### C6. 부하 테스트 상태머신 (잡 생명주기) [LG-01] · MUST
- **설명**: 잡은 정의된 상태 전이만 따른다: `PENDING → VALIDATING → QUEUED → PROVISIONING → RUNNING → AGGREGATING → COMPLETED`, 분기 `REJECTED`(검증/쿼터 실패), `FAILED`, `ABORTED`(서킷 브레이커/스트림 단절 안전종료). VALIDATING에서 도메인·쿼터 확인, PROVISIONING에서 워커(컨테이너/K8s Job) 준비.
- **수용 기준**:
  - `GET /load-tests/:id` → 현재 `status`, `progress{elapsed_sec, current_vu}`, `aborted_reason` 반환.
  - 정의되지 않은 상태 전이는 거부(불변식): 예 COMPLETED→RUNNING 불가.
  - 도메인 미검증/쿼터 실패는 REJECTED로 종결(RUNNING 진입 금지).
  - status 열거값은 데이터 모델의 10개(pending~rejected)와 일치.
- **데이터**: load_tests(status, started_at, finished_at, aborted_reason)
- **API**: GET `/load-tests/:id`

### C7. k6 워커 — 가중치 혼합 트래픽 실행 [LG-04] [LG-01] · MUST
- **설명**: RUNNING 진입 시 워커(로컬=Docker 컨테이너 spawn, 프로덕션=K8s Job — **인터페이스로 추상화**)를 생성해 k6 시나리오 실행. weight 기반 확률로 매 요청 대상 API 선택, endpoint 정의(headers/body/query)를 주입. 메트릭은 **API 단위 태깅**으로 집계. 잡 종료(정상/중단) 시 워커 즉시 회수(**idle 워커 0**).
- **수용 기준**:
  - 실행 트래픽이 지정 weight 비율에 근사(허용 오차 내)하게 API별로 분배됨(메트릭 태그로 검증 가능).
  - endpoint의 method/path/headers/body_template가 실제 요청에 반영.
  - 잡 종료 후 워커 리소스가 남지 않음(컨테이너/Job 삭제 확인). 오케스트레이터 호출은 spawn/abort/cleanup 인터페이스 뒤에 있어 Docker↔K8s 교체 가능.
- **데이터**: load_tests(scenario.apis[]), endpoints
- **API(내부)**: gRPC WorkerControl.DispatchJob / AbortJob / StreamMetrics / Heartbeat (전 구간 mTLS)

### C8. 서킷 브레이커 [LG-03] · MUST (Critical)
- **설명**: 워커 사이드카가 슬라이딩 윈도(예 10초)로 5xx 비율·에러율 계산. **에러율 > 80%** 또는 대상 `503` 지속 시 Control Plane에 abort 신호 → 전 워커 즉시 부하 중단. 상태 `ABORTED` 기록 + 사용자 알림(개발: MailHog / Slack 스텁) + **부분 결과 보존**.
- **수용 기준**:
  - 에러율 임계 초과 감지 시 잡이 ABORTED로 전이하고 `aborted_reason`에 사유 기록(예 "error_rate > 0.8").
  - abort 시 모든 활성 워커가 부하를 멈춤(idle=0 회수 포함).
  - 중단 시점까지 수집된 결과(load_test_results)는 삭제되지 않고 보존.
  - WS 스트림으로 `{event:"aborted", reason, at}` 이벤트 발행.
- **데이터**: load_tests(status=aborted, aborted_reason)
- **API**: WS `/load-tests/:id/stream`; 내부 gRPC AbortJob

### C9. 부하 테스트 강제 중단(사용자) [LG-01]
- **설명**: 사용자가 진행 중 테스트를 안전 종료.
- **수용 기준**:
  - `POST /load-tests/:id/abort` → 잡을 ABORTED로 전이, 워커 회수, 부분 결과 보존.
  - 이미 종료(completed/failed/aborted/rejected)된 잡에 대한 abort → 409 `CONFLICT`.
- **데이터**: load_tests(status, aborted_reason)
- **API**: POST `/load-tests/:id/abort`

### C10. 실시간 메트릭 WebSocket 스트림 [LG-02] · MUST
- **설명**: RUNNING 동안 워커 집계 메트릭을 Control Plane WS로 구독자에게 push(≤2초 주기). 서킷 브레이커 발동 이벤트도 동일 채널로 전달.
- **수용 기준**:
  - `WS /load-tests/:id/stream` 접속 시 주기 메시지 `{ts, rps, latency_p95_ms, error_rate, active_vu}` 수신(주기 ≤2초).
  - abort 발생 시 `{event:"aborted", reason, at}` 메시지 1회 전달 후 스트림 종료.
  - 테넌트 스코프 밖 load_test id 구독 시도 → 인가 거부(FORBIDDEN/NOT_FOUND).
- **데이터**: 시계열 원본은 TSDB, RDB 미저장(불변식). load_test_id로 상관.
- **API**: WS `/load-tests/:id/stream`

### C11. 요약 결과 조회 — API별 분해 [LG-04] [LG-01]
- **설명**: AGGREGATING 후 결과를 **API별 1행 + 전체 집계 1행**으로 저장하고 per_api 분해와 전체 요약을 반환. 병목 API 식별 포함.
- **수용 기준**:
  - `GET /load-tests/:id/results` → 전체 `rps_avg, latency{p50,p95,p99}, error_rate, max_vu_before_degradation, bottleneck_endpoint` + `per_api[]`(endpoint_id, method, path, weight, rps_avg, latency_p95_ms, error_rate).
  - load_test_results에 endpoint_id별 행 + endpoint_id NULL(전체 집계) 행이 존재.
  - 완료 전 조회 시 결과 미완 상태를 명확히(빈/부분 또는 상태 안내).
- **데이터**: load_test_results(load_test_id, endpoint_id[NULL=전체], rps_avg, latency_p50/p95/p99, error_rate, max_vu_before_degradation, bottleneck_endpoint, metrics_ref)
- **API**: GET `/load-tests/:id/results`

### C12. 시계열 메트릭 조회 [LG-02]
- **설명**: 완료/진행 테스트의 시계열 메트릭을 TSDB에서 조회(요약과 별개, RDB에는 참조 키만).
- **수용 기준**:
  - `GET /load-tests/:id/metrics` → 시계열 포인트 반환. 원본은 TSDB, RDB의 `metrics_ref` 키로 조회.
  - 시계열 데이터가 RDB(load_test_results 등)에 원본 저장되지 않음(불변식).
- **데이터**: load_test_results.metrics_ref (TSDB 쿼리 키)
- **API**: GET `/load-tests/:id/metrics`

### C13. VU-Minutes usage 이벤트 발행 지점 [BILL-01] [BILL-02] (발행만, 계산 상세 제외)
- **설명**: 잡 완료 흐름에서 usage 이벤트(VU-Minutes = VU × 분)를 발행하는 **훅 지점**을 명시. 초과분(overage)·금액 계산·Stripe 연동은 본 세션 제외.
- **수용 기준**:
  - 잡이 COMPLETED/ABORTED로 종결될 때 usage 이벤트가 발행되어야 함(발행 지점 존재).
  - 이벤트 payload에 org_id, load_test_id, vu_minutes 산정 근거 포함.
  - overage/amount 계산 로직은 Billing 서비스 책임(본 계약 미포함).
- **데이터**: usage_records(org_id, load_test_id, vu_minutes, overage_vu_minutes, amount_cents) — 계량 발행 지점만
- **API**: (참고) GET `/orgs/:orgId/usage` — 본 세션 미구현, 발행 지점만 표시

---

## 불변식 체크리스트 (MUST)

- **RLS 멀티테넌시**: verified_domains/endpoints/load_tests/load_test_results/usage_records 등 org_id 스코프 테이블 전체에 RLS 정책, 요청마다 `SET app.current_org = <uuid>`. 크로스테넌트 접근 불가. (02 §3, 01 §3)
- **도메인 소유권 게이트 [SC-01]**: 부하 잡은 `verified` 도메인에만 생성 허용. 미검증 → 403/REJECTED. (01 §2.3, 03 §3.2)
- **endpoint↔domain 소속 검증**: scenario.apis[]의 모든 endpoint가 domain_id 소속이어야 함. 위반 422.
- **잡 상태머신**: 정의된 전이만 허용. REJECTED/FAILED/ABORTED 분기 준수. (01 §2.1)
- **서킷 브레이커 [LG-03]**: 에러율>80%/503 지속 시 전 워커 즉시 중단 + ABORTED + 부분 결과 보존. (01 §2.3)
- **워커 idle = 0**: 잡 종료 시 컨테이너/K8s Job 즉시 회수(finalizer 보장). (01 §2.2)
- **시계열 RDB 분리**: 메트릭 원본은 TSDB, RDB에는 참조 키(metrics_ref)만. (02 서두, §2.5)
- **내부 mTLS**: Control Plane ↔ Worker gRPC 전 구간 mTLS. (01 §1, 03 §5)
- **오케스트레이션 추상화**: 워커 spawn/abort/cleanup을 인터페이스로 분리(Docker Compose ↔ K8s Job 교체 가능). (본 세션 확정 스택)
- **감사 로그**: load_test.create/abort 등 잡 이벤트 audit_logs 기록(SOC2 대비). (01 §4, 02 §2.8)

---

## architect 결정 필요 목록

확정 스택(Go, Docker Compose, RLS)은 제외. 아래는 S1 범위에 걸린 미확정 설계 결정.

1. **잡 큐 기술**: NATS vs Kafka (01 §1 "NATS/Kafka" 병기). 로컬 개발용 큐 선택 + gRPC 디스패치와의 역할 분담.
2. **로드 워커 시계열 TSDB**: VictoriaMetrics 확정 여부 및 metrics_ref 키 스키마. (01 다이어그램은 VictoriaMetrics, 02는 "별도 저장소"로 일반화)
3. **워커 메트릭 역류 경로**: gRPC StreamMetrics vs OTLP vs WS — 로드 워커 메트릭이 Control Plane WS까지 오는 경로 확정(01 §1은 "WS/OTLP" 병기).
4. **서킷 브레이커 파라미터 확정**: 슬라이딩 윈도 길이(문서 "예: 10초"), 에러율 임계(80% 확정), 503 "지속" 판정 기준.
5. **k6 시나리오 표현**: k6 JS 직접 vs 선언형 JSON/YAML→k6 변환 (01 §2.2 둘 다 언급). weighted↔journey 실행기 구현 방식.
6. **weight 분배 정밀도**: VU/RPS를 weight로 나눌 때 라운딩·최소 VU 보장 규칙(수용 오차 정의).
7. **VU-Minutes 산정 규칙**: 램프업 구간·중단(ABORTED) 시 부분 VU-Minutes 계산 방식(Billing과 경계).
8. **알림 채널 스텁 범위**: 개발 MailHog + Slack 스텁의 트리거·페이로드 최소 계약.
9. **워커 격리(로컬)**: K8s 네임스페이스/NetworkPolicy(01 §3) 대응하는 Docker Compose 격리 수준.

---

## 모호/모순

1. **LG-01 / LG-02 미정의**: 사용자 요청은 [LG-01~04]를 참조하나 docs에는 LG-03(서킷 브레이커)·LG-04(다중 API)만 명시 번호가 있다. LG-01(부하 생명주기·실행)·LG-02(실시간 메트릭)는 본 계약이 편의상 부여한 추적 ID이며, 정식 요구 번호 확정 필요.
2. **도메인 등록 권한**: 03 §2 표는 도메인 등록/검증을 `member` 권한으로 표기하나, [SC-01] DDoS 게이트 성격상 admin 이상 필요 여부는 미기재. (권한 정책 확인 필요)
3. **verify 실패 상태**: 데이터 모델 status에 `failed`가 있으나, 03 §3.1 예시는 실패 시 422만 반환하고 상태 전이를 명시하지 않음(pending 유지 vs failed 전환 불명확).
4. **import 포맷 상세**: 03 §3.6은 openapi spec_url 예시만 제시. HAR 소스의 필드 매핑·인라인 spec 지원·인증 필요 스펙 접근 방식은 근거 부족.
5. **journey 모드 결과 구조**: [LG-04] journey(순차 여정) 모드의 결과가 per_api[] 분해와 어떻게 매핑되는지(스텝별 vs 여정 전체) 문서 근거 없음.
6. **target_url vs domain**: load_tests에 domain_id와 target_url이 병존. target_url을 클라이언트가 보내는지 domain에서 파생하는지(스킴 결정 포함) 불명확 — 03 §3.2 요청 예시엔 target_url 없음(domain_id만).
7. **쿼터 초과 동작 분기**: 402 QUOTA_EXCEEDED와 "overage_billing 허용 시 진행"의 관계 — 초과를 허용하고 진행할지, 항상 차단할지 정책 결정 필요(05 §쿼터 초과는 "초과 과금/업그레이드 선택" 모달 언급).
8. **max_vu_before_degradation 산정 주체**: 결과 필드는 "AI 한계점 도출 근거"라 되어 있어 S4/AI 의존 가능성. S1 워커가 직접 산출하는지 리포트 단계 산출인지 경계 불명확.
