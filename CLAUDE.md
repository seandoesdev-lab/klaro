# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 저장소 성격 (중요)

이 저장소는 **설계 문서 + 초기 구현이 공존**하는 단계다.

- `docs/klaro/*.md` — 설계 문서 6종 (이 프로젝트의 **단일 진실 공급원 / source of truth**)
- `docs/klaro/prototype.html` — 단일 파일 화면 프로토타입 (React/빌드 없이 순수 HTML+CSS+JS, 디자인 토큰·화면 레이아웃 검증용)
- `services/load-test/` — **부하 테스트 서비스(S1) MVP 구현**(Go). 실제 코드·테스트·docker-compose 존재. 빌드/테스트는 이 디렉토리 기준.
- `_workspace/` — 하네스 실행 중간 산출물(요구사항 계약·아키텍처 설계). 감사 추적용, 삭제 금지.

새 서비스 착수 시에는 여전히 스택 결정이 먼저다(아래 §미확정 사항). 문서는 **한국어**로 작성되어 있고, 이어지는 산출물도 한국어 기준으로 맞춘다.

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

`prototype.html`은 `05-ui-ux-design.md`의 시각 구현체다. 디자인 토큰(색상 변수, 라이트/다크 테마)이 두 곳에 함께 존재하므로 한쪽을 바꾸면 다른 쪽도 동기화한다. 구현 관련 설계/계획은 `docs/superpowers/`에도 있다.

## 제품 개요

**klaro**는 두 축으로 구성된다:

1. **배포 적합성 SaaS** — 배포 전 "지금 배포해도 되는가?"에 답한다. 세 가지를 하나의 배포 적합성 리포트로 종합:
   - **부하 테스트 (S1)** — k6 기반, 동적 워커 스케일아웃, 서킷 브레이커로 대상 서버 보호
   - **보안 스캔 (S2)** — SAST(Semgrep+osv-scanner) / DAST(OWASP ZAP), 소스는 Ephemeral(RAM) 처리
   - **리포트 (S4)** — 부하+스캔(+APM 스냅샷) 조인 → **AWS Bedrock(Claude)**로 AI 요약 → PDF(MinIO 저장)
2. **상시 관측 플랫폼 (S3, APM/Observability)** — **최종 목표는 Datadog 유사 서비스.** 배포 시점 스냅샷이 아니라, 운영 중인 서비스에 OpenTelemetry SDK를 상시 설치해 메트릭·트레이스·로그를 지속적으로 수집·저장·조회·알림한다. 배포 적합성 리포트는 이 플랫폼이 보유한 데이터를 "특정 시간 구간 스냅샷"으로 소비하는 관계다.

> **2026-08-23 결정**: S3(APM)을 배포 리포트에 종속된 스냅샷 기능이 아니라 **독립된 상시 관측 제품**으로 승격. 데이터 모델(장기 보존 시계열 vs 잡 단위 스냅샷)·과금 모델(호스트/데이터량 기준 vs VU-Minutes)·인프라(상시 가동 Collector vs on-demand 워커)가 배포 리포트 흐름과 근본적으로 다르므로, 아키텍처 설계 단계에서 별도 서비스 경계로 재설계한다. 상세는 `docs/klaro/01-technical-design.md §2.4`.

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

- **Control Plane 언어**: Go(Gin/Echo) vs NestJS(TS) — *S1은 Go 채택(`services/load-test/`)*
- **로컬 오케스트레이션**: Docker Compose(단순) vs 로컬 K8s(프로덕션 근접)
- **메시지 큐**: NATS JetStream vs Kafka
- **Bedrock**: 사용할 Claude 모델·리전·토큰 예산 상한
- **테넌트 격리**: 공용 DB + RLS vs 스키마/DB 분리(Enterprise)
> **2026-08-23 아키텍처 확정**: 상시 관측 플랫폼(S3)의 서비스 경계·데이터 모델·과금·알림·다운샘플링·SDK 배포 전략은 architect 설계로 확정됨. 상세는 `_workspace/05_architect_observability-design.md` §7(사용자 확정 결과), 반영 문서는 `docs/klaro/01-technical-design.md §2.4` · `02-data-model.md §2.5/§2.7/§3` · `03-api-spec.md §3.7`. 핵심: 신규 `services/observability/`(Go/Gin) 분리, org 단위 관측 키+쿼터(호스트수+GB, 초과 시 차단 없이 overage 과금), 알림=vmalert, 다운샘플링=vmalert recording rule(메트릭 한정), 대시보드(OBS-09)는 M2+ 이연, SDK(`klaro-apm`)는 thin OTel wrapper로 설계 완료(실 구현은 별도 마일스톤).
- **(남은 구현 리스크, 결정 아님)**: vmalert의 멀티테넌트 recording rule 격리 평가 동작을 backend-builder 착수 전 스파이크로 검증 필요(설계 §HOW-10). MVP `apm_spans/apm_logs`(Postgres) → VM/Tempo/Loki 이행은 상시 경로 안정 확인 후 실행.

