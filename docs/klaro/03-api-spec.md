# klaro — API 스펙 & 엔드포인트 목록 (초안)

**버전** v1.0.0-dev · **작성일** 2026-07-16 · **상태** 초안

관련 문서: [기술 설계서](./01-technical-design.md) · [데이터 모델](./02-data-model.md) · [비용 모델](./04-cost-model.md) · [UI/UX](./05-ui-ux-design.md)

---

## 1. 공통 규약

- **Base URL**: `https://api.klaro.io/v1`
- **포맷**: JSON. 요청/응답 `Content-Type: application/json`.
- **인증**:
  - 사용자 세션: `Authorization: Bearer <JWT>`
  - CI/CLI: `Authorization: Bearer <API_KEY>`
- **테넌트 스코프**: 모든 리소스는 조직에 귀속. 헤더 `X-Org-Id: <uuid>` 또는 경로로 스코프.
- **페이지네이션**: `?limit=&cursor=` (커서 기반), 응답 `{ data:[], next_cursor }`.
- **레이트리밋**: `429` + `Retry-After`.
- **에러 포맷**:
  ```json
  { "error": { "code": "DOMAIN_NOT_VERIFIED", "message": "...", "details": {} } }
  ```
- **표준 에러 코드**: `UNAUTHENTICATED`, `FORBIDDEN`, `NOT_FOUND`, `VALIDATION_ERROR`, `QUOTA_EXCEEDED`, `DOMAIN_NOT_VERIFIED`, `RATE_LIMITED`, `CONFLICT`, `INTERNAL`.

---

## 2. 엔드포인트 목록 (요약)

