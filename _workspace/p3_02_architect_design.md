# klaro Phase 3 — 아키텍처·구현 설계: S3 APM/관측성 실파이프라인

**작성** architect · **작성일** 2026-07-20 · **상태** 빌더 착수용(확정)
**입력** `_workspace/p3_01_spec-analyst_contract.md`, `_workspace/02_architect_design.md`, `_workspace/p2_02_architect_design.md`, `docs/klaro/00~03`, `services/load-test/**`(실측)
**확정 스택(입력)** Go · 공용 DB+RLS(Phase 1) · Docker Compose · 시계열 = VictoriaMetrics(메트릭)/Tempo(트레이스)/Loki(로그) OSS 무비용 · mTLS 필수 · MinIO(오브젝트 백엔드)
**대상 모듈** 단일 Go 모듈 `services/load-test`(`github.com/klaro/load-test`)에 **신규 패키지 `internal/otelgw`(수집 게이트웨이)·`internal/tsdb`(시계열 클라이언트)·`internal/mtls`(인증서)** 를 추가하고 `internal/api/apm.go`를 재작성한다. 신규 바이너리 `cmd/otelgw`, `cmd/gencerts` 2개. Compose에 서비스 5개(otelgw·otel-collector·victoriametrics·tempo·loki·minio) 추가.

> backend-builder는 이 문서만으로 착수할 수 있다. 결정 #1~#8 전부 확정, 되물을 항목 없음.
> **Phase 1/2 패턴 재사용**: org tx 관통(`store.RunInOrg`/`BeginOrg`, `store.go:78-101`), `Querier` 주입, sys 풀 부트스트랩(`ResolveAgentOrg`, `apm_store.go:46`), WS 스트림 패턴(`ws.go`, Redis pub/sub `Signaler`, `queue.go:28`). Phase 3는 이 위에 시계열 저장·mTLS 수집만 얹는다. RLS/tenancy 구조는 손대지 않는다.

---

## 0. 설계 요약 (한 문단)

현행 MVP(`apm.go`·`apm_store.go`·`0003`)는 span/log를 PostgreSQL `apm_spans`/`apm_logs`에 REST(`X-Ingest-Token`)로 직접 저장하는데, 이는 불변식 "시계열 RDB 밖" 위반(Phase 1 **F-7**)이다. Phase 3는 이를 **OTLP/gRPC + mTLS 수집 → 시계열 저장소 분리** 파이프라인으로 승격한다. 수집 경로는 **klaro 경량 Go 게이트웨이 `otelgw`**(신규)가 mTLS를 종단하고, 클라이언트 인증서 + OTLP 메타데이터의 `ingest_token`을 `apm_agents`(sys 풀)로 대조해 **권위 있는 `org_id`/`project_id`를 리소스 어트리뷰트로 각인**(클라이언트 위조 불가)한 뒤 내부 **OTel Collector**(별도 서비스)로 평문 전달한다. Collector는 신호별로 라우팅한다 — 메트릭+`spanmetrics`/`servicegraph` 파생 메트릭 → VictoriaMetrics(remote-write, `org_id` 라벨), 트레이스 → Tempo(`X-Scope-OrgID` 테넌트 헤더), 로그 → Loki(동 헤더). 조회 API(`apm.go` 재작성)는 `internal/tsdb`의 VM/Tempo/Loki 클라이언트로 재대상화하며 **서버측에서 요청자 org(principal.OrgID)만** 라벨/테넌트 헤더에 강제(APM-TENANT). 마이그레이션 **0010**은 `apm_spans`/`apm_logs`를 제거(dev 데이터, F-7 해소)하고 `apm_agents`에 mTLS 인증서 참조 컬럼을 추가한다. Tempo/Loki 오브젝트 백엔드로 **MinIO** 도입, VM은 로컬 디스크. 리텐션은 세 저장소 네이티브 설정으로 구현하고 플랜별 차등은 과금 페이즈로 연기(훅 지점 명시). 신규 유료 의존 0([COST-05]).

---

## 1. 결정표 (#1~#8 전부 확정)

