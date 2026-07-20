# klaro Phase 3 — S3 APM/관측성 요구사항 계약

**작성** spec-analyst · **일자** 2026-07-20 · **상태** architect 인계 대기
**단일 진실 공급원**: `docs/klaro/00~05`. 문서와 코드 충돌 시 문서 기준, 문서 자체 모순은 §모호/모순에 표기.

> 이 계약은 **WHAT + 수용 기준**만 담는다. HOW(Collector 배치·인증서 발급 방식·저장소 인스턴스 구조)는 `architect` 결정 사항으로 §architect 결정 필요 목록에 분리했다.
> 확정 스택(입력): Go · 공용 DB+RLS · Docker Compose · 시계열 = VictoriaMetrics(메트릭)/Tempo(트레이스)/Loki(로그) OSS 무비용 [COST-05] · mTLS 필수.

---

## 범위 (Phase 3)

설계 문서(`01 §2.4`, `00 §2.4/§3`, `02 §2.5`)가 규정한 실제 APM 파이프라인으로 **승격**한다. 현재 MVP는 span/log를 PostgreSQL(`apm_spans`/`apm_logs`)에 직접 저장하고 REST(`X-Ingest-Token`)로 수집하는 상태이며, 이는 불변식 "시계열 RDB 밖" 위반(Phase 1 검증 F-7, `_workspace/06_invariants-reviewer_verdict.md:19,56,92`)이다.

포함:
1. **OTLP 수집** — OTLP/gRPC + mTLS 수신(트레이스/메트릭/로그). [OTLP-*, MTLS-*]
2. **시계열 저장소 분리(불변식)** — 메트릭/트레이스/로그를 VictoriaMetrics/Tempo/Loki로 이전, PostgreSQL은 참조 키만. [TSDB-*]
3. **조회/집계** — 대시보드 소비용 APM 조회 API(서비스 맵, 레이턴시/에러율, 트레이스, 로그 검색). [APM-02, APM-03, APMQ-*]
4. **에이전트 등록·인증** — `apm_agents` 등록, ingest 토큰/mTLS 인증. [MTLS-02]

범위 밖(명시): S4 리포트의 APM 데이터 조인(집계 워커), 플랫폼 자체 관측성(Prometheus/Grafana, `00 §4`), 프로덕션 K8s 승격, SDK 패키지(`klaro-apm` npm/PyPI/Maven) 자체 배포. 단 SDK가 기대하는 수집 프로토콜 계약(OTLP/gRPC+mTLS)은 본 계약에 포함.

---

## 요구사항 계약

### [OTLP-01] OTLP/gRPC 텔레메트리 수집
- 설명: 계측된 고객 앱(OTel SDK)이 트레이스·메트릭·로그를 **OTLP over gRPC**로 klaro에 전송하고, klaro가 이를 수신·수용한다. 현행 REST(JSON) 수집(`POST /projects/:id/apm/ingest`)에서 OTLP 표준 프로토콜로 승격한다.
- 관련 ID: [OTLP-01](신설), [APM-01] (`01-technical-design.md:64,116`; `00-tech-stack.md:64` "전송: OTLP over gRPC + mTLS")
- 수용 기준:
  - OTLP/gRPC 수신 엔드포인트가 존재하고 표준 OTLP `ExportTraceServiceRequest`/`ExportMetricsServiceRequest`/`ExportLogsServiceRequest`를 수용한다.
  - 수집 경로는 대상 앱 요청 경로를 블로킹하지 않는다(비동기 배치 익스포트, `01:113`).
  - 인증 실패/미인가 텔레메트리는 거부(수용 카운트 0)된다.
- 데이터 모델: 원본 텔레메트리는 RDB에 저장하지 않음(→ TSDB-01~03). `apm_agents.ingest_token`(`02:206`).
- API: 신설 OTLP/gRPC 리스너. 현행 REST 격차: `apm.go:46 apmIngest`, `router.go:43`. **REST 수집을 존치/폐기/병행할지는 §결정 필요.**

