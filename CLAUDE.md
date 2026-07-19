# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 저장소 성격 (중요)

이 저장소는 현재 **설계 단계**다. 애플리케이션 코드·빌드 시스템·테스트·패키지 매니페스트가 **아직 없다**. 존재하는 것은:

- `docs/klaro/*.md` — 설계 문서 6종 (이 프로젝트의 **단일 진실 공급원 / source of truth**)
- `docs/klaro/prototype.html` — 단일 파일 화면 프로토타입 (React/빌드 없이 순수 HTML+CSS+JS, 디자인 토큰·화면 레이아웃 검증용)

따라서 "빌드/린트/테스트 명령"은 아직 정의되지 않았다. 구현 착수 시 스택 결정이 먼저다(아래 §미확정 사항).

문서는 **한국어**로 작성되어 있고, 이 저장소에서 이어지는 산출물도 한국어 기준으로 맞춘다.

## 문서 지도

문서들은 서로 상대경로로 교차 참조하며, 이 링크 그래프를 깨지 않게 유지해야 한다. 어떤 항목을 바꾸면 참조하는 다른 문서도 함께 갱신한다.

| 문서 | 내용 |
|------|------|
| `docs/klaro/00-tech-stack.md` | 기술 스택 선정과 개발/프로덕션 승격 매핑 |
| `docs/klaro/01-technical-design.md` | 아키텍처·서비스별 설계·잡 상태머신·시퀀스 |
| `docs/klaro/02-data-model.md` | PostgreSQL 스키마(ERD)·인덱스·격리/보존 규칙 |
| `docs/klaro/03-api-spec.md` | REST 엔드포인트 목록·요청/응답 예·gRPC(내부)·웹훅 |
| `docs/klaro/04-cost-model.md` | 무비용 개발 원칙·비용 통제(FinOps) |
| `docs/klaro/05-ui-ux-design.md` | 화면 설계·디자인 토큰·상태 디자인·핸드오프 노트 |

`prototype.html`은 `05-ui-ux-design.md`의 시각 구현체다. 디자인 토큰(색상 변수, 라이트/다크 테마)이 두 곳에 함께 존재하므로 한쪽을 바꾸면 다른 쪽도 동기화한다.

## 제품 개요

**klaro** — 배포 전 "지금 배포해도 되는가?"에 답하는 SaaS. 네 가지를 하나의 배포 적합성 리포트로 종합한다:

1. **부하 테스트 (S1)** — k6 기반, 동적 워커 스케일아웃, 서킷 브레이커로 대상 서버 보호
2. **보안 스캔 (S2)** — SAST(Semgrep+osv-scanner) / DAST(OWASP ZAP), 소스는 Ephemeral(RAM) 처리
3. **APM/관측성 (S3)** — OpenTelemetry 기반, VictoriaMetrics/Tempo/Loki 저장
4. **리포트 (S4)** — 위 결과를 조인 → **AWS Bedrock(Claude)**로 AI 요약 → PDF(MinIO 저장)

## 아키텍처 핵심 (구현 시 반드시 준수)

전체 그림은 `01-technical-design.md §1`. 설계 전반을 관통하는 불변 원칙:

- **멀티테넌시 = PostgreSQL RLS**: `org_id`를 가진 모든 테이블에 Row-Level Security. 세션 변수 `SET app.current_org = <uuid>`로 스코프 강제. 인가 계층은 `Org > Project > Resource`, 역할 `owner/admin/member/viewer`.
- **도메인 소유권 검증 게이트 (DDoS 악용 차단)**: 부하/DAST 잡은 `verified_domains`에 등록된(DNS TXT 또는 challenge 파일로 검증된) 도메인에만 생성 허용. 미검증이면 `REJECTED`.
- **잡 생명주기 상태머신**: `PENDING→VALIDATING→QUEUED→PROVISIONING→RUNNING→AGGREGATING→COMPLETED`, 예외 경로 `REJECTED/FAILED/ABORTED`. `load_tests.status` enum과 일치해야 한다.
- **서킷 브레이커**: 워커 사이드카가 슬라이딩 윈도로 에러율 계산 → 임계(에러율 >80%) 초과 시 즉시 `abort`. 사고가 아니라 "보호"로 UI에 표현.
- **Ephemeral 소스 처리**: 스캔 대상 소스코드는 tmpfs(RAM)에만, 디스크/DB 저장 절대 금지, 잡 종료 시 소멸.
- **워커 idle=0**: 잡 종료 시 K8s Job/Pod 즉시 회수(finalizer).
- **통신 규약**: 클라이언트↔Control Plane은 REST/JSON + WebSocket(실시간 메트릭), Control Plane↔Worker는 큐(디스패치)+gRPC(제어), APM SDK↔Collector는 OTLP/gRPC + **mTLS 필수**.
- **시계열 데이터는 RDB 밖**: 메트릭/트레이스/로그는 VictoriaMetrics/Tempo/Loki에 저장하고, PostgreSQL은 참조 키(`metrics_ref`, trace id 등)만 보관.