| # | 항목 | 결정 | 근거(한 줄) | 기각 대안 |
|---|------|------|-------------|-----------|
| **#1** | mTLS 인증서 관리 | **개발 CA(자체 서명, 10y)** 를 Go 툴 `cmd/gencerts`가 init 시 `certs/` 볼륨에 생성. **공용 CA + org/agent별 클라이언트 인증서 발급**(에이전트 등록 시 klaro-api가 CA키로 서명, 90d). `ingest_token`은 **폐기하지 않고 mTLS 내부 앱계층 식별자로 병행 유지**(전송계층=mTLS, 앱계층=token). 인증서 지문은 `apm_agents.cert_fingerprint`에 바인딩. | mTLS는 "klaro CA가 발급한 유효 인증서"만 증명 → org/agent 권위·즉시 폐기는 앱계층 token+fingerprint 대조로. openssl 없이 Go 표준 `crypto/x509`라 Windows 개발 환경 이식성 확보. (M-5 해소) | 인증서 subject만으로 org 식별 → 폐기에 CRL/OCSP 필요(로컬 과설계). token 폐기 → 즉시 폐기·감사 수단 상실. |
| **#2** | Collector 배치 | **klaro Go 게이트웨이 `otelgw`(mTLS 종단·org 각인) + OTel Collector(신호 라우팅) 2단**. 게이트웨이가 mTLS 종단 후 내부 평문(도커 네트워크)으로 Collector에 OTLP 전달. | org 강제(cert/token→org_id 대조 + `apm_agents` DB 조회)는 stock Collector로 불가(커스텀 distro 빌드 필요) → 경량 Go 게이트웨이가 단순. 신호 fan-out·샘플링·spanmetrics는 검증된 Collector가 담당. | Collector 단독 mTLS 종단 → cert→org 매핑에 커스텀 authenticator/ocb 빌드 필요(무겁고 DB 접근 어려움). klaro-api가 OTLP 직접 수신 → api 프로세스에 대용량 텔레메트리 트래픽 혼입, fan-out 재구현. |
| **#3** | 시계열 org 격리 | **메트릭 = VM에 `org_id` 라벨**(게이트웨이가 리소스 어트리뷰트 각인 → Collector가 라벨화). **트레이스/로그 = Tempo/Loki 네이티브 멀티테넌시 `X-Scope-OrgID: <org_id>` 헤더**. 조회 API가 **서버측에서 요청자 org(principal.OrgID)** 로만 라벨 매처/테넌트 헤더 강제 — 클라이언트는 org 파라미터를 지정할 수 없다. | VM 단일노드는 라벨 격리가 자연스럽고, Tempo/Loki는 X-Scope-OrgID 네이티브 지원. 강제 지점이 klaro 서버 코드라 위조 불가. (APM-TENANT) | 인스턴스 org별 분리 → 테넌트 수만큼 프로세스 폭증(무비용 위반). 클라이언트 org 파라미터 신뢰 → 교차 테넌트 조회 위험. |
| **#4** | REST ingest 존치 | OTLP/gRPC를 **정식 수집 경로로 승격**. 기존 `POST /projects/:id/apm/ingest`(REST/JSON)는 **`APM_REST_INGEST=1`(기본 off) dev 플래그 뒤로 축소**, 켜지면 게이트웨이 내부 OTLP 경로로 변환 전달. `POST /apm/demo` seed는 **dev 유지**하되 합성 텔레메트리를 내부 OTLP로 주입(PG 미기록). | 문서(01/00)가 OTLP/gRPC 기준이고 03-api-spec에 REST ingest가 아예 없음(M-1) → 승격이 자연스러움. web UI·기존 스니펫 호환은 dev 플래그로 유예. | REST 완전 폐기 → 기존 dev 흐름·데모 즉시 파손. REST 정식 병행 → 평문/토큰 수집 경로가 mTLS 불변식과 충돌. |
| **#5** | apm_spans/apm_logs 처리 | **마이그레이션 0010에서 두 테이블 DROP**(dev 데이터, 실데이터 없음). RLS 정책은 테이블과 함께 소멸. `apm_agents`는 유지 + **mTLS 참조 컬럼 신설**(`cert_fingerprint`, `cert_expires_at`, `revoked_at`). 관련 store 메서드(`InsertSpans/InsertLogs/SlowTraces/TraceByID/ListLogs`)는 **PG에서 제거**하고 `internal/tsdb`로 재대상화. | RDB에 `apm_agents`+참조 키만 남기는 것이 문서(`02 §2.5`) 정합·F-7 해소 완료 조건. dev 데이터라 보존 불필요. (TSDB-04) | 빈 껍데기 유지 → 사문화 테이블·혼동, 코드가 실수로 재기록할 여지. |
| **#6** | near-realtime | **`WS /projects/:id/apm/stream`**. klaro-api가 구독 활성 시 **2s 티커로 VM instant query 폴링 → JSON 프레임 push**(S1 `ws.go` 패턴 재사용). 게이트웨이→Redis tee push는 최적화 훅으로 명시. | 폴링은 신규 인프라 0·디커플드·≤2s 충족([APM-02]). VM instant query는 저비용. | Collector→klaro push 재전송 → Collector 커스텀 exporter/추가 큐 필요(과설계). |
| **#7** | 리텐션 | **세 저장소 네이티브 리텐션**으로 기본 보존 구현: VM `-retentionPeriod=90d`, Tempo `block_retention=2160h`, Loki `retention_period=2160h`(+compactor). 플랜별(24h/14d/90d) 차등은 **과금 페이즈로 연기**하되 훅 지점 명시(§7.3). | 네이티브 리텐션이 무비용·무배치로 기본 보존 충족. 플랜 차등은 `plans.apm_retention_days`(`02:252`)가 과금 페이즈 대상. (BILL-03 경계) | Phase 3에 배치 삭제 잡 구현 → 플랜 모델·과금 미착수 상태라 조기 결합. |
| **#8** | 저장소 백엔드 | **MinIO 도입**(S4 리포트에도 필요). Tempo·Loki 오브젝트 백엔드로 MinIO(S3 API), **VM은 로컬 디스크 볼륨**. compose에 `minio` + 버킷 초기화(`minio-init`) 추가. | Tempo/Loki는 S3 백엔드가 표준·운영 단순, MinIO는 프로덕션 S3 전환 무변경(`00:89-91`). VM은 로컬 TSDB가 기본·고성능. 전부 OSS ₩0. | 전부 로컬 FS → Tempo/Loki 로컬 백엔드는 비권장·S4 전환비용. 전부 MinIO(VM 포함) → VM S3 백엔드 비표준. |

### 1.1 모호/모순(M-1~M-6) 해소 방침