### [OTLP-02] OTel Collector 파이프라인(메트릭/트레이스/로그 라우팅)
- 설명: 수집한 텔레메트리를 신호 종류별로 분리해 VictoriaMetrics(메트릭)/Tempo(트레이스)/Loki(로그)로 export 한다.
- 관련 ID: [OTLP-02](신설), [TSDB-01~03] (`01:116`; `00 §2.4:63`)
- 수용 기준:
  - 메트릭은 VictoriaMetrics, 트레이스는 Tempo, 로그는 Loki에만 최종 저장된다(신호별 라우팅 검증).
  - Collector/수집기가 Docker Compose 스택에 서비스로 존재하고 기동한다.
  - 트레이스 tail-based 샘플링·배치 익스포트가 적용된다([APM-01]).
- 데이터 모델: 없음(파이프라인). PostgreSQL 미기록.
- API: 없음(내부 파이프라인). **Collector를 별도 서비스로 둘지 klaro 게이트웨이가 OTLP 수신할지는 §결정 필요.**

### [MTLS-01] 수집 경로 mTLS 상호 인증(불변식, MUST)
- 설명: APM SDK↔Collector(수집기) 구간은 **mTLS 필수**. 클라이언트·서버 상호 인증서로 양방향 검증한다.
- 관련 ID: [MTLS-01](신설), 불변식(CLAUDE.md "APM SDK↔Collector는 OTLP/gRPC + mTLS 필수"; `01:47,116,143`; `00:64,100`)
- 수용 기준:
  - 유효 클라이언트 인증서 없는 OTLP/gRPC 연결은 거부된다(TLS 핸드셰이크 실패).
  - 서버 인증서가 클라이언트에서 검증 가능(개발 CA 신뢰 체인 존재).
  - 평문(비-TLS) 수집 경로는 노출되지 않는다(개발 편의 예외를 두면 §결정 필요로 명시).
- 데이터 모델: 없음(전송 보안).
- API: OTLP/gRPC 리스너 TLS 설정. **로컬 Compose 인증서 발급·배포 방식(개발 CA), 테넌트별 인증서 vs 공용 CA+토큰 병행은 §결정 필요.**

### [MTLS-02] 에이전트 등록 및 자격 발급
- 설명: 프로젝트 멤버가 APM 에이전트를 등록하면 klaro가 그 에이전트의 인증 자격(현행 ingest 토큰, 목표 mTLS 클라이언트 인증서/식별)을 발급한다. 발급된 자격은 텔레메트리를 정확한 org/project 스코프로 귀속시킨다.
- 관련 ID: [MTLS-02](신설), [APM-*] (`02:206` "ingest_token: mTLS 클라이언트 식별"; `03-api-spec.md:73`)
- 수용 기준:
  - `POST /projects/:id/apm/agents {language}` 로 에이전트 등록 시 language ∈ {nodejs, springboot, fastapi} 검증(`apm.go:22-26`), 자격이 1회 반환된다.
  - 발급 자격으로 전송된 텔레메트리는 등록 시점의 org_id/project_id로만 귀속된다(교차 테넌트 귀속 불가).
  - `GET /projects/:id/apm/agents` 로 목록·`last_seen_at` 조회 가능(`apm.go:35`, `apm_store.go:24`).
- 데이터 모델: `apm_agents(id, org_id, project_id, language, ingest_token, last_seen_at)`(`02:198-207`, `migrations/0003:5-13`).
- API: `POST/GET /projects/:id/apm/agents`(`03:72-73`). 격차: `ingest_token` 방식은 구현됨(`apm_store.go:13-22,46-62`), **mTLS 인증서 발급/식별 미구현** — 토큰↔인증서 관계는 §결정 필요.

