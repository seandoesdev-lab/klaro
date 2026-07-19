---
name: klaro-backend-patterns
description: klaro 백엔드(Control Plane API·워커·Billing)를 명세대로 구현하는 패턴. API 엔드포인트·RLS·잡 상태머신·k6 다중 API 가중치 부하·gRPC/mTLS·Stripe 테스트 구현/수정 시 반드시 사용. backend-builder 에이전트 전용.
---

# klaro-backend-patterns — 백엔드 구현 패턴

## 착수 전
`_workspace/01_*_contract.md`, `_workspace/02_architect_design.md`를 읽고 빌드 순서를 따른다. 없으면 spec-analyst/architect 산출을 먼저 요청.

## 필수 패턴

### API (03-api-spec.md 준수)
- 경로·메서드·권한·에러 코드를 명세와 1:1. 표준 에러 포맷 `{ error: { code, message, details } }`.
- 페이지네이션 `?limit=&cursor=` → `{ data:[], next_cursor }`. 레이트리밋 `429 + Retry-After`.
- 부하 생성: `domain_id` + `scenario.apis[{endpoint_id, weight}]`. 검증: 도메인 미검증 → `403 DOMAIN_NOT_VERIFIED`, endpoint가 domain 비소속 → `422 VALIDATION_ERROR`([LG-04], [CAT-01]).

### RLS 멀티테넌시 (타협 불가)
- `org_id` 스코프 테이블 전체에 RLS 정책. 요청 처리 시작에 `SET app.current_org = <uuid>`. 애플리케이션 레벨 필터만으로 대체 금지.

### 잡 생명주기
- 상태머신 전이만 허용: `PENDING→VALIDATING→QUEUED→PROVISIONING→RUNNING→AGGREGATING→COMPLETED`(+`REJECTED/FAILED/ABORTED`). 불법 전이 거부.
- 워커 idle=0: 잡 종료 시 K8s Job/Pod 즉시 회수(finalizer).

### 서킷 브레이커
- 워커 사이드카가 슬라이딩 윈도로 에러율 계산 → >80% 또는 지속 503 → CP에 abort → 전 워커 중단 → `ABORTED` + 부분 결과 보존 + 알림(MailHog).

### 워커별
- S1: k6 executor가 weight 비율로 대상 API 선택, 메트릭 API 단위 태깅 → `load_test_results` API별 1행 + 전체 집계 1행.
- S2: 스캔 소스는 **tmpfs(RAM)** 체크아웃, 디스크/DB 기록 금지, 잡 종료 시 소멸. SAST=Semgrep+osv-scanner, DAST=ZAP headless.
- S3: OTel 수집 → VictoriaMetrics/Tempo/Loki, OTLP+mTLS, tail 샘플링(오버헤드 ≤2%).
- S4: 집계 → **Bedrock mock 기본**, PDF는 헤드리스 Chromium → MinIO(S3 호환 SDK).

### 비용 게이트
- Bedrock 외 유료 외부 호출 추가 금지([COST-05]). 새 유료 의존이 필요하면 구현 대신 제기.

## 검증
변경 후 빌드/테스트 실행. 미완 항목은 `_workspace/03_backend-builder_manifest.md`에 엔드포인트·모듈 단위로 기록(프론트/QA가 경계면 참조).

## 왜 이렇게 하나
RLS·도메인 게이트·Ephemeral·mTLS는 klaro의 보안 판매 포인트다. 애플리케이션 레벨로 우회하면 한 번의 실수로 테넌트 유출·DDoS 악용·소스 유출이 발생한다.
