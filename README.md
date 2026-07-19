# klaro

> 배포 전 **"지금 배포해도 되는가?"** 에 답하는 배포 적합성 SaaS.
> 부하 테스트·보안 스캔·APM·리포트를 하나의 대시보드로 종합한다.

**상태:** 설계 문서 6종 + **부하 테스트 서비스(S1) MVP 구현**(Go) + 실시간 대시보드(전 사이드바 기능). 개발 단계 무비용 원칙(로컬/셀프호스팅 OSS)으로 동작한다.

---

## 무엇이 구현되어 있나

`services/load-test/` 에 실제 동작하는 Go 서비스가 있고, 대시보드가 같은 오리진에서 서빙된다.

| 기능 | 구현 내용 |
|------|-----------|
| **부하 테스트** | 도메인 검증 게이트 → k6 워커 실행 → WebSocket 실시간 메트릭(≤2초) → 결과 요약. 에러율 80% 초과 시 서킷브레이커로 안전 종료. |
| **보안 스캔** | DAST(대상 보안 헤더 실분석: HSTS/CSP/X-Frame-Options 등 → 심각도·점수), SAST(스텁). 오탐 무시 관리. |
| **APM** | 에이전트 등록(ingest 토큰) + 트레이스/로그 수집(Postgres 저장) + 느린 트레이스(≥3s) 워터폴. |
| **리포트** | 부하+스캔 조인 → 성능/보안 점수 + **mock AI 요약**(비용 정책상 Bedrock 미호출) + 공유 링크(비밀번호/만료). |
| **설정** | 도메인 소유권 검증 관리(DNS TXT / 파일 챌린지), APM SDK 토큰. |
| **대시보드** | 앱 셸(상단바+사이드바+메인), light/dark 테마, KPI 타일·차트·워터폴. `//go:embed`로 API가 서빙. |

---

## 빠른 시작 (Docker만 필요)

로컬에 Go/k6 설치 없이 전부 컨테이너로 동작한다.

```bash
cd services/load-test
docker compose up --build -d
curl -s localhost:8080/healthz          # {"status":"ok"}
```

브라우저에서 **http://localhost:8080** 을 열면 대시보드가 뜬다.

### 부하 테스트 스모크
```bash
# 1) 테스트 대상 기동 (같은 네트워크, 이름은 반드시 target)
docker run -d --name target --network load-test_default nginx:alpine

# 2) 대상 호스트를 검증 도메인으로 시드
docker compose exec -T postgres psql -U klaro -d klaro -c \
"INSERT INTO verified_domains (project_id,domain,method,token,status,verified_at)
 VALUES ('00000000-0000-0000-0000-000000000002','target','file','x','verified',now())
 ON CONFLICT (project_id,domain) DO UPDATE SET status='verified';"
```
그다음 대시보드의 **부하 테스트** 화면에서 대상 URL `http://target:80` 을 입력하고 시작한다.
(도메인 미검증 시 실행 버튼이 비활성화되고 `403 DOMAIN_NOT_VERIFIED` 로 거부된다.)

---

## 아키텍처 (구현분)

```
[브라우저] ──REST/WS──▶ Control Plane API (Go/Gin, :8080)
                          │  ├─ Postgres  (메타데이터·스캔·APM·리포트)
                          │  └─ Redis     (잡 큐 + 메트릭 Pub/Sub)
                          ▼ enqueue
                      Load/Scan Worker (Go) ──▶ k6 / 보안 헤더 분석 ──▶ [대상]
```

- **Control Plane**: REST + WebSocket, 개발용 인증 스텁(`Authorization: Bearer dev`), 잡 상태머신(`validating→queued→running→aggregating→completed`, 예외 `aborted/failed/rejected`).
- **워커**: k6 부하(NDJSON 집계 + 서킷브레이커) / 스캔.
- **저장**: 메타·요약·스캔·APM·리포트는 PostgreSQL. 시계열 원본은 프로덕션에서 VictoriaMetrics/Tempo/Loki로 승격 예정.