### [TSDB-01] 메트릭 → VictoriaMetrics 저장(불변식, MUST)
- 설명: 수집 메트릭은 VictoriaMetrics에 저장하고 PostgreSQL에는 저장하지 않는다.
- 관련 ID: [TSDB-01](신설), 불변식(시계열 RDB 분리) (`00 §3:88`; `02:7,209`)
- 수용 기준: 메트릭 원본이 VictoriaMetrics에서 조회되고, PostgreSQL 어느 테이블에도 메트릭 시계열 행이 생기지 않는다.
- 데이터 모델: PostgreSQL은 참조 키만(`load_test_results.metrics_ref`, `02:152`). → TSDB-04.
- API: 조회는 APMQ-*.

### [TSDB-02] 트레이스 → Tempo 저장(불변식, MUST)
- 설명: span/trace 원본은 Grafana Tempo에 저장하고 PostgreSQL `apm_spans`에 저장하지 않는다.
- 관련 ID: [TSDB-02](신설), [APM-03] (`00 §2.4:63`, `§3:89`)
- 수용 기준:
  - 신규 트레이스가 Tempo에서 trace_id로 조회된다.
  - 신규 수집이 `apm_spans`(PostgreSQL)에 행을 추가하지 않는다.
- 데이터 모델: PostgreSQL 참조는 trace_id/span id 키만(`02:209`). 기존 `apm_spans` 처리는 TSDB-04.
- API: 트레이스 조회 APMQ-02/03.

### [TSDB-03] 로그 → Loki 저장(불변식, MUST)
- 설명: 애플리케이션/에러 로그는 Grafana Loki에 저장하고 PostgreSQL `apm_logs`에 저장하지 않는다.
- 관련 ID: [TSDB-03](신설) (`00 §2.4:63`, `§3:90`)
- 수용 기준: 신규 로그가 Loki에서 조회되고 `apm_logs`(PostgreSQL)에 행을 추가하지 않는다.
- 데이터 모델: 기존 `apm_logs` 처리는 TSDB-04.
- API: 로그 검색 APMQ-04.

### [TSDB-04] 기존 PostgreSQL apm_spans/apm_logs 이전(F-7 해소)
- 설명: Phase 1 MVP가 PostgreSQL에 둔 `apm_spans`/`apm_logs`를 시계열 저장소로 이전하고, RDB에는 불변식이 허용하는 참조 키만 남긴다. F-7(불변식 위반) 해소가 이 항목의 완료 조건이다.
- 관련 ID: [TSDB-04](신설), F-7 (`_workspace/06_invariants-reviewer_verdict.md:19,56,92`; `migrations/0003:15-39`; `apm_store.go:64-144`; `model/apm.go:33-54`)
- 수용 기준:
  - 이전 완료 후 APM 조회 경로가 PostgreSQL `apm_spans`/`apm_logs`를 읽지 않는다(코드 참조 제거 또는 참조키 전용으로 축소).
  - 데이터 모델 문서(`02 §2.5`)와 정합: RDB는 `apm_agents` + 참조 키만 보유. **테이블 제거 vs 참조키만 유지(빈 껍데기)는 §결정 필요.**
  - 마이그레이션은 재실행·하위호환 안전(기존 0009 관례처럼 `IF EXISTS`/`IF NOT EXISTS`).
- 데이터 모델: `apm_spans`/`apm_logs`(`02:211`, `migrations/0003`), `metrics_ref`(`02:152,209`).
- API: 관련 store 메서드(`InsertSpans/InsertLogs/SlowTraces/TraceByID/ListLogs`, `apm_store.go:64-144`)의 재대상화 필요.

### [APM-TENANT] 시계열 저장소 멀티테넌시 org 격리(불변식, MUST)
- 설명: 시계열 저장소(VM/Tempo/Loki)에서도 org 간 데이터가 격리되어, 한 org의 조회가 타 org 텔레메트리를 반환하지 않는다. RDB 참조 키(`metrics_ref` 등)와 `apm_agents`는 기존 RLS(`org_id` + `app.current_org`)로 보호된다.
- 관련 ID: [APM-TENANT](신설), 불변식(RLS 멀티테넌시) (`02 §3:294-297`)
- 수용 기준:
  - APM 조회 API가 항상 요청자 org 스코프의 텔레메트리만 반환한다(교차 테넌트 조회 0건).
  - `apm_agents` 및 RDB 참조 행은 `org_isolation` FORCE RLS 대상(현행 유지, `_workspace/06_...:62`).
  - 시계열 저장소 조회 시 org 스코프가 강제된다(라벨/테넌트 헤더/쿼리 필터 — 방식은 §결정 필요).
