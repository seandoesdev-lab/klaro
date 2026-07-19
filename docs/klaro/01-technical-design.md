# klaro — 상세 기술 설계서 (Technical Design Document)

**버전** v1.0.0-dev · **작성일** 2026-07-16 · **상태** 아키텍처 리뷰용

관련 문서: [기술 스택](./00-tech-stack.md) · [데이터 모델](./02-data-model.md) · [API 스펙](./03-api-spec.md) · [비용 모델](./04-cost-model.md) · [UI/UX](./05-ui-ux-design.md)

---

## 1. 아키텍처 개요

```
┌─────────────────────────────────────────────────────────────┐
│ Clients: Web(Next.js) · Chrome Ext · CLI · GitHub App        │
└───────────────┬─────────────────────────────────────────────┘
                │ REST(JSON) + WebSocket(stream)
                ▼
        ┌──────────────────────┐        ┌──────────────┐
        │   Control Plane API   │◀──────▶│    Redis     │ (세션/쿼터/잡캐시)
        │  auth·tenant·orchestr │        └──────────────┘
        └───┬───────────┬───────┘
   gRPC/Proto│           │ publish (job/billing)
            ▼            ▼
   ┌──────────────┐  ┌──────────────────┐
   │  Job Queue   │  │   PostgreSQL     │ (RLS, 메타/과금/요약)
   │ (NATS/Kafka) │  └──────────────────┘
   └──┬────────┬──┘
      │        │ consume
      ▼        ▼
┌───────────┐ ┌────────────────┐   ┌──────────────────────────┐
│Load Worker│ │  Scan Worker    │   │  APM Collector (OTel)    │
│  (k6/K8s) │ │ (Ephemeral/K8s) │   └───────────┬──────────────┘
└─────┬─────┘ └───────┬─────────┘               │ OTLP/mTLS
      │ HTTP 부하       │ SAST/DAST      ┌────────┼──────────┬─────────┐
      ▼                ▼                ▼        ▼          ▼         ▼
 [고객 Staging]───APM SDK──────▶  VictoriaMetrics  Tempo    Loki   MinIO
                                   (metrics)      (trace)  (logs) (archive)
                                        │
                                        ▼
                              ┌──────────────────┐
                              │ Report Aggregator│──▶ AI 요약(Bedrock) + PDF ──▶ MinIO
                              └──────────────────┘
```

### 통신 규약
- **클라이언트 ↔ Control Plane**: REST/JSON(제어), WebSocket(실시간 메트릭 구독).
- **Control Plane ↔ Worker**: 잡 디스패치는 큐, 제어/하트비트는 gRPC, 메트릭 역류는 WS/OTLP.
- **APM SDK ↔ Collector**: OTLP over gRPC, **mTLS 필수**.

---

## 2. 서비스별 상세 설계

### 2.1 Control Plane

책임: 인증·인가, 테넌시, 잡 생명주기 오케스트레이션, 쿼터/과금 이벤트 발행.

**잡 생명주기 상태 머신**
```
PENDING → VALIDATING → QUEUED → PROVISIONING → RUNNING → AGGREGATING → COMPLETED
                │                    │             │
                ▼                    ▼             ▼
             REJECTED            FAILED       ABORTED(안전종료)
```
- `VALIDATING`: 도메인 소유권 검증, 플랜 쿼터 확인.
- `PROVISIONING`: K8s Job 생성, 워커 준비 대기.
- `RUNNING`: 실시간 메트릭 스트림, 서킷 브레이커 감시.
- `AGGREGATING`: 결과 조인 + AI 리포트 생성.
- `ABORTED`: 서킷 브레이커/스트림 단절로 안전 종료.

**인가 모델(RBAC)**: `Org > Project > Resource`. 역할: `owner / admin / member / viewer`. 모든 리소스 접근은 테넌트 컨텍스트(`org_id`)로 스코프.

### 2.2 Load Generator (S1)

**동적 스케일아웃**
1. `RUNNING` 진입 시 Control Plane이 K8s Job 생성(VU 규모 → Pod 수 산정).
2. 각 워커는 k6 시나리오 슬라이스를 실행, 출력 메트릭을 집계기로 스트림.
3. 잡 종료(정상/중단) → Job/Pod 즉시 삭제(finalizer로 회수 보장). **목표: idle 워커 0.**

**서킷 브레이커 (Critical, [LG-03])**
- 워커 사이드카가 슬라이딩 윈도(예: 10초)로 5xx 비율·에러율 계산.
- **에러율 > 80%** 또는 대상 `503` 지속 → Control Plane에 `abort` 신호 → 전 워커 즉시 부하 중단.
- 상태 `ABORTED` 기록 + 사용자 알림(이메일=개발 MailHog / Slack 스텁) + 부분 결과 보존.

**다중 API — 가중치 혼합 트래픽 ([LG-04])**
- 한 사이트(`verified_domain`)에 등록된 여러 API(`endpoints` 카탈로그)를 **하나의 부하 테스트에서 동시** 실행.
- 총 VU/RPS를 API별 `weight` 비율로 분배(예: 60/30/10) → 실제 운영 트래픽 믹스 재현.
- k6 executor가 weight 기반 확률로 매 요청의 대상 API를 선택. 헤더·바디·쿼리는 endpoint 정의에서 주입.
- 메트릭은 **API 단위로 태깅** 집계 → 결과·리포트에서 **API별 분해**(`load_test_results`에 API별 1행 + 전체 집계 1행). 병목 API를 개별 식별.
- 대안 모드 `journey`: 로그인→조회→결제처럼 API를 **순차**로 밟는 사용자 시나리오 1회를 반복(선택형).