| 도메인 | 메서드 · 경로 | 설명 | 권한 |
|--------|--------------|------|------|
| Auth | POST `/auth/signup` | 회원가입 | public |
| Auth | POST `/auth/login` | 로그인(JWT 발급) | public |
| Auth | POST `/auth/refresh` | 토큰 갱신 | public |
| Auth | GET `/auth/oauth/:provider` | OAuth 시작(GitHub/Google) | public |
| Auth | POST `/auth/saml/acs` | SAML 콜백(M3) | public |
| Org | GET `/orgs` | 내 조직 목록 | member |
| Org | POST `/orgs` | 조직 생성 | user |
| Org | GET `/orgs/:orgId/members` | 멤버 목록 | member |
| Org | POST `/orgs/:orgId/members` | 멤버 초대 | admin |
| Org | PATCH `/orgs/:orgId/members/:userId` | 역할 변경 | admin |
| Org | DELETE `/orgs/:orgId/members/:userId` | 멤버 제거 | admin |
| API Key | GET `/orgs/:orgId/api-keys` | 키 목록 | admin |
| API Key | POST `/orgs/:orgId/api-keys` | 키 발급 | admin |
| API Key | DELETE `/orgs/:orgId/api-keys/:id` | 키 폐기 | admin |
| Project | GET `/projects` | 프로젝트 목록 | member |
| Project | POST `/projects` | 프로젝트 생성 | member |
| Project | GET `/projects/:id` | 상세 | member |
| Project | PATCH `/projects/:id` | 수정 | member |
| Project | DELETE `/projects/:id` | 삭제 | admin |
| Domain | GET `/projects/:id/domains` | 검증 도메인 목록 | member |
| Domain | POST `/projects/:id/domains` | 도메인 등록(토큰 발급) | member |
| Domain | POST `/projects/:id/domains/:domainId/verify` | 소유권 검증 실행 | member |
| Domain | DELETE `/projects/:id/domains/:domainId` | 도메인 제거 | member |
| Endpoint | GET `/projects/:id/domains/:domainId/endpoints` | 사이트 API 카탈로그 목록 | member |
| Endpoint | POST `/projects/:id/domains/:domainId/endpoints` | API 등록 | member |
| Endpoint | POST `/projects/:id/domains/:domainId/endpoints/import` | OpenAPI/HAR 일괄 등록 | member |
| Endpoint | GET `/endpoints/:id` | API 상세 | member |
| Endpoint | PATCH `/endpoints/:id` | API 수정 | member |
| Endpoint | DELETE `/endpoints/:id` | API 삭제 | member |
| Load Test | GET `/projects/:id/load-tests` | 부하 테스트 목록 | member |
| Load Test | POST `/projects/:id/load-tests` | 부하 테스트 생성/실행 | member |
| Load Test | GET `/load-tests/:id` | 상세/상태 | member |
| Load Test | POST `/load-tests/:id/abort` | 강제 종료 | member |
| Load Test | GET `/load-tests/:id/results` | 요약 결과 | member |
| Load Test | GET `/load-tests/:id/metrics` | 시계열 메트릭 조회 | member |
| Load Test | WS `/load-tests/:id/stream` | 실시간 메트릭 구독 | member |
| Scan | GET `/projects/:id/scans` | 스캔 목록 | member |
| Scan | POST `/projects/:id/scans` | 스캔 실행(sast/dast) | member |
| Scan | GET `/scans/:id` | 상세/상태 | member |
| Scan | GET `/scans/:id/findings` | findings 목록 | member |
| Scan | PATCH `/scans/:id/findings/:findingId` | 오탐 무시/상태 변경 | member |
| APM(MVP 스냅샷) | GET `/projects/:id/apm/agents` | 에이전트 목록 | member |
| APM(MVP 스냅샷) | POST `/projects/:id/apm/agents` | 에이전트 등록(ingest token) | member |
| APM(MVP 스냅샷) | GET `/projects/:id/apm/traces` | 트레이스 조회(느린 트랜잭션) | member |
| APM(MVP 스냅샷) | GET `/projects/:id/apm/logs` | 로그 조회 | member |
| 관측(상시) | POST/GET `/orgs/:orgId/obs/keys` · rotate · DELETE | 관측 API 키 발급/폐기/로테이션 | admin |
| 관측(상시) | GET `/orgs/:orgId/obs/quota` | 수집 쿼터 조회 | member |
| 관측(상시) | GET `/orgs/:orgId/obs/metrics/query` | 메트릭 Explorer | member |
| 관측(상시) | GET `/orgs/:orgId/obs/traces` · `/:traceId` | 트레이스 Explorer | member |
| 관측(상시) | GET `/orgs/:orgId/obs/logs` | 로그 Explorer | member |
| 관측(상시) | CRUD `/orgs/:orgId/obs/alert-rules` | 알림 룰 | member |
| 관측(상시) | GET `/orgs/:orgId/obs/alert-events` | 알림 이력 | member |
| 관측(상시) | CRUD `/orgs/:orgId/obs/dashboards` | 대시보드/패널 | member |
| 관측(상시) | WS `/orgs/:orgId/obs/live` | 라이브 스트림 구독 | member |
| Report | GET `/projects/:id/reports` | 리포트 목록 | member |
| Report | POST `/projects/:id/reports` | 리포트 생성 | member |
| Report | GET `/reports/:id` | 상세 | member |
| Report | GET `/reports/:id/pdf` | PDF 다운로드 | member |
| Report | POST `/reports/:id/share` | 공유 링크 생성 | member |
| Report | DELETE `/reports/:id/share/:shareId` | 공유 폐기 | member |
| Public | GET `/shared/:slug` | 공유 리포트 조회(비밀번호) | public |
| Billing | GET `/orgs/:orgId/subscription` | 구독 상태 | admin |
| Billing | POST `/orgs/:orgId/subscription` | 플랜 변경 | owner |
| Billing | GET `/orgs/:orgId/usage` | 사용량(VU-Minutes) | admin |
| Webhook | POST `/webhooks/github` | GitHub App 이벤트 | signed |
| Webhook | POST `/webhooks/stripe` | Stripe 이벤트 | signed |
| Health | GET `/healthz` · `/readyz` | 헬스체크 | public |

---

## 3. 핵심 엔드포인트 상세

### 3.1 도메인 검증 (DDoS 악용 차단 게이트)

**POST** `/projects/:id/domains` — 도메인 등록
```json
// req
{ "domain": "staging.example.com", "method": "dns_txt" }
// res 201
{
  "id": "uuid",
  "domain": "staging.example.com",
  "method": "dns_txt",
  "status": "pending",
  "verification": {
    "record_name": "_klaro.staging.example.com",
    "record_value": "klaro-verify=abc123",
    "file_path": "/klaro-challenge.txt",
    "file_content": "abc123"
  }
}
```