## 비용 정책 (설계·구현 판단 기준)

`04-cost-model.md`가 상세. **개발 단계에서 금전 비용이 발생하는 유일한 항목은 AWS Bedrock(LLM) 토큰뿐이다.** 나머지는 전부 로컬/셀프호스팅 OSS로 ₩0:

- AWS 관리형 서비스(EKS/RDS/S3/SES 등) 사용 **보류** → 로컬 K8s(k3s/kind)·PostgreSQL 컨테이너·MinIO(S3 호환)·MailHog(SMTP 캡처)로 대체.
- Stripe는 **테스트 모드**(실거래·수수료 없음).
- AI 요약은 개발 중 **mock 모드가 기본**([COST-03]); Bedrock 실호출은 통합 검증 시에만 켠다.
- **Bedrock 외 유료 외부 서비스 호출을 코드에 새로 추가하지 않는다**([COST-05] 리뷰 게이트). 새 유료 의존성이 필요하면 먼저 제기할 것.

MinIO는 S3 API 호환이므로, 스토리지 접근은 S3 SDK로 작성해 프로덕션(S3) 전환 시 코드 변경이 최소화되도록 한다.

## 요구사항 ID 규약

문서 곳곳에 `[SC-01]`, `[BILL-02]`, `[APM-03]`, `[LG-03]`, `[LG-04]`, `[CAT-01]`, `[COST-05]`, `[EDGE-02]` 같은 **요구사항 태그**가 있다. 접두사는 도메인을 뜻한다(SC=Security/Scan, BILL=Billing, APM, LG=Load Generator, CAT=API 카탈로그, COST=FinOps, RP=Report, EDGE=엣지케이스). 구현/PR/커밋에서 관련 요구사항을 이 ID로 참조하면 문서와 코드가 추적 가능해진다.

## 미확정 사항 (구현 착수 전 결정 필요)

각 문서 말미의 "착수 전 확정 필요" 절에 모여 있다. 핵심:

- **Control Plane 언어**: Go(Gin/Echo) vs NestJS(TS)
- **로컬 오케스트레이션**: Docker Compose(단순) vs 로컬 K8s(프로덕션 근접)
- **메시지 큐**: NATS JetStream vs Kafka
- **Bedrock**: 사용할 Claude 모델·리전·토큰 예산 상한
- **테넌트 격리**: 공용 DB + RLS vs 스키마/DB 분리(Enterprise)

이들이 정해지기 전에는 스택 특정 코드를 임의로 만들지 말고, 결정을 먼저 확인하거나 제안한다.

## 하네스: klaro 개발 (명세→구현→검증)

**목표:** klaro 설계 문서를 불변식·비용 정책을 지키며 실제 구현으로 옮기는 것.

**트리거:** klaro 기능/서비스 구현·개발·빌드 요청 시(부하/스캔/APM/리포트/과금/대시보드, 후속 "재실행·수정·보완" 포함) `klaro-build` 스킬을 사용하라. 단순 질문은 직접 응답 가능.

**변경 이력:**
| 날짜 | 변경 내용 | 대상 | 사유 |
|------|----------|------|------|
| 2026-07-19 | 초기 구성 (통합 하네스: 에이전트 6 + 스킬 6 + 오케스트레이터 1) | 전체 | - |