디렉터리:
```
services/load-test/
  cmd/{api,worker}        # 엔트리포인트
  internal/
    api/                  # 라우터·핸들러·WS·대시보드(web/index.html, go:embed)
    worker/               # k6 러너·집계·서킷브레이커·스캔 워커
    scenario/ breaker/    # 순수 로직(시나리오 생성·서킷브레이커)
    domainverify/         # 도메인 소유권 검증
    store/ queue/ model/  # Postgres 리포지토리·큐·도메인 타입
  migrations/             # 0001 부하·0002 스캔·0003 APM/리포트
  docker-compose.yml
```

---

## 테스트

```bash
cd services/load-test
# 단위 테스트 (Docker, 로컬 Go 불필요)
docker run --rm -v "$PWD:/src" -w /src golang:1.25 sh -c "go build ./... && go test ./..."

# 통합 테스트 (compose Postgres/Redis 대상)
docker run --rm --network load-test_default -v "$PWD:/src" -w /src \
  -e TEST_DATABASE_URL=postgres://klaro:klaro@postgres:5432/klaro?sslmode=disable \
  -e TEST_REDIS_ADDR=redis:6379 golang:1.25 \
  sh -c "go test -tags integration ./..."
```

- **단위**: 시나리오 생성·상태머신·서킷브레이커·스캔 헤더 분석·리포트 점수/요약·APM 필터.
- **통합**: store·queue (Postgres/Redis).
- **브라우저 e2e**: `testdata/*.mjs` (Playwright 헤드리스) — 앱 셸·뷰 전환·실시간 KPI·전 기능 구동·콘솔 에러 검증.

> Windows Git Bash에서 Docker 볼륨 마운트 시 `MSYS_NO_PATHCONV=1` 를 앞에 붙인다.

---

## 설계 문서

`docs/klaro/` 가 단일 진실 공급원(한국어).

| 문서 | 내용 |
|------|------|
| [00-tech-stack](docs/klaro/00-tech-stack.md) | 기술 스택·개발/프로덕션 승격 |
| [01-technical-design](docs/klaro/01-technical-design.md) | 아키텍처·서비스별 설계·상태머신 |
| [02-data-model](docs/klaro/02-data-model.md) | PostgreSQL 스키마·인덱스 |
| [03-api-spec](docs/klaro/03-api-spec.md) | REST 엔드포인트·요청/응답 |
| [04-cost-model](docs/klaro/04-cost-model.md) | 무비용 개발 원칙(FinOps) |
| [05-ui-ux-design](docs/klaro/05-ui-ux-design.md) | 화면 설계·디자인 토큰 |
| [prototype.html](docs/klaro/prototype.html) | 디자인 프로토타입(대시보드 시각 기준) |

구현 설계/계획은 `docs/superpowers/` 에도 있다.

---

## 비용 정책

개발 단계에서 금전 비용이 발생하는 유일한 항목은 **AWS Bedrock(LLM) 토큰**뿐이며, 그마저도 개발 중에는 **mock 이 기본**이다. 나머지(K8s·DB·스토리지·메일·결제)는 전부 로컬/셀프호스팅 OSS 또는 테스트 모드로 ₩0. 새 유료 외부 의존성은 추가하지 않는다. 상세: [04-cost-model](docs/klaro/04-cost-model.md).

---

## 아직 안 된 것 (프로덕션 승격 항목)

실제 Semgrep/OWASP ZAP 통합, OpenTelemetry SDK 실수집(VictoriaMetrics/Tempo/Loki), Bedrock 실호출 AI 요약, PDF 렌더, K8s 동적 워커 오케스트레이션, RLS 멀티테넌시, JWT/OAuth/RBAC, 결제(Stripe) — 모두 MVP 이후 단계.