**POST** `/projects/:id/domains/:domainId/verify` — 검증 실행
```json
// res 200
{ "id": "uuid", "status": "verified", "verified_at": "2026-07-16T00:00:00Z" }
// res 422
{ "error": { "code": "VALIDATION_ERROR", "message": "TXT record not found" } }
```

### 3.2 부하 테스트 생성

**POST** `/projects/:id/load-tests`
```json
// req — 사이트(domain) 하나에 등록된 여러 API를 가중치 혼합 트래픽으로 동시 실행
{
  "domain_id": "uuid-site",                    // 테스트 대상 사이트(검증된 도메인)
  "scenario": {
    "mode": "weighted",                        // weighted(가중치 혼합) | journey(순차 여정)
    "vu": 1000,
    "duration_sec": 1800,
    "ramp_up_sec": 60,
    "apis": [                                   // 카탈로그(endpoints) 중 선택 + 트래픽 비중
      { "endpoint_id": "uuid-products", "weight": 60 },
      { "endpoint_id": "uuid-login",    "weight": 30 },
      { "endpoint_id": "uuid-health",   "weight": 10 }
    ],
    "thresholds": { "http_req_duration_p95_ms": 2000, "error_rate": 0.05 }
  },
  "region": "ap-northeast-2"
}
// res 202 (비동기)
{ "id": "uuid", "status": "validating" }
// res 403 (미검증 도메인)
{ "error": { "code": "DOMAIN_NOT_VERIFIED", "message": "target domain must be verified" } }
// res 422 (endpoint가 domain 소속이 아님)
{ "error": { "code": "VALIDATION_ERROR", "message": "all endpoints must belong to domain_id" } }
// res 402 (쿼터 초과)
{ "error": { "code": "QUOTA_EXCEEDED", "message": "plan limit: 1000 VU", "details": { "requested": 2000, "overage_billing": true } } }
```

**GET** `/load-tests/:id` — 상태 조회
```json
{
  "id": "uuid",
  "status": "running",
  "progress": { "elapsed_sec": 300, "current_vu": 1000 },
  "aborted_reason": null
}
```

**WS** `/load-tests/:id/stream` — 실시간 메트릭(≤2초 주기)
```json
// server → client
{ "ts": "2026-07-16T00:05:00Z", "rps": 4210, "latency_p95_ms": 1850, "error_rate": 0.02, "active_vu": 1000 }
// 서킷 브레이커 발동 시
{ "event": "aborted", "reason": "error_rate > 0.8", "at": "2026-07-16T00:06:00Z" }
```

**GET** `/load-tests/:id/results`
```json
{
  "rps_avg": 4200,
  "latency": { "p50": 320, "p95": 1850, "p99": 3100 },
  "error_rate": 0.02,
  "max_vu_before_degradation": 1250,
  "bottleneck_endpoint": "POST /api/v1/payment",
  "per_api": [
    { "endpoint_id": "uuid-products", "method": "GET",  "path": "/api/v1/products", "weight": 60, "rps_avg": 2520, "latency_p95_ms": 900,  "error_rate": 0.01 },
    { "endpoint_id": "uuid-login",    "method": "POST", "path": "/api/v1/login",    "weight": 30, "rps_avg": 1260, "latency_p95_ms": 1850, "error_rate": 0.03 },
    { "endpoint_id": "uuid-health",   "method": "GET",  "path": "/api/v1/health",   "weight": 10, "rps_avg": 420,  "latency_p95_ms": 120,  "error_rate": 0.00 }
  ]
}
```

### 3.3 보안 스캔

**POST** `/projects/:id/scans`
```json
// req (DAST)
{ "type": "dast", "target_url": "https://staging.example.com" }
// req (SAST via PR)
{ "type": "sast", "pr_number": 42 }
// res 202
{ "id": "uuid", "status": "pending" }
```

**PATCH** `/scans/:id/findings/:findingId` — 오탐 처리
```json
// req
{ "status": "ignored", "ignore_reason": "false positive - test fixture" }
// res 200
{ "id": "uuid", "status": "ignored" }
```

### 3.4 리포트 & 공유

**POST** `/reports/:id/share`
```json
// req
{ "password": "s3cret", "expires_in_days": 30 }
// res 201
{ "slug": "r_ab12cd", "url": "https://app.klaro.io/shared/r_ab12cd", "expires_at": "2026-08-15T00:00:00Z" }
```