- 데이터 모델: `apm_agents.org_id`, `metrics_ref`.
- API: APMQ-* 전부. **시계열 org 격리 메커니즘(라벨 vs 테넌트 헤더 vs 인스턴스 분리)은 §결정 필요.**

### [APMQ-01] 서비스 맵 조회
- 설명: 프로젝트의 서비스 간 호출 관계·서비스별 상태를 대시보드가 소비할 형태로 제공한다.
- 관련 ID: [APMQ-01](신설), [APM-02] (`01:117` near-realtime; UI 소비용)
- 수용 기준: 트레이스에서 도출한 서비스 노드와 호출 엣지를 org 스코프로 반환한다. 데이터 없으면 빈 그래프.
- 데이터 모델: Tempo(span service/parent 관계). 현행은 `apm_spans.service/parent_span_id`(`model/apm.go:37,40`).
- API: 신설(예: `GET /projects/:id/apm/service-map`). **현행 미구현 — 03-api-spec 미기재(문서 격차, §모호/모순).**

### [APMQ-02] 레이턴시/에러율 집계 조회
- 설명: 서비스/엔드포인트별 레이턴시 분포(p50/p95/p99)와 에러율을 시간 범위로 조회한다.
- 관련 ID: [APMQ-02](신설), [APM-02]
- 수용 기준: 지정 시간창의 레이턴시 백분위·에러율이 org 스코프로 반환된다. min/max 시간 파라미터 지원.
- 데이터 모델: VictoriaMetrics(메트릭)/Tempo. RDB `load_test_results.latency_p50/p95/p99`(`02:149`)는 부하 요약용으로 별개.
- API: 신설. **현행 미구현, 03-api-spec 미기재(§모호/모순).**

### [APMQ-03] 트레이스 조회(느린 트랜잭션·단건)
- 설명: 응답 ≥3초 느린 트랜잭션 목록과 단일 trace의 span 트리를 조회한다.
- 관련 ID: [APM-03](`01:119`, ≥3s span 트리·느린 DB 쿼리 하이라이트), [APMQ-03]
- 수용 기준:
  - `min_ms`(기본 3000) 이상 지속 트레이스를 최신순으로 반환(현행 동작 유지, `apm.go:89-102`).
  - trace_id로 단건 조회 시 span 트리(부모-자식 순)를 반환하고 없으면 404(`apm.go:104-115`).
  - 조회 소스가 Tempo로 전환된다(현행 PostgreSQL, `apm_store.go:97-121`).
- 데이터 모델: Tempo. 현행 `apm_spans`(`migrations/0003:15-29`).
- API: `GET /projects/:id/apm/traces?min_ms=`(`03:74`), `GET /projects/:id/apm/traces/:traceId`(구현됨, `router.go:106` — **03-api-spec 미기재, 문서 격차**).

### [APMQ-04] 로그 검색 조회
- 설명: 프로젝트 로그를 레벨·시간·텍스트로 검색·조회한다.
- 관련 ID: [APM-03] 연계, [APMQ-04] (`03:75`)
- 수용 기준:
  - 최신순 로그를 limit(기본 100, 상한 500) 반환(현행 유지, `apm.go:117-130`, `apm_store.go:124-144`).
  - 레벨/시간창/텍스트 필터 지원(현행은 limit만 — 확장 필요).
  - 조회 소스가 Loki로 전환된다.
- 데이터 모델: Loki. 현행 `apm_logs`(`migrations/0003:31-39`).
- API: `GET /projects/:id/apm/logs`(`03:75`).

