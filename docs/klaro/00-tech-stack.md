# klaro — 기술 스택 (Tech Stack)

**버전** v1.0.1-dev · **작성일** 2026-07-16 · **상태** 아키텍처 리뷰용

> 선정 원칙: (1) **개발 단계 무비용** — 셀프호스팅 OSS·로컬 실행 우선, (2) 검증된 오픈소스 우선, (3) 멀티테넌트 격리·관측성 내장, (4) 동적 워커 오케스트레이션 친화.
>
> **비용 정책(중요)**: 개발 단계에서는 유료 클라우드 자원을 쓰지 않는다. **AWS 관리형 서비스(EKS·RDS·S3·SES 등) 사용은 보류**하고 로컬/셀프호스팅으로 대체한다. **예외: LLM은 AWS Bedrock**(Claude on Bedrock)만 사용하며, AWS 계정은 Bedrock 전용으로 둔다. 비용 상세는 [비용 모델](./04-cost-model.md) 참조.

---

## 1. 스택 개요 (한눈에)

| 레이어 | 선택(개발) | 프로덕션 승격(추후) | 개발 비용 | 선정 사유 |
|--------|-----------|--------------------|-----------|-----------|
| Control Plane API | **Go (Gin/Echo)** 또는 **NestJS(TS)** | 동일 | 무료 | 고동시성·gRPC·낮은 메모리 |
| 실시간 게이트웨이 | **Go + WebSocket** | 동일 | 무료 | 메트릭 스트림 fan-out |
| 부하 엔진 | **k6 (xk6 확장)** | 동일 | 무료(OSS) | JS 시나리오, 분산 지원 |
| SAST 엔진 | **Semgrep** + **osv-scanner** | 동일 | 무료(OSS) | 룰 커스터마이즈, 의존성 취약점 |
| DAST 엔진 | **OWASP ZAP** (headless) | 동일 | 무료(OSS) | 성숙한 액티브 스캔 |
| APM SDK | 언어별 **OpenTelemetry** SDK 래핑 | 동일 | 무료 | 표준, 벤더 종속 회피 |
| 워커 오케스트레이션 | **로컬 K8s (k3s / kind / minikube)** 또는 Docker Compose | EKS 등 관리형 | 무료 | 동적 스케일아웃·즉시 회수를 로컬에서 재현 |
| 메시지 큐 | **NATS JetStream** (셀프호스팅) | 동일/관리형 | 무료 | 잡 디스패치 + 이벤트 스트림 |
| 관계형 DB | **PostgreSQL 16** (컨테이너, 셀프호스팅, +RLS) | RDS/Aurora | 무료 | 테넌트 격리(RLS), 과금 정합성 |
| 시계열 DB | **VictoriaMetrics** (셀프호스팅) | 동일/관리형 | 무료 | 고카디널리티 메트릭 |
| 트레이스 저장 | **Grafana Tempo** (로컬 FS / MinIO 백엔드) | Tempo + S3 | 무료 | OTel 네이티브 |
| 로그 저장 | **Grafana Loki** (MinIO 백엔드) | Loki + S3 | 무료 | 저비용 로그 아카이빙 |
| 캐시/세션 | **Redis** (컨테이너) | 동일/관리형 | 무료 | 세션·레이트리밋·잡 캐시 |
| 오브젝트 스토리지 | **MinIO** (S3 호환, 셀프호스팅) | AWS S3 | 무료 | 리포트 PDF·로그 아카이브. **S3 API 그대로**라 프로덕션 전환 코드 변경 최소 |
| 프런트엔드 | **Next.js (React, TS)** | 동일 | 무료 | SSR 대시보드, 실시간 차트 |
| 차트/시각화 | **Recharts / visx** | 동일 | 무료 | 부하·메트릭 시각화 |
| **LLM (AI 리포트)** | **AWS Bedrock — Claude** | 동일 | **유료(토큰)** | AWS 계정 Bedrock 전용 사용. 유일한 개발 단계 유비용 항목 |
| 결제 | **Stripe (테스트 모드)** | Stripe 라이브 | 무료(테스트) | 실거래 전까지 수수료 0. 결제 기능은 그대로 구현 |
| 이메일/알림 | **MailHog** (로컬 SMTP 캡처) | SES 등 | 무료 | 발송 메일을 로컬 UI로 확인, 외부 발송·비용 없음 |
| 비밀 관리 | **로컬 `.env` + SOPS/age** | Secrets Manager | 무료 | 개발 단계 로컬 시크릿 |
| IaC | **Docker Compose** + **Helm(로컬 차트)** | Terraform + Helm | 무료 | 로컬 재현성 |
| CI/CD | **GitHub Actions (무료 티어)** | 동일 | 무료 | 제품 연동 대상과 동일 생태계 |

> 요약: **개발 단계 유비용 항목은 AWS Bedrock(LLM) 토큰뿐**이며, 나머지는 전부 로컬/셀프호스팅 OSS로 비용이 발생하지 않는다.

---

## 2. 서비스별 상세 스택

### 2.1 Control Plane (P0)
- **언어/프레임워크**: Go 1.22+ (권장) — gRPC(제어), REST(외부 API), WebSocket(스트림).
- **RPC**: gRPC + Protobuf (Control Plane ↔ Worker), REST/JSON (클라이언트 대면).
- **인증**: JWT(Access/Refresh), OAuth2(GitHub/Google), SAML 2.0(M3), 서비스 간 mTLS.
- **레이트리밋/쿼터**: Redis 기반 토큰 버킷.