**GET** `/shared/:slug` (public)
```
Header: X-Report-Password: s3cret
→ 200 리포트 뷰 / 401 잘못된 비밀번호 / 410 만료·폐기
```

### 3.5 사용량 (과금)

**GET** `/orgs/:orgId/usage`
```json
{
  "period": { "start": "2026-07-01", "end": "2026-07-31" },
  "plan": "pro",
  "included_vu_minutes": 30000,
  "used_vu_minutes": 42000,
  "overage_vu_minutes": 12000,
  "overage_amount_cents": 600,
  "breakdown": [ { "load_test_id": "uuid", "vu_minutes": 30000 } ]
}
```

### 3.6 API 카탈로그 (사이트별 여러 API)

한 사이트(검증된 도메인)에 테스트할 API를 등록해두고, 부하 테스트·스캔이 재사용한다([CAT-01]).

**POST** `/projects/:id/domains/:domainId/endpoints` — API 등록
```json
// req
{
  "name": "로그인",
  "method": "POST",
  "path": "/api/v1/login",
  "headers": { "Content-Type": "application/json" },
  "body_template": { "email": "{{email}}", "password": "{{password}}" },
  "expected_status": 200,
  "default_weight": 30,
  "tags": ["write", "auth"]
}
// res 201
{ "id": "uuid-login", "domain_id": "uuid-site", "method": "POST", "path": "/api/v1/login", "status": "active" }
// res 409 (동일 사이트에 method+path 중복)
{ "error": { "code": "CONFLICT", "message": "endpoint already exists for this domain" } }
```

**GET** `/projects/:id/domains/:domainId/endpoints` — 카탈로그 목록
```json
{
  "data": [
    { "id": "uuid-products", "name": "상품 목록", "method": "GET",  "path": "/api/v1/products", "default_weight": 60 },
    { "id": "uuid-login",    "name": "로그인",    "method": "POST", "path": "/api/v1/login",    "default_weight": 30 },
    { "id": "uuid-health",   "name": "헬스체크",  "method": "GET",  "path": "/api/v1/health",   "default_weight": 10 }
  ],
  "next_cursor": null
}
```

**POST** `/projects/:id/domains/:domainId/endpoints/import` — 일괄 등록(OpenAPI 스펙 또는 브라우저 HAR)
```json
// req
{ "source": "openapi", "spec_url": "https://staging.example.com/openapi.json" }
// res 200
{ "imported": 42, "skipped_duplicates": 3 }
```

**PATCH** `/endpoints/:id` — 수정 / **DELETE** `/endpoints/:id` — 삭제(진행 중 테스트가 참조하면 `409`).

### 3.7 상시 관측 플랫폼 API (2026-08-23 확정 — 상세 `_workspace/05_architect_observability-design.md`)

> **MVP와의 경계 [OBS-10]**: §2의 `GET/POST /projects/:id/apm/*`(project 단위, Postgres)는 **리포트용 스냅샷 조회로 존치**하고, 아래 org 단위 상시 API와 **공존**한다(대체 아님). 상시 API는 `services/observability/`(Go/Gin) 신규 서비스가 제공하며 모든 경로 org 스코프(RLS). 시계열 원본은 VictoriaMetrics/Tempo/Loki(각 백엔드 native 테넌트=org).

**수집(SDK↔Collector)**: OTLP over gRPC + **mTLS 필수**. 헤더 `klaro-obs-key: <org 키 시크릿>`. Collector가 키 검증·쿼터 집행·테넌트 라우팅.

**조회/관리 인증(사용자↔CP)**: `Authorization: Bearer <JWT>`. 서명 알고리즘은 **배포 설정값 하나**(HS256 또는 RS256)로만 검증한다 — 토큰이 선언한 `alg`를 따르지 않는다. 필수 클레임 `org_id`(uuid) · `role` · `exp`.

- **스코프는 클레임이 정한다.** §1의 `X-Org-Id` 헤더는 이 평면에서 스코프 선택에 쓰이지 않는다. 경로의 `:orgId`는 클레임의 org를 **지목**만 할 수 있고, 다르면 `403 FORBIDDEN`(SQL 이전). 그 org가 곧 RLS 세션 스코프(`app.current_org`)다.
- **역할별 인가**(`owner/admin/member/viewer`):

