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
| APM | GET `/projects/:id/apm/agents` | 에이전트 목록 | member |
| APM | POST `/projects/:id/apm/agents` | 에이전트 등록(ingest token) | member |
| APM | GET `/projects/:id/apm/traces` | 트레이스 조회(느린 트랜잭션) | member |
| APM | GET `/projects/:id/apm/logs` | 로그 조회 | member |
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