### [APM-02] near-realtime 스트리밍(≤2초)
- 설명: 수집된 메트릭을 대시보드가 실시간 구독한다(수집→표시 지연 ≤2초).
- 관련 ID: [APM-02](`01:117`)
- 수용 기준: Collector/수집기가 스트림을 Control Plane WS로 push, 대시보드가 구독해 ≤2초 내 갱신. (부하 S1의 `WS /load-tests/:id/stream` 패턴 참조, `03:66`)
- 데이터 모델: 없음(스트림).
- API: 신설 WS(예: `WS /projects/:id/apm/stream`). **현행 미구현, 03-api-spec 미기재. WS 계약 구체화는 §결정 필요.**

### [BILL-03] APM 보존 정책(플랜별)
- 설명: 플랜별 APM 보존일(Free 24h / Pro 14d / Enterprise 90d) 경과 텔레메트리를 자동 제거한다.
- 관련 ID: [BILL-03](`02 §3:298`; `plans.apm_retention_days` `02:252`)
- 수용 기준: 보존일 경과 데이터가 TSDB/Loki 리텐션 정책 또는 배치 삭제로 제거되고, 조회 결과에서 사라진다.
- 데이터 모델: `plans.apm_retention_days`(`02:252`). 시계열 저장소 리텐션.
- API: 없음(백그라운드). **Phase 3 포함 여부·리텐션 구현 방식은 §결정 필요(범위 경계).**

---

## 불변식 체크리스트 (MUST)

- [ ] **시계열 RDB 분리** — [TSDB-01~04]. 메트릭/트레이스/로그는 VM/Tempo/Loki, PostgreSQL은 `apm_agents`+참조 키만. **F-7 해소가 Phase 3 핵심 목표.** (`02:7,209`, `_workspace/06_...:92`)
- [ ] **mTLS 필수** — [MTLS-01/02]. OTLP/gRPC 수집은 상호 인증서 검증. 평문 수집 경로 미노출. (`01:47,143`; `00:64`)
- [ ] **RLS 멀티테넌시** — [APM-TENANT]. `apm_agents`·RDB 참조는 `org_id`+`app.current_org` FORCE RLS(현행 유지), 시계열 저장소도 org 격리. (`02 §3:294-297`)
- [ ] **비용 게이트 [COST-05]** — VM/Tempo/Loki/OTel Collector는 전부 OSS 셀프호스팅(₩0). Phase 3에 신규 유료 외부 의존 추가 금지. (`00:38`, `04`)
- [x] **APM 저오버헤드 [APM-01] ≤2%** — 비동기 배치·tail 샘플링(OTLP-01/02 수용 기준에 반영).
- [x] **near-realtime [APM-02] ≤2초** — [APM-02] 항목.
- N/A: 도메인 검증 게이트([SC-01])·서킷 브레이커·워커 idle=0·Ephemeral 소스 — S1/S2 범위, S3 APM 무관(단 mTLS는 공통 불변식으로 S3에서 최초 실현).

---

## architect 결정 필요 목록

> spec-analyst는 스택/HOW를 결정하지 않는다. 아래는 `architect`(`_workspace/p3_02_architect_design.md`)에서 확정할 항목.