| 게이트 | 최소 역할 | 대상 |
|--------|----------|------|
| 조회 | `member` | `GET /obs/tenant` · `GET /obs/keys` · `GET /obs/quota` · Explorer(`metrics/query`·`traces`·`traces/:traceId`·`traces/:traceId/correlated`·`logs`) · `GET /obs/alert-rules[/:ruleId]` · `GET /obs/alert-events` · `GET /obs/dashboards[/:dashId]` · `GET /obs/live`(WS) |
| 변경 | `admin` | `POST/DELETE /obs/keys[/:keyId[/rotate]]` · `POST/PATCH/DELETE /obs/alert-rules[/:ruleId]` · `POST/PATCH/DELETE /obs/dashboards[/:dashId]` |

조회 하한이 `viewer`가 아니라 `member`인 것은 의도다: 이 평면은 테넌트의 관측 이력 전체를 노출하므로 열람이 최저 권한일 수 없다. `viewer`는 klaro 전체 RBAC의 역할이고 상시 관측 평면에서는 아직 부여가 없다.

`GET /obs/live`(WebSocket)는 같은 `member+` 문턱이지만 거절을 close code로 알린다(`4403` 스코프 불일치·역할 부족, `4400` 잘못된 stream) — 브라우저 WebSocket API가 핸드셰이크 상태를 노출하지 않기 때문이다(설계 §4.3).

**내부 평면(`/internal/*`, Collector·vmalert 전용)**: 별도 리스너. 전송은 mTLS이고, **인증은 전송과 분리**되어 검증된 클라이언트 인증서 **또는** 내부 공유 토큰(`Authorization: Bearer`)을 요구한다. 개발용 평문 전송을 켜도 인증은 유지된다. 거절 401에는 `WWW-Authenticate: Bearer realm="klaro-internal"`이 붙어, 수집 키 거절과 게이트웨이 자신의 거절을 구분한다.

**POST** `/orgs/:orgId/obs/keys` — 관측 API 키 발급 [OBS-02]
```json
// req
{ "name": "prod-cluster", "scope_label": { "env": "prod" } }
// res 201 (secret은 1회만 노출)
{ "id": "uuid", "name": "prod-cluster", "key_prefix": "obsk_ab12", "secret": "obsk_ab12cd…full", "status": "active" }
```
**POST** `/orgs/:orgId/obs/keys/:keyId/rotate` → `{ "id", "key_prefix", "secret", "grace_until" }` (구키는 grace 후 거부)
**DELETE** `/orgs/:orgId/obs/keys/:keyId` → 204 (이후 구키 수집 401)

**GET** `/orgs/:orgId/obs/quota` — 수집 쿼터 [OBS-02]
```json
{ "active_hosts": 12, "host_limit": 50, "ingest_gb": 3.4, "ingest_limit_gb": 100, "period": { "start": "2026-08-01", "end": "2026-08-31" } }
```

**GET** `/orgs/:orgId/obs/metrics/query` — 메트릭 Explorer [OBS-03] · `?from&to&filter&agg&step`
```json
{ "series": [ { "labels": { "service": "api", "endpoint": "/orders" }, "points": [ [1690000000, 12.5], [1690000060, 13.1] ] } ] }
```

**GET** `/orgs/:orgId/obs/traces` — 트레이스 Explorer [OBS-04] · `?from&to&service&min_duration_ms=3000`
```json
{ "data": [ { "trace_id": "abc", "root_service": "api-gateway", "duration_ms": 3480, "start": "2026-08-23T00:00:00Z" } ] }
```
**GET** `/orgs/:orgId/obs/traces/:traceId` → span 워터폴
```json
{ "trace_id": "abc", "spans": [ { "span_id": "s1", "parent_span_id": null, "service": "api-gateway", "name": "GET /orders", "duration_ms": 3480, "status": "ok" }, { "span_id": "s2", "parent_span_id": "s1", "service": "orders-svc", "name": "SELECT …", "duration_ms": 3200, "status": "ok" } ] }
```

**GET** `/orgs/:orgId/obs/traces/:traceId/correlated` — 크로스시그널 연계분석 [OBS-03/04/05, APM-03]
`?pad_sec&log_limit&step&metric`(metric 반복 가능, 생략 시 서버 기본 세트)