이들이 정해지기 전에는 스택 특정 코드를 임의로 만들지 말고, 결정을 먼저 확인하거나 제안한다.

## 하네스: klaro 개발 (명세→구현→검증)

**목표:** klaro 설계 문서를 불변식·비용 정책을 지키며 실제 구현으로 옮기는 것.

**트리거:** klaro 기능/서비스 구현·개발·빌드 요청 시(부하/스캔/APM/리포트/과금/대시보드, 후속 "재실행·수정·보완" 포함) `klaro-build` 스킬을 사용하라. 단순 질문은 직접 응답 가능.

## 하네스: klaro 프런트엔드 디자인

**목표:** 확정된 klaro 디자인 시스템(프로토타입·UI/UX 명세)을 재사용해 실제 API에 연결되는 대시보드 화면을 일관되게 구현한다.

**트리거:** klaro 대시보드/화면/UI 생성·수정·재실행·보완 요청 시 `klaro-frontend-orchestrator` 스킬을 사용하라. 단순 질문은 직접 응답 가능.

**변경 이력:**
| 날짜 | 변경 내용 | 대상 | 사유 |
|------|----------|------|------|
| 2026-07-19 | 초기 구성 (통합 하네스: 에이전트 6 + 스킬 6 + 오케스트레이터 klaro-build) | 전체 | 명세→구현→검증 개발 하네스 구축 |
| 2026-07-19 | 초기 구성 (designer/qa 에이전트 + dashboard-design 스킬 + orchestrator) | 전체 | 부하 테스트 대시보드 프런트엔드 하네스 구축 |
| 2026-07-19 | "앱 셸 골격 복제(필수)" 규칙 추가 | skills/klaro-dashboard-design | 1차 산출물이 프로토타입 상단바+사이드바 셸을 복제하지 않아 시각적으로 달라 보인 피드백 반영 |
| 2026-08-23 | S3(APM)을 독립 상시 관측 제품으로 승격 결정(제품 개요·미확정 사항 갱신) | CLAUDE.md, docs/klaro/*.md | 최종 목표가 Datadog 유사 서비스로 확인됨. 배포 리포트(스냅샷)와 상시 관측(장기 시계열)의 제품·데이터·과금 모델이 근본적으로 달라 문서 프레이밍부터 분리 |
| 2026-08-23 | 상시 관측 플랫폼(S3) 요구사항 계약(OBS-01~10) + 아키텍처 설계 확정(신규 services/observability/ 분리, 9+2건 HOW 결정) | _workspace/04·05, docs/klaro/01·02·03-*.md | spec-analyst→architect 하네스 파이프라인 실행. 사용자가 쿼터 초과=전 플랜 overage 과금(권고와 다름), 다운샘플링·SDK 배포전략은 이연 대신 즉시 설계를 선택 |
| 2026-08-24 | 인프라 호스트 수집 + hostmap(P1b): OTel hostmetrics 에이전트, `/obs/hosts`·`/obs/hosts/:id/metrics`·`/obs/slo/uptime`, 대시보드 `/infrastructure`(visx 육각 hostmap + 업타임 SLO) | services/observability/{deploy,internal/{explorer,inventory,api}}, apps/observability-dashboard/src, docs/klaro/03-api-spec.md | Datadog Infrastructure의 최소 형태. 수집은 별도 agent→gateway 위상을 쓴다 — 게이트웨이의 테넌트는 요청 컨텍스트에서 오므로 스크레이프 파이프라인을 그 안에 두면 "테넌트는 CP가 준 값뿐" 불변식을 우회해야 한다. 조인 키는 host_ident(=`instance` 라벨) 하나이고 조인은 앱에서 한다(시계열은 RDB 밖) |