1. **mTLS 인증서 관리(핵심)** — 로컬 Compose에서 개발 CA 발급·배포 방식. 테넌트/에이전트별 클라이언트 인증서 vs 공용 CA + ingest 토큰 병행. 인증서 수명·회전·폐기. `ingest_token`(현행)과 mTLS 인증서의 관계(대체 vs 병행). [MTLS-01/02 관련]
2. **Collector 배치(핵심)** — OTel Collector를 별도 Compose 서비스로 둘지, klaro api/worker(Go)가 OTLP를 직접 수신할지. mTLS 종단(Collector에서 종료 vs klaro에서 종료). [OTLP-01/02]
3. **시계열 org 격리(핵심)** — VM/Tempo/Loki에서 org 격리 메커니즘: 라벨/`org_id` 라벨 vs 멀티테넌트 헤더(`X-Scope-OrgID` 계열) vs 인스턴스 분리. 조회 시 스코프 강제 지점. [APM-TENANT]
4. **REST 수집 존치 여부** — 현행 `POST /apm/ingest`(REST/JSON, X-Ingest-Token) 폐기/존치/OTLP와 병행. 03-api-spec에 REST ingest가 아예 없어(문서 격차) 승격이 자연스러우나 기존 web UI(`web/index.html:1939`)·문서 스니펫 의존. [OTLP-01]
5. **기존 apm_spans/apm_logs 처리** — 테이블 제거 vs 참조키 전용 축소. 데이터 마이그레이션(기존 데모/실데이터 폐기 여부). [TSDB-04]
6. **near-realtime 파이프라인** — Collector→WS push 경로 구현(Collector가 klaro로 재전송 vs klaro가 TSDB 폴링). WS 계약(구독 스코프·페이로드). [APM-02]
7. **보존/리텐션 범위** — [BILL-03] APM 보존을 Phase 3에 포함할지, TSDB 네이티브 리텐션 vs 배치 잡. 플랜별 24h/14d/90d 실현 지점.
8. **저장소 인스턴스 구성** — VM/Tempo/Loki 각 저장 백엔드(Tempo/Loki는 MinIO vs 로컬 FS, `00:89-90`). MinIO 도입 필요 여부(현 Compose에 MinIO 없음).

---

## 모호/모순

- **[M-1] 03-api-spec에 OTLP/ingest 엔드포인트 부재** — `03-api-spec.md:72-75`는 APM으로 agents/traces/logs REST만 나열하고 **수집(ingest/OTLP) 엔드포인트가 없다.** 반면 `01 §2.4`·`00 §2.4`는 OTLP/gRPC 수집을 명시. 현행 코드는 REST `POST /apm/ingest`(문서 미기재)로 수집. → 수집 프로토콜 계약이 문서 간 불일치. 문서(01/00)의 OTLP/gRPC를 기준으로 삼되 03 갱신 필요.
- **[M-2] 구현 엔드포인트 3종이 03-api-spec 미기재** — `GET /apm/traces/:traceId`(`router.go:106`), `POST /apm/demo`(dev seed, `router.go:108`), `POST /apm/ingest`(`router.go:43`)가 문서에 없다. 데모 seed는 dev 전용이나 API 계약상 문서화 공백.
- **[M-3] 서비스 맵/레이턴시집계/WS 스트림 엔드포인트 미정의** — 대시보드가 소비할 [APMQ-01/02]·[APM-02] 스트림은 `01 §2.4`에 개념(near-realtime, 병목)만 있고 `03-api-spec`에 구체 엔드포인트가 없다. 계약에서 신설로 표기했으나 경로/스키마는 architect+03 갱신 필요.
- **[M-4] metrics_ref 상관 대상 모호** — `02:152,209`는 `load_test_results.metrics_ref`로 부하 결과를 TSDB와 상관한다고 하나, APM(에이전트 기반) 텔레메트리의 RDB 참조 키(어떤 테이블에 어떤 키를 남길지)는 명시가 없다. `apm_agents`만으로 상관 가능한지, 별도 참조 테이블이 필요한지 불명확. [TSDB-04/APM-TENANT 설계 시 확정 필요]
- **[M-5] mTLS와 X-Ingest-Token 병존 의미** — `02:206`은 `ingest_token`을 "mTLS 클라이언트 식별"이라 주석하나, 토큰(애플리케이션 계층)과 mTLS(전송 계층)는 다른 메커니즘이다. 토큰이 mTLS 내부의 org/agent 식별자로 병행되는지, mTLS 인증서 subject로 대체되는지 문서상 불명확. [architect 결정 1과 연동]
- **[M-6] SDK 언어 범위 vs 자동계측** — `apm_agents.language`는 nodejs/springboot/fastapi 3종 고정(`02:204`, `model/apm.go:12-16`)이나 OTLP 수집은 언어 불문 표준이다. language 필드가 수집 인증/라우팅에 실제로 쓰이는지(현재는 span.service 기본값 채움용, `apm.go:62-64`) 재검토 필요.