**시나리오 포맷**: k6 JS 또는 선언형 JSON/YAML(내부 변환). 필드: 대상 사이트(domain), VU, duration, ramp-up, thresholds, **apis[]**(카탈로그 endpoint 참조 + weight).

### 2.3 Security Scanner (S2)

**도메인 소유권 검증 ([SC-01], DDoS 악용 차단 — 선행 게이트)**
- 방식 A: DNS TXT 레코드 `klaro-verify=<token>` 조회.
- 방식 B: `https://<domain>/klaro-challenge.txt` = `<token>`.
- 검증 통과 도메인만 `verified_domains`에 등록. **DAST·부하 잡은 검증된 도메인에만 생성 허용**(미검증 시 `REJECTED`).

**SAST 파이프라인**
- GitHub App 웹훅(PR opened/synced) → Ephemeral 스캔 Job.
- 소스 체크아웃은 **tmpfs(RAM)**, Semgrep + osv-scanner 실행 → 결과 저장 → **Pod 소멸 시 소스 완전 삭제**(디스크 미기록, [보안 요구]).
- 결과를 PR 코멘트로 게시(취약점 수·심각도).

**DAST 파이프라인**: OWASP ZAP headless로 검증 URL 액티브 스캔 → 취약점 리포트.

**오탐 관리 ([SC-04])**: 항목별 `ignored` + 사유. 재스캔 시 finding 해시로 매칭해 상태 유지.

### 2.4 APM / Observability (S3)

**저오버헤드 목표 ([APM-01], ≤2%)**
- OTel 자동계측 + 샘플링(트레이스 tail-based), 배치 익스포트로 오버헤드 최소화.
- SDK는 비동기 큐로 전송, 대상 앱 요청 경로 블로킹 금지.

**데이터 파이프라인**
- SDK → OTel Collector(mTLS) → 메트릭(VictoriaMetrics) / 트레이스(Tempo) / 로그(Loki).
- **near-realtime([APM-02], ≤2초)**: Collector가 스트림을 Control Plane WS로 push → 대시보드 구독.

**병목 트레이스 ([APM-03])**: 응답 ≥3초 트랜잭션을 span 트리로 표시, 느린 DB 쿼리/함수 하이라이트.

### 2.5 Report Aggregator (S4)

- 잡 `AGGREGATING` 이벤트 → 배치 워커가 부하 요약 + 스캔 findings + APM 메트릭 조인.
- 종합 점수 산출(Performance/Security 가중 규칙 — 별도 스펙).
- **AI 요약**: 구조화 JSON(한계 VU, 병목 엔드포인트, top 느린 쿼리) → **AWS Bedrock(Claude)** → 자연어 문단. (유일한 유비용 항목 — 토큰 기반)
- **PDF**: 헤드리스 Chromium으로 리포트 페이지 렌더 → **MinIO(S3 호환)** 저장.
- **공유 링크 ([RP-04])**: 서명 URL + 비밀번호(해시) + 만료/폐기.

---

## 3. 멀티테넌시 & 격리

- **RLS**: 모든 테넌트 스코프 테이블에 `org_id` + PostgreSQL Row-Level Security 정책. 세션 변수 `app.current_org`로 강제.
- **워커 격리**: 잡별 네임스페이스/네트워크 폴리시로 부하·스캔 워커 상호 격리.
- **Enterprise 전용 DB**: 플랜에 따라 스키마/DB 분리 옵션(기술스택 확정 항목).

---

## 4. 보안 설계

| 항목 | 구현 |
|------|------|
| 전송 암호화 | 외부 TLS, 내부·에이전트 **mTLS** |
| 인증 | JWT + OAuth2, SAML(M3) |
| 소스코드 처리 | Ephemeral(RAM), 디스크 미저장, 잡 종료 시 소멸 |
| DDoS 악용 방지 | 도메인 소유권 검증 게이트([SC-01]) |
| 감사 로그 | 모든 잡·과금·권한 변경 이벤트 기록(SOC2 대비) |
| 비밀 관리 | 개발: 로컬 `.env` + SOPS / 프로덕션: Secrets Manager. 코드·이미지에 시크릿 미포함 |

---

## 5. 신뢰성 & DR

- **SLA 99.95%**: 상태 무저장 API 다중 인스턴스 + 헬스체크 + 오토스케일.
- **DR**: PostgreSQL 크로스리전 리드레플리카, 오브젝트/시계열 리전 복제, **10분 내 Failover**. *(프로덕션 단계 — 개발 단계에서는 무비용 유지를 위해 보류)*
- **서킷 브레이커**는 고객 인프라 보호 + 플랫폼 폭주 방지 이중 역할.

---

## 6. 핵심 시퀀스 (부하 테스트 정상 흐름)

```
User → API: POST /load-tests (target, VU, duration)
API → API: 도메인 검증 + 쿼터 확인 (VALIDATING)
API → Queue: enqueue job (QUEUED)
Queue → LoadWorker: dispatch (PROVISIONING→RUNNING)
LoadWorker → 고객서버: HTTP 부하
APM SDK → Collector → API(WS): 실시간 메트릭
API → User(WS): 대시보드 스트림 (≤2s)
[에러율>80%] LoadWorker → API: abort → ABORTED + 알림
LoadWorker → API: 완료 → AGGREGATING
Aggregator → AI/PDF → S3: 리포트 (COMPLETED)
API → Billing: usage 이벤트(VU-Minutes)
```