하나의 트레이스를 **그 안에서 쓰인 로그**와 **그 서비스·호스트의 메트릭**에 조인해 한 번에 반환한다. 조인 키는 방향마다 다르고, 그 차이를 응답이 숨기지 않는다:

- **트레이스→로그는 정확**하다. `trace_id`(+`span_id`)가 로그 레코드에 실려 있고(상관키 규약 `sdk/SDK_CONTRACT.md §12`), 서버가 `{klaro_org_id="<org>"} | trace_id = "<hex>"` 형태의 LogQL을 만든다. `trace_id`는 **스트림 라벨이 아니라 structured metadata의 label filter**다 — 요청마다 값이 달라 라벨로 색인하면 요청당 스트림 하나가 만들어진다.
- **트레이스→메트릭은 근사**다. 메트릭 시리즈에는 trace_id가 존재할 수 없으므로(카운터는 요청 단위가 아니다) `service_name`+`service_instance_id`로만 조인한다. 어느 범위에서 온 시리즈인지 `scopes`/`metrics[].service|host`로 드러내, 근사임이 보이게 한다.

**org 강제**: 트레이스를 먼저 읽고(Tempo `X-Scope-OrgID`) 그 결과로 창(window)과 범위를 만든다. 다른 org의 trace_id는 이 시점에서 404이고, 로그·메트릭 질의는 아예 만들어지지 않는다. 세 백엔드 모두 매퍼가 준 테넌트(VM AccountID 경로 세그먼트, Tempo/Loki 헤더) + 주입된 `klaro_org_id` 매처를 쓴다.

**부분 실패는 부분 응답**: 트레이스 읽기 실패만 치명적(404/502)이고, 로그·메트릭은 best effort다. 읽지 못한 신호는 빈 배열 + `notes` 항목으로 온다 — 빈 배열만 주면 "조용했다"로 읽히기 때문이다. `notes`는 업스트림 응답 본문을 절대 그대로 담지 않는다(다른 테넌트의 라벨·내부 호스트명이 섞일 수 있다).

**한도**: `pad_sec` ≤ 900(기본 30), 메트릭 팬아웃은 scope 4개 × 질의 12개까지이며 잘렸으면 `notes`에 적는다.

```json
{
  "trace_id": "5b8efff798038103d269b633813fc60c",
  "spans": [ { "span_id": "aaaa", "parent_span_id": null, "service": "web-bff", "host": "pod:aaaa1111", "start": 1756000000000, "duration_ms": 4000, "status": "unset" } ],
  "from": "2026-08-23T23:59:30Z",
  "to": "2026-08-24T00:00:34Z",
  "scopes": [ { "service": "web-bff", "host": "pod:aaaa1111", "spans": 1, "duration_ms": 4000, "errors": 0 } ],
  "logs": [ { "ts": 1756000003000, "level": "ERROR", "message": "psp timeout after 3000ms", "trace_id": "5b8e…", "span_id": "bbbb", "labels": { "service_name": "payments" } } ],
  "logs_query": "{klaro_org_id=\"…\"} | trace_id = \"5b8e…\"",
  "metrics": [ { "key": "process_cpu_utilization@web-bff/pod:aaaa1111", "metric": "process_cpu_utilization", "service": "web-bff", "host": "pod:aaaa1111", "series": [ { "labels": { "service_name": "web-bff" }, "points": [ [1756000000000, 0.81] ] } ], "resolution": "raw", "query": "process_cpu_utilization{klaro_org_id=\"…\",service_instance_id=\"pod:aaaa1111\",service_name=\"web-bff\"}" } ],
  "notes": []
}
```

**GET** `/orgs/:orgId/obs/logs` — 로그 Explorer [OBS-05] · `?from&to&filter&query&limit`
```json
{ "data": [ { "ts": "2026-08-23T00:00:00Z", "level": "error", "message": "upstream timeout", "labels": { "service": "api" } } ], "next": "cursor" }
```

**POST/GET/PATCH/DELETE** `/orgs/:orgId/obs/alert-rules` — 알림 룰 CRUD [OBS-06]
```json
// req
{ "name": "high-error-rate", "signal": "metric", "query": "rate(http_errors[5m])", "comparator": "gt", "threshold": 0.05, "for_duration_sec": 120, "severity": "critical", "channels": [ { "type": "slack", "target": "#alerts" } ], "enabled": true }
// res 201 (미지원 signal/comparator → 422)
{ "id": "uuid", "name": "high-error-rate", "signal": "metric", "enabled": true }
```