| ID | 모순 | 해소 |
|----|------|------|
| **M-1** | 03-api-spec에 OTLP/ingest 엔드포인트 부재, 01/00은 OTLP/gRPC 명시 | 문서(01/00)의 **OTLP/gRPC를 정식 수집 계약**으로 확정(#4). 03-api-spec §3.x에 "수집=OTLP/gRPC(게이트웨이 `:4317`, mTLS), REST ingest는 dev 플래그" 절 신설(§6.7 갱신 대상). |
| **M-2** | `traces/:traceId`·`apm/demo`·`apm/ingest` 미기재 | 03에 `GET .../traces/:traceId`(Tempo 단건), `POST .../apm/demo`(dev seed), 수집 프로토콜 명시. ingest(REST)는 "dev 전용, 기본 비활성" 주석. |
| **M-3** | service-map/latency집계/WS 스트림 미정의 | §6에 경로·요청/응답 shape 확정, 03-api-spec 신설(§6.7 갱신 대상). |
| **M-4** | APM 텔레메트리 RDB 참조 키 모호 | **별도 참조 테이블 불요**로 확정. 상관 키 = `org_id`(라벨/테넌트) + `project_id`(라벨/`service.namespace`) + `service.name`이며 `apm_agents`가 RDB 앵커. `load_test_results.metrics_ref`는 부하 요약 전용으로 별개 유지. S4 조인은 project_id+시간창으로 TSDB 질의(후속). |
| **M-5** | mTLS vs ingest_token 병존 의미 | **병행 확정**(#1): mTLS=전송계층 인증(CA 발급 증명), `ingest_token`=앱계층 agent 식별(OTLP 메타데이터) → `apm_agents` 조회로 권위 org/project 결정, `cert_fingerprint`로 token↔인증서 바인딩·즉시 폐기. `02:206` 주석을 "mTLS 클라이언트 식별(앱계층 토큰, 인증서와 병행)"로 갱신. |
| **M-6** | language 3종 고정 vs OTLP 언어불문 | `apm_agents.language`는 **에이전트 등록 메타·SDK 안내용**으로만 유지(수집 인증/라우팅에 미사용). OTLP 수집은 언어 불문. 라우팅·귀속은 전적으로 cert+token+org_id로 수행. 검증: 등록 시 enum 체크(`apm.go:22-26`)는 유지, 수집 경로는 language 미참조. |

---

## 2. 수집 파이프라인 아키텍처 (핵심)

### 2.1 전 구간 데이터 흐름 + org 스코프 강제 지점

```
[고객 앱 + OTel SDK(klaro-apm)]
   │  OTLP/gRPC + mTLS  (클라이언트 인증서 + metadata: x-ingest-token=<token>)
   │  :4317
   ▼
┌──────────────────────────────────────────────────────────────────┐
│ klaro-otelgw  (신규 Go, internal/otelgw)          ★org 강제 지점 1  │
│ 1. mTLS 종단: RequireAndVerifyClientCert, ClientCAs=devCA          │
│ 2. metadata x-ingest-token 추출 → apm_agents 조회(sys 풀, BYPASSRLS)│
│    → 권위 org_id/project_id, revoked_at IS NULL, cert_fingerprint  │
│    == 제시 인증서 지문 대조 (불일치/폐기 → codes.Unauthenticated)   │
│ 3. 리소스 어트리뷰트 각인(덮어쓰기): org_id, project_id,           │
│    service.namespace=project_id  (클라이언트 위조분 무시)          │
│ 4. gRPC metadata 세팅: X-Scope-OrgID=<org_id>                       │
│ 5. last_seen_at 갱신(sys 풀)                                       │
└───────────────────────────┬────────────────────────────────────────┘
     내부 OTLP/gRPC(평문, 도커 네트워크 klaro-obsv-net)  otel-collector:4319
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│ otel-collector (contrib)                          ★org 강제 지점 2  │
│ receivers.otlp{ include_metadata:true }                            │
│ connectors: spanmetrics(dims: org_id,project_id,service.name),     │
│             servicegraph(dims: org_id,project_id)                  │
│ extensions: headers_setter(from_context X-Scope-OrgID)             │
│ exporters:                                                         │
│   metrics → prometheusremotewrite → victoriametrics:8428           │
│             (resource_to_telemetry_conversion → org_id 라벨)       │
│   traces  → otlp/tempo (X-Scope-OrgID 헤더 propagate)              │
│   logs    → loki       (X-Scope-OrgID 헤더 propagate)              │
└──────┬───────────────────────┬──────────────────────┬─────────────┘
       ▼                       ▼                      ▼
 VictoriaMetrics          Tempo                    Loki
 (org_id 라벨,          (테넌트=org_id,           (테넌트=org_id,
  로컬 디스크)           MinIO 백엔드)             MinIO 백엔드)
       ▲                       ▲                      ▲
       │ PromQL(+org_id 매처)   │ TraceQL(+X-Scope-OrgID) │ LogQL(+X-Scope-OrgID)
       │                       │                      │
┌──────┴───────────────────────┴──────────────────────┴─────────────┐
│ klaro-api  internal/tsdb + internal/api/apm.go     ★org 강제 지점 3  │
│ 조회 핸들러가 principal.OrgID(서버 세션)를 org 스코프로 강제        │
│ - VM: PromQL 라벨 매처 org_id="<principal.OrgID>" 주입             │
│ - Tempo/Loki: HTTP 헤더 X-Scope-OrgID=<principal.OrgID> 주입       │
│ - 클라이언트는 org를 절대 지정 불가 (경로/쿼리에 org 파라미터 없음)│
└────────────────────────────────────────────────────────────────────┘
       │ WS(2s 폴링→push)
       ▼
   [대시보드]
```

**org 스코프 3중 강제**: (1) 수집 시 게이트웨이가 권위 org_id 각인(APM-TENANT 귀속), (2) 저장 시 VM 라벨·Tempo/Loki 테넌트로 물리 격리, (3) 조회 시 klaro-api가 요청자 org로만 매처/헤더 강제(교차 테넌트 조회 0건). 세 지점 중 어느 하나만 뚫려도 격리가 유지되도록 방어 심층화.

### 2.2 게이트웨이 상세 (`internal/otelgw`)

- **서버**: `google.golang.org/grpc` + `otlpgrpc`(`go.opentelemetry.io/collector/pdata` 또는 OTLP proto). OTLP 3서비스 구현: `TraceService`/`MetricsService`/`LogsService` Export. 각 Export는 pdata를 받아 리소스 어트리뷰트 각인 후 다운스트림 Collector로 `otlpgrpc` 클라이언트로 재전송.
- **TLS**: `tls.Config{ ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: devCAPool, Certificates: [serverCert] }`. 평문 리스너 미개방(MTLS-01 수용: 비-TLS 경로 부재).
- **org 해석**: gRPC `metadata.FromIncomingContext` → `x-ingest-token`. `store.ResolveAgentOrg(projectFromToken?, token)` 재사용/확장. **인증서 지문 대조**: `peer.FromContext` → `tls.ConnectionState.PeerCertificates[0]` SHA-256 지문 == `apm_agents.cert_fingerprint`. 불일치·`revoked_at IS NOT NULL` → `status.Error(codes.Unauthenticated)`. (인증 실패 텔레메트리 수용 카운트 0 — OTLP-01 수용)
- **비동기·저오버헤드**: 게이트웨이는 검증·각인만 하고 즉시 다운스트림 async 전송(대상 앱 블로킹 없음, [APM-01]). tail 샘플링은 Collector `tail_sampling` 프로세서로.
- **바이너리**: `cmd/otelgw/main.go` — `OTELGW_ADDR=:4317`, `COLLECTOR_ADDR=otel-collector:4319`, `CERT_DIR=/certs`, DB DSN(sys 풀). Store는 sys 풀만 사용(수집은 org 스코프 확정 이전 부트스트랩, D-10 연장).

### 2.3 mTLS 종단·인증서 검증 흐름 (순서)

1. SDK가 TLS 핸드셰이크 시 klaro CA가 발급한 클라이언트 인증서 제시 → 게이트웨이가 `ClientCAs`로 체인 검증. **실패 시 핸드셰이크 거부**(평문 경로 없음, MTLS-01 수용).
2. 서버 인증서(게이트웨이)는 SDK가 CA 신뢰로 검증(양방향).
3. 앱계층: OTLP 메타데이터 `x-ingest-token` → `apm_agents`(sys) 조회 → org/project + 기대 지문.
4. 제시 인증서 지문 == 기대 지문 대조(token↔cert 바인딩). 통과 시에만 각인·전달.

---

## 3. mTLS 인증서 관리 (핵심)

### 3.1 개발 CA 생성 (`cmd/gencerts`)

Go 표준 `crypto/x509`·`crypto/ecdsa`만 사용(openssl 불요 → Windows 개발 이식). 최초 `docker compose up` 시 `certs-init` 서비스(또는 `make certs`)가 실행:

```
cmd/gencerts/main.go 산출물 (→ certs/ 볼륨)
  ca.crt / ca.key             # 개발 루트 CA (ECDSA P-256, 10y). ca.key는 klaro-api·gencerts만 접근
  server.crt / server.key     # otelgw 서버 인증서 (SAN: otelgw, localhost, 127.0.0.1; 1y)
  agent-dev.crt/.key          # dev 기본 에이전트 클라이언트 인증서 (SAN URI: klaro://org/<devOrg>/agent/dev; 90d)
```

- 멱등: 파일 존재 시 재생성 스킵(`--force`로 재발급). CA는 리포지토리 커밋 금지(`.gitignore certs/`), 볼륨에만.
- `agent-dev`는 dev seed org(`00..01`)의 데모 에이전트에 매핑 — `POST /apm/demo`·web UI가 이 인증서로 수집 테스트.

### 3.2 에이전트별 클라이언트 인증서 발급 (MTLS-02)

`POST /projects/:id/apm/agents {language}` 확장:
1. 기존대로 `apm_agents` 행 생성 + `ingest_token` 발급.
2. **추가**: klaro-api가 CA키(`/certs/ca.key`, 읽기전용 마운트)로 이 에이전트용 클라이언트 인증서 서명. SAN URI = `klaro://org/<org_id>/agent/<agent_id>`, 유효 90d.
3. `apm_agents.cert_fingerprint`(SHA-256), `cert_expires_at` 기록.
4. 응답에 `{ id, ingest_token, client_cert(PEM), client_key(PEM), ca_cert(PEM) }` **1회 반환**(key는 재조회 불가). SDK 설정에 그대로 주입.

### 3.3 수명·회전·폐기 규칙

| 대상 | 수명(dev) | 회전 | 폐기 |
|------|-----------|------|------|
| 개발 CA | 10y | `gencerts --force` 재발급 | (dev 전역 리셋 시만) |
| 서버(otelgw) | 1y | 재발급 + 게이트웨이 재기동 | — |
| 에이전트 클라이언트 | 90d | 재등록(신규 발급) 또는 `POST .../agents/:id/rotate`(선택, 후속) | **`apm_agents.revoked_at` set** → 게이트웨이 앱계층 대조에서 즉시 거부(CRL 불요) |

- **폐기가 즉시 유효한 이유**: mTLS는 "유효 CA 인증서" 여부만 보므로 인증서 자체 폐기(CRL/OCSP)는 로컬 과설계. 대신 게이트웨이가 매 수집마다 `apm_agents`(token+fingerprint+revoked_at) 대조 → org/agent 삭제·폐기가 다음 요청부터 반영. 이것이 `ingest_token` 병행 유지의 핵심 근거(M-5).
- **프로덕션 승격 노트**: 개발 CA → 사설 PKI(예: AWS Private CA / cert-manager / SPIFFE-SPIRE). `ca.key`는 KMS/HSM. 인증서 자동 회전(short-lived, SPIFFE SVID). CRL/OCSP는 조직 정책에 따라 추가. 코드 인터페이스(`mtls.Issuer`, `mtls.Verifier`)를 두어 발급자 교체가 국소화되게 설계.

### 3.4 Compose 배포(볼륨/시크릿)

- 명명 볼륨 `certs` 를 `certs-init`(생성)·`otelgw`(server+ca 읽기)·`api`(ca.crt+ca.key 읽기, 발급용)에 마운트.
- `otelgw` 는 `server.crt/key`+`ca.crt`(ClientCAs), `api`는 `ca.crt`+`ca.key`(서명), SDK(고객측)는 발급받은 `client.crt/key`+`ca.crt`.
- 시크릿은 `.env`/SOPS 대상 아님(로컬 자체 서명). 프로덕션은 Secrets Manager(§3.3 노트).

---

## 4. 데이터 모델·마이그레이션 (0010)

### 4.1 `migrations/0010_apm_tsdb.sql`

기존 러너는 `migrations/*.sql` 사전순 적용(0001…0010, initdb는 슈퍼유저 `klaro` → RLS 우회). 재실행·하위호환 안전(`IF EXISTS`/`IF NOT EXISTS`).

```sql
-- F-7 해소: 시계열은 VM/Tempo/Loki 로 이전. RDB는 apm_agents + 참조 키만.
-- dev 데이터(실데이터 없음) → 테이블 제거. RLS 정책은 테이블과 함께 소멸.
DROP TABLE IF EXISTS apm_spans;
DROP TABLE IF EXISTS apm_logs;

-- apm_agents: mTLS 인증서 참조 컬럼 추가(발급 지문·만료·폐기). org_id/RLS(0005/0006) 유지.
ALTER TABLE apm_agents ADD COLUMN IF NOT EXISTS cert_fingerprint text; -- SHA-256(client cert), token↔cert 바인딩
ALTER TABLE apm_agents ADD COLUMN IF NOT EXISTS cert_expires_at  timestamptz;
ALTER TABLE apm_agents ADD COLUMN IF NOT EXISTS revoked_at       timestamptz; -- set 시 게이트웨이 수집 거부
CREATE INDEX IF NOT EXISTS idx_apm_agents_fingerprint ON apm_agents(cert_fingerprint);
```

- **org_id/RLS**: `apm_agents`는 0005/0006에서 이미 `org_id` + `org_isolation` FORCE RLS 보유. ADD COLUMN은 정책 무영향 → 신규 컬럼도 자동 org 스코프 보호. `ResolveAgentOrg`(sys 풀)는 D-10 부트스트랩이라 그대로.
- **DROP 재적용**: 기존 `0006_rls.sql:13-14`가 `apm_spans`/`apm_logs`를 RLS 배열에 포함하는데, initdb 는 0006 → 0010 순서라 0006 실행 시점엔 테이블 존재(정상), 0010이 이후 DROP. `docker compose down -v && up`으로 initdb 전체 재적용 시 문제 없음. **주의**: 기존 볼륨 유지 채 0010만 수동 적용하려면 `docker compose down -v` 권장(마이그레이션 변경 관례, `docker-compose.yml:11` 주석).
- **참조 키(M-4)**: 별도 테이블 신설 없음. 상관 키 = `org_id`+`project_id`+`service.name`(전부 TSDB 라벨/테넌트/리소스 어트리뷰트). `apm_agents`가 RDB 앵커. S4 조인 훅은 §7.3.

### 4.2 문서 갱신 대상 (링크 그래프 정합)

- `docs/klaro/02-data-model.md §2.5`: `apm_spans`/`apm_logs` "MVP 임시 저장" 문단 제거(→ "0010에서 제거, TSDB 이전 완료"), `apm_agents`에 `cert_fingerprint`/`cert_expires_at`/`revoked_at` 행 추가, `ingest_token` 비고를 M-5 해소 문구로 갱신.
- `docs/klaro/03-api-spec.md §2/§3`: APM 수집=OTLP/gRPC(게이트웨이) 명시, service-map/metrics/traces:traceId/stream/logs 필터 신설(§6.7).

---

## 5. Go 패키지·모듈 설계

### 5.1 신규 `internal/tsdb` (시계열 클라이언트 추상화)

스캐너(`internal/scanner`)처럼 **DB·gin 무지**, org_id를 인자로 받아 스코프 강제. 순수 HTTP 클라이언트.

```
internal/tsdb/
  tsdb.go        # 공통 타입(TimeRange, ServiceNode, Edge, LatencyStat, TraceSummary, SpanTree, LogLine), 인터페이스
  vm.go          # VMClient: PromQL query/query_range (VM :8428 /prometheus/api/v1/*)
  tempo.go       # TempoClient: TraceQL 검색 /api/search, 단건 /api/traces/{id} (X-Scope-OrgID)
  loki.go        # LokiClient: LogQL query_range /loki/api/v1/query_range (X-Scope-OrgID)
  *_test.go      # 각 저장소 JSON 응답 픽스처 파싱 유닛테스트(네트워크 불필요)
```

**인터페이스(작은 경계, org_id 필수 인자)**:

```go
type APMReader interface {
    ServiceMap(ctx context.Context, orgID, projectID string, tr TimeRange) (ServiceGraph, error)     // APMQ-01: VM servicegraph 메트릭
    Latency(ctx context.Context, orgID, projectID, service string, tr TimeRange) ([]LatencyStat, error) // APMQ-02: VM spanmetrics
    SlowTraces(ctx context.Context, orgID, projectID string, minMs float64, tr TimeRange) ([]TraceSummary, error) // APMQ-03: Tempo TraceQL
    Trace(ctx context.Context, orgID, projectID, traceID string) (*SpanTree, error)                  // APMQ-03 단건: Tempo
    SearchLogs(ctx context.Context, orgID, projectID string, f LogFilter) ([]LogLine, error)          // APMQ-04: Loki
    LiveMetrics(ctx context.Context, orgID, projectID string) (LiveSnapshot, error)                   // APM-02 WS 폴링용
}
```

- **org 강제**: 모든 메서드가 `orgID`를 첫 인자로 받아 **VM은 PromQL에 `{org_id="<orgID>",project_id="<projectID>"}` 라벨 매처 주입**, **Tempo/Loki는 `X-Scope-OrgID: <orgID>` 헤더 + `{project_id="<projectID>"}` 셀렉터 주입**. 호출자는 항상 `principal.OrgID` 전달(§6). 클라이언트 지정 불가.
- 구현체 `NewVMClient(baseURL)`, `NewTempoClient(baseURL)`, `NewLokiClient(baseURL)`. 하나의 `APMStore` 파사드가 셋을 조합해 `APMReader` 구현.

### 5.2 `internal/otelgw` (수집 게이트웨이) — §2.2

```
internal/otelgw/
  server.go    # gRPC OTLP 서버(mTLS), Export 핸들러(trace/metric/log)
  resolve.go   # metadata token + cert 지문 → org/project 해석(store.sys)
  stamp.go     # pdata 리소스 어트리뷰트 각인(org_id/project_id/service.namespace) + X-Scope-OrgID metadata
  forward.go   # 다운스트림 Collector otlpgrpc 클라이언트(async)
```

### 5.3 `internal/mtls` (인증서 발급/검증 헬퍼)

```
internal/mtls/
  ca.go        # LoadCA, GenerateCA (cmd/gencerts 공용)
  issue.go     # IssueClientCert(caCert, caKey, org, agent) → PEM, fingerprint  (agents 발급용)
  verify.go    # Fingerprint(cert) helper, LoadServerTLS
```

### 5.4 `internal/api/apm.go` 재작성

- `createAgent`: 기존 로직 + `mtls.IssueClientCert` 호출 → `cert_fingerprint`/`cert_expires_at` 저장, 응답에 cert/key/ca PEM 포함(1회).
- `listAgents`: 기존 유지(+`cert_expires_at`/`revoked_at` 노출), `last_seen_at` 유지.
- `listSlowTraces`→`SlowTraces`(TempoClient), `getTrace`→`Trace`(Tempo), `listApmLogs`→`SearchLogs`(Loki, 필터 확장), **신규** `getServiceMap`(VM), `getApmMetrics`(VM). 전부 `d.APM.<메서드>(c, principal.OrgID, projectID(c), ...)` 호출. `tenancy.Tx(c)`는 `apm_agents` 조회에만(TSDB 조회는 tx 불요).
- `apmIngest`(REST): `APM_REST_INGEST=1`일 때만 등록, 내부 OTLP 변환 전달. 기본 404.
- `seedApmDemo`: 합성 텔레메트리를 내부 OTLP 경로로 주입(PG 미기록). dev 유지.
- **삭제**: `store` 의존 span/log 삽입·조회. `demoTelemetry` 헬퍼는 OTLP pdata 빌더로 이관.

### 5.5 `internal/store/apm_store.go` 정리

- **제거**: `InsertSpans`, `InsertLogs`, `SlowTraces`, `TraceByID`, `ListLogs`, `scanSpans`(PG span/log 의존 전부).
- **유지**: `CreateAgent`(+cert 컬럼), `ListAgents`(+cert 컬럼), `ResolveAgentOrg`(+지문·revoked 반환, 게이트웨이용).
- `model/apm.go`: `ApmSpan`/`ApmLog`/`FilterSlowSpans` 제거(또는 tsdb 타입으로 대체). `ApmAgent`에 `CertFingerprint`/`CertExpiresAt`/`RevokedAt` 필드 추가. **주의**: `FilterSlowSpans` 참조 테스트가 있으면 tsdb 타입 기준으로 재작성.

### 5.6 WS 스트림 (`internal/api/ws.go` 확장 or `apm_ws.go`)

```go
r.GET("/projects/:id/apm/stream", authenticate→resolveOrg→authorize(V) 상당, func(c) {
    // WS는 인증 그룹 밖(ws.go 패턴). 쿼리 토큰/헤더로 principal·org 확정 후 upgrade.
    orgID := principal.OrgID; projectID := c.Param("id")
    conn := upgrade()
    ticker := time.NewTicker(2 * time.Second)  // ≤2s (APM-02)
    for { select {
      case <-ticker.C:
        snap, _ := d.APM.LiveMetrics(ctx, orgID, projectID) // VM instant query
        conn.WriteJSON(snap)
      case <-closed / <-ctx.Done(): return
    }}
})
```

- WS 인증: 기존 `registerWS`는 인증 그룹 밖이므로 S1처럼 loadTestID 소유는 RLS가 담당하나, APM 스트림은 org 스코프가 필수 → **쿼리 파라미터 `?token=<jwt>` 또는 `Sec-WebSocket-Protocol` 헤더로 principal 확정**(브라우저 WS 헤더 제약 회피). 확정 org로만 VM 폴링. (S1 stream이 인증 없이 열려있다면 별도 이슈지만 Phase 3 범위 밖 — APM stream은 org 강제 필수라 인증 추가.)
- **최적화 훅(#6)**: 후속에 게이트웨이가 수집 메트릭 요약을 Redis `apm:stream:<org>:<project>`로 tee → WS가 `Signaler.SubscribeMetrics` 유사 구독. 폴링을 push로 대체.

---

## 6. API 표면 (요청/응답 shape + org 스코프)

모든 경로는 기존 org 스코프 그룹(`authenticate→resolveOrg→tenancyTx→authorize`) 하. **org 파라미터는 어디에도 노출하지 않으며**, 서버가 `principal.OrgID`를 tsdb 클라이언트에 강제 주입(APM-TENANT).

### 6.1 수집 (OTLP/gRPC) — [OTLP-01/02, MTLS-01]
- 엔드포인트: `otelgw:4317` (mTLS). REST 아님. klaro-api 라우터 밖(별도 프로세스).
- 표준 `ExportTraceServiceRequest`/`ExportMetricsServiceRequest`/`ExportLogsServiceRequest` 수용.
- (dev) `POST /projects/:id/apm/ingest` — `APM_REST_INGEST=1`에서만. 기본 404.

### 6.2 [APMQ-01] 서비스 맵 — 신설
```
GET /projects/:id/apm/service-map?from=<rfc3339>&to=<rfc3339>   (viewer)
200 { "nodes":[{"service":"api-gateway","error_rate":0.01,"req_rate":42.3}],
      "edges":[{"from":"api-gateway","to":"orders-svc","call_rate":40.1,"error_rate":0.02}] }
```
- 소스: VM `servicegraph` 메트릭 `traces_service_graph_request_total{org_id,project_id}` (edges) + `spanmetrics` `calls_total`(nodes). 데이터 없으면 빈 그래프(`{nodes:[],edges:[]}`).

### 6.3 [APMQ-02] 레이턴시/에러율 — 신설
```
GET /projects/:id/apm/metrics?service=<svc>&from=&to=   (viewer)
200 { "service":"orders-svc",
      "latency":{"p50":120,"p95":880,"p99":3200},   // ms
      "error_rate":0.03, "req_rate":42.3 }
```
- 소스: VM PromQL `histogram_quantile(0.95, sum(rate(duration_milliseconds_bucket{org_id,project_id,service_name}[5m])) by (le))` 등. `service` 생략 시 프로젝트 전체 집계.

### 6.4 [APMQ-03] 트레이스 — 재대상화(Tempo)
```
GET /projects/:id/apm/traces?min_ms=3000&from=&to=   (viewer)  현행 동작 유지(기본 3000, 최신순)
200 { "data":[{"trace_id":"...","root_service":"api-gateway","root_name":"GET /orders",
               "duration_ms":3480,"status":"ok","start":"...","span_count":7}] }
GET /projects/:id/apm/traces/:traceId   (viewer)
200 { "trace_id":"...","spans":[{span_id,parent_span_id,service,name,duration_ms,status,ts}] }  // 부모→자식
404 trace not found (현행 유지)
```
- 소스: Tempo `GET /api/search?q={duration > <min_ms>ms}&start=&end=`(TraceQL), 단건 `GET /api/traces/{traceId}`. `X-Scope-OrgID: <principal.OrgID>`. span 트리 정렬은 klaro가 parent 관계로 재구성(현행 `TraceByID` 정렬 계약 유지).

### 6.5 [APMQ-04] 로그 검색 — 재대상화(Loki)
```
GET /projects/:id/apm/logs?level=&q=&from=&to=&limit=100   (viewer)  limit 기본100·상한500(현행 유지)
200 { "data":[{"level":"error","message":"upstream timeout...","ts":"...","service":"..."}] }
```
- 소스: Loki `GET /loki/api/v1/query_range?query={project_id="<pid>"} |= "<q>" | level="<level>"&limit=`. `X-Scope-OrgID`. level/q/시간창 필터 확장([APMQ-04] 수용).

### 6.6 [APM-02] near-realtime WS — 신설
```
WS /projects/:id/apm/stream?token=<jwt>   (viewer)  ≤2s 프레임
frame: { "ts":"...", "req_rate":42.3, "error_rate":0.03, "p95_ms":880, "active_services":5 }
```

### 6.7 03-api-spec.md 갱신 대상 (M-1~M-3)
- §2 표: APM 행에 service-map/metrics/traces:traceId/stream 추가, 수집=OTLP/gRPC 주석.
- §3.x: APM 절 신설 — 수집 프로토콜(OTLP/gRPC+mTLS), 위 6.2~6.6 요청/응답, org 스코프(서버 강제·클라 미지정) 명시. `POST /apm/demo`(dev), REST ingest(dev 플래그) 주석.

---

## 7. Docker / 인프라 변경

### 7.1 `docker-compose.yml` 추가 서비스 (내부 네트워크 `klaro-obsv-net`)

```yaml
  certs-init:                         # 최초 1회 CA/서버/dev-agent 인증서 생성(멱등)
    build: { context: ., dockerfile: Dockerfile.gencerts }  # or api 이미지 재사용 + entrypoint gencerts
    command: ["gencerts","--dir","/certs"]
    volumes: [ "certs:/certs" ]

  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    environment: { MINIO_ROOT_USER: klaro, MINIO_ROOT_PASSWORD: klaro-minio }
    ports: ["9000:9000","9001:9001"]
    volumes: [ "minio_data:/data" ]
    healthcheck: { test: ["CMD","mc","ready","local"], interval: 5s, retries: 10 }

  minio-init:                         # tempo/loki 버킷 생성
    image: minio/mc:latest
    depends_on: { minio: { condition: service_healthy } }
    entrypoint: >
      /bin/sh -c "mc alias set m http://minio:9000 klaro klaro-minio &&
                  mc mb -p m/tempo m/loki m/reports; exit 0"

  victoriametrics:
    image: victoriametrics/victoria-metrics:latest
    command: ["-storageDataPath=/vmdata","-retentionPeriod=90d","-httpListenAddr=:8428"]
    volumes: [ "vm_data:/vmdata" ]
    # remote-write 수신: http://victoriametrics:8428/api/v1/write, 조회: /prometheus/api/v1/*

  tempo:
    image: grafana/tempo:latest
    command: ["-config.file=/etc/tempo/tempo.yaml"]
    volumes: [ "./deploy/tempo.yaml:/etc/tempo/tempo.yaml:ro" ]
    depends_on: { minio-init: { condition: service_completed_successfully } }
    # multitenancy_enabled:true, S3 백엔드=minio/tempo, block_retention=2160h

  loki:
    image: grafana/loki:latest
    command: ["-config.file=/etc/loki/loki.yaml"]
    volumes: [ "./deploy/loki.yaml:/etc/loki/loki.yaml:ro" ]
    depends_on: { minio-init: { condition: service_completed_successfully } }
    # auth_enabled:true(X-Scope-OrgID 필수), S3 백엔드=minio/loki, retention_period=2160h

  otel-collector:
    image: otel/opentelemetry-collector-contrib:latest
    command: ["--config=/etc/otelcol/config.yaml"]
    volumes: [ "./deploy/otelcol.yaml:/etc/otelcol/config.yaml:ro" ]
    depends_on: [ victoriametrics, tempo, loki ]
    # 내부 수신 :4319(평문, 게이트웨이 전용), export → VM/Tempo/Loki

  otelgw:                             # klaro mTLS 수집 게이트웨이(신규)
    build: { context: ., dockerfile: Dockerfile.otelgw }
    environment:
      OTELGW_ADDR: ":4317"
      COLLECTOR_ADDR: "otel-collector:4319"
      CERT_DIR: "/certs"
      SYSTEM_DATABASE_URL: postgres://klaro_system:klaro_system@postgres:5432/klaro?sslmode=disable
    ports: ["4317:4317"]             # 외부 SDK 수집(mTLS)
    volumes: [ "certs:/certs:ro" ]
    depends_on:
      postgres: { condition: service_healthy }
      certs-init: { condition: service_completed_successfully }
      otel-collector: { condition: service_started }

  api:
    # ...(기존)... 인증서 발급용 CA 마운트 추가
    volumes:
      - scan_src:/scan-src
      - certs:/certs:ro              # ca.crt + ca.key (에이전트 인증서 서명)
    environment:
      # ...(기존)...
      VM_ADDR: http://victoriametrics:8428
      TEMPO_ADDR: http://tempo:3200
      LOKI_ADDR: http://loki:3100
      CA_DIR: /certs
      APM_REST_INGEST: "0"           # dev REST ingest 기본 비활성(#4)

volumes:
  klaro_pgdata:
  scan_src: { ... }                  # 기존
  certs:                             # CA/서버/에이전트 인증서
  minio_data:
  vm_data:
```

- `deploy/` 신규: `otelcol.yaml`(receivers.otlp:4319 include_metadata, connectors spanmetrics/servicegraph dims=org_id,project_id,service.name, extensions headers_setter from_context X-Scope-OrgID, exporters prometheusremotewrite/otlp(tempo)/loki), `tempo.yaml`(multitenancy_enabled, S3=minio), `loki.yaml`(auth_enabled true, S3=minio).
- 신규 Dockerfile: `Dockerfile.otelgw`(멀티스테이지 Go build `cmd/otelgw`), `Dockerfile.gencerts`(또는 api 이미지 + gencerts 바이너리 동봉).

### 7.2 Collector 핵심 설정 요지 (`deploy/otelcol.yaml`)
- `receivers.otlp.protocols.grpc { endpoint: 0.0.0.0:4319, include_metadata: true }`
- `connectors.spanmetrics { dimensions: [org_id, project_id, service.name], histogram.explicit.buckets }`, `connectors.servicegraph { dimensions: [org_id, project_id] }`
- `processors.tail_sampling`(느린/에러 트레이스 우선, [APM-01] tail 샘플링), `processors.batch`(배치 익스포트)
- `extensions.headers_setter { headers: [{ key: X-Scope-OrgID, from_context: X-Scope-OrgID }] }`
- `exporters.prometheusremotewrite { endpoint: http://victoriametrics:8428/api/v1/write, resource_to_telemetry_conversion.enabled: true }`, `exporters.otlp/tempo { endpoint: tempo:4317, auth: headers_setter }`, `exporters.loki { endpoint: http://loki:3100/loki/api/v1/push, auth: headers_setter }`
- pipelines: traces→[spanmetrics,servicegraph,tempo], metrics(+connectors 출력)→[prometheusremotewrite], logs→[loki]

### 7.3 리텐션·플랜 훅 (#7, BILL-03 경계)
- 기본(dev): VM 90d, Tempo/Loki 2160h(90d).
- **플랜별 차등 훅 지점**(과금 페이즈): Tempo/Loki는 **per-tenant overrides**(`overrides.yaml` 키=X-Scope-OrgID=org_id → `retention`)로 org별 24h/14d/90d 적용. VM 단일노드는 per-tenant 리텐션 부재 → 과금 페이즈에 **배치 삭제 잡**(org_id 라벨 시계열 `/api/v1/admin/tsdb/delete_series`) 또는 vmcluster 승격. `plans.apm_retention_days`(`02:252`)가 소스. Phase 3는 훅 지점만 문서화, 구현 연기.

---

## 8. 빌드 순서 (backend-builder용, 의존순)

각 단계 종료 시 `go build ./... && go vet ./...` + 관련 테스트 그린을 게이트로.

1. **마이그레이션 `0010_apm_tsdb.sql`** — `apm_spans`/`apm_logs` DROP, `apm_agents` cert 컬럼. `docker compose down -v && up`으로 initdb 재적용, psql로 테이블 부재·컬럼 확인. (org_id/RLS 불변)
2. **model 정리** — `ApmSpan`/`ApmLog`/`FilterSlowSpans` 제거, `ApmAgent`에 cert 필드. tsdb 공통 타입(`ServiceGraph`/`LatencyStat`/`TraceSummary`/`SpanTree`/`LogLine`/`LiveSnapshot`) 정의. 유닛테스트 갱신.
3. **`internal/mtls`** — CA 생성/로드, 클라이언트 인증서 발급, 지문. `cmd/gencerts` 바이너리. 유닛테스트(발급→검증 라운드트립, 지문 일치).
4. **`internal/tsdb`** — VM/Tempo/Loki 클라이언트 + `APMStore` 파사드(`APMReader`). org_id 라벨/헤더 주입. 저장소 JSON 픽스처 파싱 유닛테스트(네트워크 불요).
5. **store 정리** — `apm_store.go`에서 span/log 메서드 제거, `CreateAgent`/`ListAgents`/`ResolveAgentOrg`에 cert·revoked 반영(전부 `q Querier`/sys 유지).
6. **`internal/otelgw` + `cmd/otelgw`** — mTLS gRPC OTLP 서버, token+지문→org 해석(sys 풀), 리소스 어트리뷰트 각인 + X-Scope-OrgID metadata, Collector 포워딩. 유닛테스트(각인·인증 거부 경로).
7. **API 재작성** — `apm.go` 핸들러를 tsdb 파사드로 재대상화(service-map/metrics/traces/traces:traceId/logs), `createAgent` 인증서 발급, WS `apm/stream`(2s 폴링), REST ingest dev 플래그, demo seed OTLP화. `router.go` 신규 라우트. principal.OrgID 강제.
8. **deploy 설정 + Compose + Dockerfile** — `deploy/otelcol.yaml`·`tempo.yaml`·`loki.yaml`, `Dockerfile.otelgw`·`Dockerfile.gencerts`, compose 서비스(certs-init·minio(+init)·vm·tempo·loki·otel-collector·otelgw) + 볼륨·네트워크. `docker compose up --build` 전체 기동.
9. **검증·문서** — e2e: (a) mTLS 유효 인증서 OTLP 수집 → Tempo/VM/Loki 조회 반영, (b) 무효/미인가 인증서 핸드셰이크 거부·수용 0(MTLS-01/OTLP-01), (c) 교차 org 조회 0건(APM-TENANT: org A 인증서 수집분이 org B 조회에 미노출), (d) `apm_spans`/`apm_logs` 미참조(F-7), (e) WS ≤2s 프레임(APM-02). `docs/klaro/02·03` 갱신(§4.2/6.7). 불변식 체크리스트 통과.

---

## 9. 불변식 준수 매핑

| 불변식 | 설계상 보장 지점 |
|--------|------------------|
| **시계열 RDB 분리 (F-7 해소)** | §4.1 0010이 `apm_spans`/`apm_logs` DROP, §5.5 PG span/log 메서드 제거. 메트릭→VM/트레이스→Tempo/로그→Loki(§2.1). RDB는 `apm_agents`+참조 키(org_id/project_id)만. 검증 (d). |
| **mTLS 필수 [MTLS-01/02]** | §2.2 게이트웨이 `RequireAndVerifyClientCert`, 평문 수집 경로 부재. §3 개발 CA·에이전트 인증서 발급·수명/회전/폐기. 앱계층 token+지문 병행(M-5). 검증 (b). |
| **RLS/org 격리 [APM-TENANT]** | 3중 강제(§2.1): 수집 각인(권위 org_id)·저장(VM 라벨/Tempo·Loki 테넌트)·조회(klaro 서버가 principal.OrgID만 주입). `apm_agents`는 기존 FORCE RLS 유지. 검증 (c). |
| **[COST-05] 비용 게이트** | VM/Tempo/Loki/OTel Collector/MinIO 전부 OSS 셀프호스팅 ₩0. 인증서는 Go 표준 라이브러리 자체 서명. Bedrock 외 신규 유료 외부 의존 0. |
| **[APM-01] 저오버헤드 ≤2%** | 게이트웨이 검증 후 async 포워딩(대상 앱 무블로킹), Collector `batch`+`tail_sampling`. |
| **[APM-02] near-realtime ≤2초** | §5.6 WS 2s 티커 VM 폴링, 게이트웨이 tee push 최적화 훅. |
| **잡 상태머신·서킷·Ephemeral·idle=0** | S1/S2 범위, S3 무관(N/A). mTLS만 S3에서 최초 실현. |

---

## 10. backend-builder 인계 메모

- 착수 진입점: **8번 빌드 순서 1→9 순차**. 1~5는 네트워크·컨테이너 없이 `go test` 가능(픽스처 기반), 6~9는 compose 기동 필요.
- 재사용 필수: org tx(`store.BeginOrg`/`RunInOrg`), sys 풀(`store.Sys()`, `ResolveAgentOrg`), WS 패턴(`ws.go`), 라우트 그룹(`router.go:68` `g`), 미들웨어 체인. **RLS/tenancy 구조 변경 금지.**
- 되물을 항목 없음. TSDB 쿼리 스키마(spanmetrics/servicegraph 메트릭명·라벨)는 Collector 설정(§7.2)과 tsdb 클라이언트(§5.1)가 짝 — 한쪽 변경 시 다른 쪽 동기.
- frontend-builder 인계: APM 대시보드는 §6 API(service-map/metrics/traces/logs/stream) 소비. org 파라미터 없음(서버 강제). 트레이스 상세는 span 트리(부모→자식), 스트림은 2s JSON 프레임.
```