### 2.2 Load Generator (S1)
- **엔진**: k6 코어를 워커 컨테이너로 패키징, xk6로 커스텀 출력(메트릭 → Control Plane WS).
- **오케스트레이션**: 로컬 K8s(k3s/kind) Job. 잡당 N개 워커 Pod 분산. (프로덕션에서 관리형 K8s로 승격)
- **안전장치**: 워커 사이드카가 에러율/5xx 임계치 계산 → Control Plane에 즉시 신호(서킷 브레이커).

### 2.3 Security Scanner (S2)
- **SAST**: Semgrep(코드 룰) + osv-scanner(의존성). GitHub App 웹훅 → Ephemeral 스캔 Job.
- **DAST**: OWASP ZAP headless, 대상 URL 프록시 스캔.
- **Ephemeral 보장**: 소스 체크아웃은 tmpfs(RAM) 볼륨, Job 종료 시 Pod·볼륨 소멸. 디스크 미기록.

### 2.4 APM / Observability (S3)
- **표준**: OpenTelemetry (metrics/traces/logs).
- **SDK 래핑**: `klaro-apm` npm(Node) / PyPI(FastAPI) / Maven(Spring Boot) — OTel 자동계측 + klaro 익스포터.
- **수집기**: OTel Collector (게이트웨이) → VictoriaMetrics(메트릭) / Tempo(트레이스) / Loki(로그). 전부 셀프호스팅.
- **전송**: OTLP over gRPC + mTLS.

### 2.5 Report Aggregator (S4)
- **집계**: 잡 완료 이벤트 → 배치 워커가 부하·스캔·APM 데이터 조인.
- **AI 요약**: **AWS Bedrock(Claude) 호출** — 병목·한계점 자연어 생성. 구조화 입력(JSON) → 요약 텍스트. (토큰 비용 발생, 캐싱·입력 최소화로 절감)
- **PDF 생성**: 서버사이드 렌더(Playwright/Chromium 헤드리스로 대시보드 → PDF), 결과는 MinIO 저장.

### 2.6 결제 (Billing)
- **엔진**: Stripe **테스트 모드**로 구독·계량(Usage-based) 과금 흐름 전체 구현.
- **개발 비용**: 테스트 모드는 실제 결제·수수료가 발생하지 않음(₩0).
- **라이브 전환**: 실거래 시에만 Stripe 수수료(매출 연동) 발생 → 프로덕션 승격 항목.

### 2.7 이메일/알림
- **개발**: **MailHog** — 앱이 발송한 메일을 로컬 SMTP로 가로채 웹 UI로 확인. 외부 발송·비용 없음.
- **Slack 등**: 개발 단계에는 로컬 목/웹훅 스텁으로 대체(비용 없음).

---

## 3. 데이터 저장소 매핑

| 데이터 종류 | 저장소(개발) | 보존 정책 |
|-------------|-------------|-----------|
| 테넌트/유저/프로젝트/과금 메타 | PostgreSQL(컨테이너, RLS) | 영구 |
| 부하 테스트 요약 결과 | PostgreSQL | 플랜별(24h/14d/90d) |
| 시계열 메트릭 | VictoriaMetrics(셀프호스팅) | 플랜별 |
| 트레이스 | Tempo (MinIO/FS 백엔드) | 플랜별 |
| 에러/애플리케이션 로그 | Loki (MinIO 백엔드) | 플랜별 |
| 리포트 PDF | MinIO (S3 호환) | 영구(또는 만료) |
| 세션/레이트리밋/잡 캐시 | Redis(컨테이너) | 단기(TTL) |

---

## 4. 인프라 & 운영 (개발 단계)

- **실행 환경**: 로컬 개발 머신/온프레미스에서 **Docker Compose 또는 로컬 K8s(k3s/kind)**로 전 스택 구동. **AWS 관리형 인프라 사용 보류.**
- **유일한 외부 의존(유료)**: **AWS Bedrock(LLM)**. 그 외 외부 유료 서비스 없음.
- **네트워크**: 내부 통신 mTLS(로컬 인증서), 외부 노출 시 로컬 리버스 프록시(Traefik/Caddy).
- **비밀 관리**: 로컬 `.env` + SOPS/age 암호화(리포지토리에 평문 시크릿 금지).
- **관측성(자체)**: Prometheus + Grafana + Alertmanager 셀프호스팅(플랫폼 자체 모니터링).
- **DR/멀티리전**: **프로덕션 단계로 연기**(개발 단계 무비용 유지). 설계상 리전 확장 가능 구조만 확보.

---

## 5. 프로덕션 전환 시 유비용으로 승격되는 항목 (연기됨)

> 개발 단계에서 무비용으로 두되, 상용 출시 시 아래가 유비용으로 전환된다. 상세는 [비용 모델 §5](./04-cost-model.md).

- 로컬 K8s → **관리형 K8s(EKS 등)** + 워커 컴퓨트
- MinIO → **AWS S3**, 컨테이너 PostgreSQL → **RDS/Aurora**
- MailHog → **실제 이메일 발송(SES 등)**
- Stripe 테스트 → **Stripe 라이브(수수료)**
- 단일 로컬 → **멀티리전/DR**, 아웃바운드 트래픽 비용

---

## 6. 착수 전 확정 필요

1. **Control Plane 언어**: Go vs NestJS(TS) — 팀 역량 기준.
2. **로컬 오케스트레이션**: Docker Compose(단순) vs 로컬 K8s(프로덕션 근접) — 재현성 vs 단순성.
3. **큐**: NATS JetStream(경량) vs Kafka — 과금 이벤트 재처리 요구 수준.
4. **Bedrock 모델/리전**: 사용할 Claude 모델과 Bedrock 리전, 토큰 예산 상한.
5. **테넌트 격리**: RLS(공용 DB) vs 스키마/DB 분리(Enterprise 전용 DB 요구와 연계).