**GET** `/orgs/:orgId/obs/alert-events` — 알림 이력 [OBS-07] · `?state&from&to`
```json
{ "data": [ { "rule_id": "uuid", "state": "firing", "value": 0.08, "started_at": "2026-08-23T00:05:00Z", "resolved_at": null } ] }
```

**CRUD** `/orgs/:orgId/obs/dashboards` — 대시보드/패널 [OBS-09] (마일스톤 이연 후보)
```json
{ "id": "uuid", "name": "API Overview", "spec": { "panels": [ { "title": "RPS", "viz": "line", "query": { "signal": "metric", "expr": "rate(http_reqs[1m])" }, "layout": { "x": 0, "y": 0, "w": 6, "h": 4 } } ] } }
```

**WS** `/orgs/:orgId/obs/live?stream=<metric|service>` — 라이브 구독 [OBS-01/APM-02], ≤2초 주기
```json
{ "ts": "2026-08-23T00:00:00Z", "stream": "metric", "points": [ { "labels": { "service": "api" }, "value": 42.0 } ] }
```

**자격증명 전달(2026-08-23 확정)**: 이 라우트는 `Authorization: Bearer` 헤더 **또는**
핸드셰이크의 서브프로토콜 목록에서 토큰을 읽는다. 브라우저 WebSocket API가 핸드셰이크에
헤더를 붙일 수 없기 때문이고, 확장은 이 라우트에만 열려 있다(다른 라우트는 헤더만).

```
Sec-WebSocket-Protocol: klaro-bearer, <token>
```

- 서버는 선택한 서브프로토콜(`klaro-bearer`)을 **응답에 에코**한다(RFC 6455 §4.2.2).
  에코하지 않으면 브라우저가 인증에 성공한 연결을 스스로 끊는다. 토큰은 에코하지 않는다.
- 헤더가 있으면 헤더가 이긴다. 마커만 있고 토큰이 없으면 자격증명 없음(401)이다.
- **쿼리 파라미터(`?access_token=`)는 지원하지 않는다.** URL에 실린 토큰은 프록시 액세스
  로그·브라우저 히스토리·Referer에 남고, 이 토큰 하나가 org 하나다.

**교차 출처**: 공개 평면은 개발 프로파일에서만 CORS 허용 목록을 갖는다
(`OBS_DEV_CORS_ORIGINS`, 예: `http://localhost:3100`). 프리플라이트는 인증 앞에서
답하고(규격상 자격증명이 없다), `Access-Control-Allow-Credentials`는 보내지 않는다 —
자격증명은 페이지가 직접 붙이는 Bearer 토큰이므로 브라우저가 자동으로 실을 것이 없다.
프로덕션 프로파일은 이 설정을 거부한다(동일 오리진 서빙 또는 게이트웨이가 정책 소유).

---

## 4. 웹훅

**POST** `/webhooks/github` — `pull_request` 이벤트 → SAST 스캔 트리거. 서명(`X-Hub-Signature-256`) 검증.

**POST** `/webhooks/stripe` — `invoice.*`, `customer.subscription.*` → 구독 상태 동기화. 서명(`Stripe-Signature`) 검증.

---

## 5. gRPC (내부, Control Plane ↔ Worker)

Protobuf 서비스(요약):
```proto
service WorkerControl {
  rpc DispatchJob(JobSpec) returns (JobAck);
  rpc StreamMetrics(stream MetricPoint) returns (Ack);   // 워커 → CP
  rpc AbortJob(AbortRequest) returns (Ack);               // CP → 워커(서킷 브레이커)
  rpc Heartbeat(stream WorkerStatus) returns (stream Command);
}
```
전 구간 **mTLS**.

> **관측 수집 경로(내부)**: 상시 관측은 위 WorkerControl과 별개로 SDK→OTel Collector(OTLP/gRPC, mTLS)→VM/Tempo/Loki 파이프라인을 사용한다. Collector↔CP 내부 인터페이스(`/internal/authz/ingest-key`, `/internal/live-ingest`, `/internal/alerts/webhook`)도 mTLS. 상세 `_workspace/05_architect_observability-design.md §4`.
