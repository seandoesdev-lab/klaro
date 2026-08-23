# klaro S3 상시 관측 플랫폼 — 아키텍처 설계

**작성** architect · **버전** v1 · **입력** `_workspace/04_spec-analyst_observability-contract.md` (O1~O10) · `_workspace/02_architect_design.md`(S1 참고) · `services/load-test/` 실제 구현
**전환 배경** S3(APM)를 배포 리포트(S4) 종속 스냅샷에서 **독립된 상시 관측 제품(Datadog 유사)**으로 승격. 수집·저장·조회·알림은 잡 생명주기와 무관하게 24/7 상시 동작.

---

## 0. 현행 구현 정합성(중요 — 설계 전제 재확인)

계약과 별개로 **실제 코드를 읽어 확인한 사실**이 기존 설계 문서와 다르다. 본 설계는 실제 코드를 기준으로 정합화한다.

| 항목 | `02_architect_design.md`(S1 설계 문서) 서술 | `services/load-test/` 실제 코드 | 본 설계 채택 |
|---|---|---|---|
| Control Plane 웹 프레임워크 | Echo | **Gin** (`internal/api/router.go`) | **Gin** (스택 일관성) |
| 큐/pubsub | NATS JetStream | **Redis** (list + pub/sub, `internal/queue/redis.go`) | **Redis** 재사용 |
| APM 시계열 저장 | VictoriaMetrics/Tempo/Loki | **Postgres**(`apm_spans`,`apm_logs`, migration 0003) — MVP 편의 | **VM/Tempo/Loki로 이행** (불변식 복원) |
| APM 인증 단위 | — | **project 단위** `apm_agents.ingest_token` + `X-Ingest-Token` 헤더 | **org 단위 키로 승격** |

> **핵심 판단**: 현행 `apm_spans`/`apm_logs`의 Postgres 저장은 "시계열은 RDB 밖" 불변식(CLAUDE.md·02 서두)을 위반하는 **MVP 지름길**이다. 상시 플랫폼은 이 데이터를 VictoriaMetrics/Tempo/Loki로 옮기고, Postgres에는 키/호스트/룰/이력/대시보드 등 **참조·메타만** 남긴다. 기존 project-scoped 경로(`GET /projects/:id/apm/*`)는 **리포트 스냅샷 소비 경로(O10)로 존치**하고, 상시 탐색은 신규 org-scoped Explorer로 분리한다(대체 아님, 공존 — 모호#4 해소).

---

## 1. 서비스 모듈 경계 결정 — 신규 `services/observability/` 분리

**결정: 기존 `services/load-test/`를 확장하지 않고 신규 Go 서비스 `services/observability/`로 분리한다.**

**근거**
- **생명주기 상반**: S1 CP는 잡 버스트(spawn→run→cleanup, idle=0)를 다루고, 관측 플랫폼은 **상시 가동 수집 게이트웨이**다. 한 프로세스에 섞으면 "워커 idle=0" 회수 로직과 "상시 유지" 로직이 같은 코드베이스에서 충돌·혼동된다(계약 불변식 주의 항목).
- **폭발 반경(blast radius)**: 텔레메트리 폭주(24/7·대량)가 부하 테스트 제어 평면(잡 디스패치·서킷 브로드캐스트)의 지연/가용성을 갉아먹으면 안 된다. 프로세스·스케일 경계를 분리해 상호 격리.
- **인증·저장 경계**: org 단위 키 인증 + VM/Tempo/Loki 멀티테넌트 = project 단위 토큰 + Postgres와 근본적으로 다른 축. 서비스 이음매로 두는 편이 깔끔.
- **독립 스케일**: 수집 게이트웨이(Collector 앞단)는 수평 확장 대상, S1 CP는 상태 조율 중심 — 스케일 프로파일이 다르다.

**트레이드오프 / 완화**
- 단점: `tenancy`(RLS 세션 주입)·`db`(pgx 풀)·`mtls`·`Notifier`·`queue`(Redis Signaler) 플럼빙이 두 Go 서비스에 단기 중복된다.
- 완화: 공통 횡단 관심사는 추후 `services/shared/`(Go module) 또는 `internal/platform` 공유 패키지로 추출. **MVP에서는 S1의 검증된 패턴을 복제**(Redis Signaler·RLS 미들웨어·httpx 에러 규약)해 빠르게 착수하고, 3개 이상 중복 시 추출(premature abstraction 회피).

**디렉토리 골격** (S1 `services/load-test/` 수직 슬라이스 관례 답습, Gin 기준)
```
services/observability/
├── cmd/
│   ├── obsplane/            # 관측 Control Plane (Gin REST + WS + 내부 gRPC/HTTP authz)
│   └── retentionjob/        # 리텐션/삭제 배치(cron) 엔트리포인트
├── internal/
│   ├── platform/            # 횡단(도메인 무관): config, db(pgx+RLS 세션), httpx(에러 규약),
│   │                        #   mtls, audit, redisx(Signaler 재사용)
│   ├── tenancy/             # org 컨텍스트 해석 + SET LOCAL app.current_org 미들웨어(S1 복제)
│   ├── ingestkey/           # [OBS-02] org 관측 키 발급/폐기/로테이션 + 검증(Collector authz)
│   │   ├── service.go       #   키 민팅(1회 노출)·해시 저장·로테이션 grace
│   │   ├── quota.go         #   호스트수/수집량 쿼터 스냅샷 계산·캐시
│   │   ├── repo.go          #   observability_keys / observability_hosts / usage_rollups
│   │   └── handler.go
│   ├── tenants/             # [격리] 각 백엔드 테넌트 매핑(org_id → VM AccountID / X-Scope-OrgID)
│   ├── explorer/            # [OBS-03/04/05] 메트릭·트레이스·로그 조회(백엔드 프록시, org 라벨 강제)
│   │   ├── metrics.go       #   VictoriaMetrics(MetricsQL) 프록시 + org 매처 주입
│   │   ├── traces.go        #   Tempo(TraceQL) 프록시
│   │   ├── logs.go          #   Loki(LogQL) 프록시
│   │   └── handler.go
│   ├── alerting/            # [OBS-06/07] 룰 CRUD + vmalert 룰그룹 렌더·동기화 + 발동 수신
│   │   ├── rules.go         #   alert_rules CRUD·검증(신호/비교/임계/평가창)
│   │   ├── sync.go          #   룰 → vmalert per-org 룰그룹 파일/ConfigMap 렌더
│   │   ├── receiver.go      #   vmalert/Alertmanager webhook 수신 → alert_events + Notifier
│   │   └── handler.go
│   ├── dashboards/          # [OBS-09] 대시보드/패널 구성 CRUD(JSONB spec)
│   ├── live/                # [OBS-01/APM-02] Collector→CP live 수집 → Redis pub/sub → WS fan-out
│   │   ├── ingest.go        #   /internal/live-ingest 수신(Collector exporter)
│   │   ├── ws_hub.go        #   org+stream 스코프 구독 허브, ≤2초 push
│   │   └── ws_handler.go
│   ├── snapshot/            # [OBS-10] 리포트(S4)용 스냅샷 조회 어댑터(구간 질의 → Explorer 위임)
│   └── notify/              # Notifier(MailHog SMTP + Slack 스텁) — S1과 계약 공유
├── migrations/              # 0001_observability_keys ... (org-scoped + RLS)
├── deploy/
│   ├── docker-compose.yml   # obsplane, otel-collector, victoriametrics, tempo, loki,
│   │                        #   vmalert, alertmanager, redis, mailhog, postgres
│   └── otel-collector.yaml  # 수신(OTLP/mTLS)·authz 확장·라우팅 exporter 설정
└── sdk/                     # (별도 스코프 참조) klaro-apm 래퍼 릴리스는 본 세션 밖
```

**기존 `services/load-test/`의 APM 코드 처리**: `internal/model/apm.go`·`internal/store/apm_store.go`·`internal/api/apm.go`·migration `0003`는 **리포트 스냅샷 경로(O10)로 존치**하고 "MVP snapshot" 주석 유지. 신규 개발은 `services/observability/`에서 진행. 상시 Explorer가 리포트 스냅샷까지 흡수하는 시점은 열린 질문(§7-6).

---

## 2. 스택 결정 (확정 스택 위에 얹는 상시 플랫폼 결정)

### 확정(계약·CLAUDE.md — 재논의 금지)
- 시계열 저장 = **VictoriaMetrics(metrics) / Tempo(traces) / Loki(logs)** · 수집 표준 = **OpenTelemetry(OTLP over gRPC)** · SDK↔Collector = **mTLS 필수** · 격리 = **RLS + org** · 비용 = **셀프호스팅 OSS(₩0), Bedrock 외 유료 의존 금지**.
- 수집 게이트 = **OTel Collector(gateway 배포)**: SDK가 직접 백엔드로 쓰지 않고 Collector가 인증·테넌트 라우팅·live 복제의 단일 종단점.

### 결정 필요 9건 (HOW) — 각 결정·근거·트레이드오프

#### HOW-1. 알림 룰 엔진 = **vmalert(VictoriaMetrics 생태계) + 자체 룰 관리 계층** [OBS-06/07]
- **결정**: 룰은 Postgres(`alert_rules`, org-scoped RLS)에 저장하고, CP `alerting/sync`가 이를 **org별 vmalert 룰그룹**으로 렌더한다. vmalert가 VictoriaMetrics(MetricsQL)를 주기 평가하여 발동 시 CP `alerting/receiver`(webhook)로 전달 → CP가 `alert_events` 기록 + `Notifier`(MailHog/Slack 스텁, S1과 공유) 발송.
- **근거**: 평가 스케줄링·`for`(지속 창)·발동/해소(firing/resolved) 상태 관리·dedup을 이미 갖춘 OSS를 재사용(₩0, COST-05 준수·기존 VM 스택 정합). 자체 평가 루프를 재발명하지 않는다.
- **트레이드오프**: vmalert는 **메트릭 전용**. 로그(Loki ruler)·트레이스 기반 알림은 MVP 범위 밖 → 신호 컬럼(`signal`)은 미리 두되 MVP는 `metric`만 활성. 대안(자체 폴링 evaluator: 전 신호 통합·완전 제어)은 스케줄/상태/중복제거를 직접 구현해야 해 초기 과잉 → **메트릭은 vmalert, 로그/트레이스 알림은 필요 확정 시 소형 자체 폴러 추가**로 이연.
- **통지 채널(모호#5 해소)**: 개발은 MailHog(이메일)+Slack Incoming Webhook 스텁(로그 기록 no-op 가능)만. 페이로드 최소 스키마 = `{event, org_id, rule_id, rule_name, signal, value, threshold, state, at, dashboard_url}`. S1 서킷 브레이커 알림(`job.aborted`)과 **동일 `Notifier` 인터페이스 공유**하되 subject는 분리(`klaro.notify`, `klaro.obs.alert`).

#### HOW-2. Explorer 조회 계층 = **자체 얇은 쿼리 API(백엔드 프록시 + org 라벨 강제)** [OBS-03/04/05]
- **결정**: Grafana Explore를 직접 노출하지 않는다. CP `explorer` 패키지가 구조화 파라미터(`from/to`, filters, aggregation)를 받아 각 백엔드 쿼리(MetricsQL/TraceQL/LogQL)로 변환하고, **서버 사이드에서 org 테넌트 헤더/매처를 강제 주입**해 프록시한다.
- **근거**: 테넌트 격리를 앱이 강제할 수 있어야 한다(사용자가 라벨 매처를 벗겨 타 org 시리즈를 조회하는 것을 원천 차단). Grafana Explore는 OSS 멀티테넌시가 약하고 인증이 결합돼 격리 보증이 어렵고, 05 핸드오프(자체 디자인 시스템 유지)와도 상충.
- **트레이드오프**: raw PromQL/LogQL passthrough 대비 표현력 제한. MVP는 구조화 파라미터 + 화이트리스트 집계(`rate/avg/p50/p95/p99/count/sum`). raw 쿼리 노출이 필요해지면 label-enforcement 프록시(prom-label-proxy 패턴, org 매처 강제 주입)를 앞단에 두고 개방 — 후속.

#### HOW-3. 대시보드 = **자체 패널 구성 CRUD(JSONB spec)** [OBS-09]
- **결정**: `dashboards.spec`(JSONB, 패널 배열)로 저장하는 자체 CRUD. 패널은 Explorer 쿼리를 참조하고, 렌더링은 klaro 프런트가 Explorer API로 수행. Grafana 임베드/프록시 아님.
- **근거**: 자체 디자인 시스템 유지(05), 인증/iframe/멀티테넌시 결합 회피. 별도 `dashboard_panels` 테이블은 두지 않고 `spec` JSONB 하나로(YAGNI).
- **트레이드오프**: 시각화 재구현 비용. **마일스톤 편입 시점은 최소 범위 밖(팀리드) → M2+로 이연 권고(§7-3 열린 질문)**.

#### HOW-4. org 인증/쿼터 모델 = **org 관측 키(해시 저장, 라벨 태깅) + 호스트수 primary·수집량 secondary 쿼터** [OBS-02]
- **결정**:
  - 신규 `observability_keys`(org-scoped): `key_hash`만 저장(시크릿 1회 노출), `key_prefix`로 식별 표시, `scope_label jsonb`(service/env 태그 — **완전 계층 키 트리 대신 라벨 태깅**, YAGNI). 상태 `active/revoked`.
  - 로테이션 = 신규 키 발급 + grace 윈도(구·신 병존) 후 구키 revoke. revoke/rotate 후 구키 수집 거부.
  - **쿼터 축 = 활성 호스트 수(primary) + 월 수집량(GB, secondary guard)**. 호스트수를 1차 기준으로(Datadog식·예측 가능·과금 직관). `observability_hosts`(org, host_ident, last_seen_at)로 활성 호스트 카운트, `observability_usage_rollups`로 수집량 롤업.
  - 마이그레이션(모호#2 해소): 기존 project `apm_agents.ingest_token`은 **MVP 스냅샷 경로로 존치**(대체 아님). 상시 SDK는 신규 org 키 사용. 자동 데이터 마이그레이션 없음 — 두 경로 병존.
- **근거**: 여러 서비스/호스트가 하나의 org 키(+라벨)로 전송하는 것이 상시 관측의 자연스러운 단위. 해시 저장·1회 노출은 표준 API 키 위생.
- **트레이드오프**: 라벨 태깅은 키별 세분 권한(서비스별 폐기)을 못 준다 → 필요 시 sub-key 계층은 후속. 호스트수 쿼터는 컨테이너 오토스케일 환경에서 카운팅이 흔들릴 수 있어 `host_ident`(service.instance.id) 정규화 규칙 필요.

#### HOW-5. 리텐션/다운샘플링 = **신호별 개별 보존 + 배치 삭제 잡, 다운샘플링 이연** [OBS-08]
- **결정**:
  - `plans`에 신호별 보존 컬럼 신설: `obs_metrics_retention_days`·`obs_traces_retention_days`·`obs_logs_retention_days`(단일 `apm_retention_days`에서 승격, 기존 컬럼은 MVP 호환 위해 존치·후속 deprecate).
  - 각 백엔드 native 리텐션은 **최대 플랜(90d) 기준 전역 설정**, 그보다 짧은 org/플랜 보존은 **CP `retentionjob`(cron)이 백엔드 delete API로 집행**(VM `/api/v1/admin/tsdb/delete_series`, Loki delete API, Tempo는 block 단위 — 제약 있음).
  - **다운샘플링은 이연**: OSS VictoriaMetrics는 다운샘플링 미지원(엔터프라이즈 기능). 장기 저해상도 롤업이 필요하면 vmalert **recording rule**로 사전 집계 시리즈 생성(OSS 가능). MVP는 리텐션(보존·삭제)만.
- **근거**: 신호별 수명·비용이 다르다(트레이스가 가장 무겁다). 계약 수용기준(플랜 초과 데이터 조회 불가·삭제)은 배치 삭제로 충족. 다운샘플링은 spec-analyst가 근거 얕음(모호#6)으로 표시 → 요구 확정 전 미구현.
- **트레이드오프**: CP 배치 삭제는 native 리텐션보다 정밀하지만 삭제 API 부하/지연이 있다. 개발 환경은 로컬 디스크 용량 관리가 핵심(§FinOps 로컬 상한 경보 추가).

#### HOW-6. 관측 과금 축 = **신설(호스트수 primary + 수집량), VU-Minutes와 병행** [OBS-08/BILL-03]
- **결정**: 과금 미터 2축 병행 — S1 = **VU-Minutes**(테스트 단위), S3 = **월 활성 호스트수(+수집 GB overage)**. 발행은 기존 `klaro.usage.emitted` 패턴에 `meter` 판별자 추가(`vu_minutes` | `observability_hosts` | `observability_ingest_gb`). **계산·청구는 Billing 서비스 책임**, 본 스코프는 발행 지점(usage_rollups → 이벤트)만.
- **근거**: 배포 리포트와 상시 관측은 가치 축이 달라 단일 미터로 묶으면 가격이 왜곡. Datadog식 호스트 과금이 예측 가능.
- **트레이드오프**: 미터 2축은 Billing 복잡도↑. §4 FinOps에 24/7 로컬 자원(디스크·메모리) 상한·경보 기준 추가(계약 §6 신규 항목 반영).

#### HOW-7. near-realtime push = **Collector→CP live 수집 → Redis pub/sub → WS fan-out(S1 Signaler 재사용)** [OBS-01/APM-02]
- **결정**: 두 경로 분리 — (a) **히스토리/탐색 = pull**(Explorer 쿼리 API, VM/Tempo/Loki), (b) **라이브 대시보드 = push**. Collector가 메트릭 스트림 복제본을 CP `/internal/live-ingest`로 export → CP가 `klaro:obs:live:<org_id>:<stream>` Redis 채널로 publish → WS 허브가 org+stream 구독자에게 ≤2초 fan-out. S1 `Signaler`(PublishMetric/SubscribeMetrics, drop-oldest 백프레셔) 패턴을 **load_test_id 대신 org+stream 스코프**로 재사용.
- **근거**: 이미 검증된 Redis pub/sub → WS 파이프를 재사용(신규 의존 0). ≤2초는 Collector 배치 주기 ≤2s로 달성.
- **트레이드오프**: Redis pub/sub는 fire-and-forget(라이브 뷰엔 충분, 영속은 VM). 대량 구독 시 채널 팬아웃 비용 — org+stream 스코프로 제한해 완화.

#### HOW-8. 테넌트 격리 = **각 백엔드 native 멀티테넌시(org 키잉) + Postgres RLS** [격리 강화]
- **결정**: 단순 라벨 매처가 아니라 **각 저장소의 native 테넌트**를 org로 키잉 — VictoriaMetrics `AccountID`(vminsert/vmselect 멀티테넌시), Tempo/Loki `X-Scope-OrgID` 헤더. **CP만** 이 테넌트 키/헤더를 설정(RLS로 해석한 org에서 파생). Postgres 메타는 RLS 그대로.
- **근거**: 저장소 계층에서 격리가 강제되어 라벨 매처 누락 사고를 원천 차단. 모두 OSS 지원(₩0).
- **트레이드오프**: org 다수 시 VM 테넌트(AccountID) 증가 관리 필요 — 초기 규모엔 무해, 모니터링 대상. Enterprise는 org별 저장소/네임스페이스 물리 분리 옵션(후속).

#### HOW-9. APM-01~03 재프레이밍 = **승인(재번호 없이 플랫폼 전역 NFR 유지)** [APM-01/02/03]
- **결정**: spec-analyst 권고 **동의·승인**. 세 ID는 재번호하지 않고, 리포트 스냅샷이 아닌 **상시 플랫폼 전체에 적용되는 비기능 요구(NFR)**로 프레이밍만 갱신(01 §2.4). OBS 항목들이 이를 참조.
- **근거**: 세 ID가 docs 여러 곳(01·05)에서 교차 참조 → 재번호 시 링크 그래프 파손(CLAUDE.md 문서 지도 원칙). ID 안정성 > 번호 정합.

#### HOW-10. 다운샘플링 = **vmalert recording rule 기반 메트릭 전용 롤업(사용자 확정 — 이연 아님)** [OBS-08, §7-2 해소]
- **결정**: 사용자가 이연을 거부하고 지금 확정을 요청함에 따라, MVP 리텐션만이 아니라 **다운샘플링을 실제로 설계**한다. 다만 OSS VictoriaMetrics에 enterprise 다운샘플링 기능이 없으므로, 이미 스택에 있는 **vmalert**(HOW-1에서 알림 평가용으로 도입)를 **recording rule** 용도로 재사용한다(신규 컴포넌트 추가 없음, ₩0 유지).
  - vmalert recording rule 그룹이 원본(15~30s 해상도) 메트릭을 주기적으로 읽어 **5분/1시간 집계 시리즈**(counter는 rate 기반, gauge는 avg/max/p95 — 메트릭 타입별 집계 함수 매핑 필요)를 새 시리즈명(`<metric>_rollup5m`/`_rollup1h`)으로 VM에 remote-write.
  - `plans.obs_metrics_rollup_retention_days`(신규 컬럼, 02-data-model.md 반영)로 롤업 시리즈의 **장기 보존**(raw보다 훨씬 길게, 예: Enterprise 365d)을 별도 관리. raw는 기존 `obs_metrics_retention_days`(짧음) 그대로.
  - **Explorer 폴백**: 조회 구간이 raw 보존 창을 넘으면 `explorer/metrics.go`가 자동으로 롤업 시리즈를 조회하도록 폴백하고, 응답에 `resolution: "raw"|"5m"|"1h"` 필드를 넣어 프런트가 "저해상도 데이터" 배지를 표시할 수 있게 한다.
  - **스코프 경계(명시적)**: 다운샘플링은 **메트릭에만** 적용된다. 트레이스·로그는 OSS 생태계에 표준 다운샘플링 개념이 없다(Datadog도 로그는 샘플링/보존만 제어) — 두 신호는 리텐션(보존 후 삭제)만 적용하며 이를 다운샘플링 미적용으로 명시적으로 문서화한다(사용자 기대치 관리).
- **리스크(구현 전 검증 필요)**: vmalert의 multitenant recording rule 동작(VM `AccountID` 테넌트별로 규칙이 올바르게 격리 평가되는지)을 backend-builder 착수 전 소규모 스파이크로 확인해야 한다 — 안 되면 org별 recording rule 그룹을 개별 배포하는 방식으로 대체(관리 비용 증가).
- **트레이드오프**: recording rule 결과 시리즈가 늘어나 VM 카디널리티가 소폭 증가하지만, 집계라 raw보다 훨씬 낮은 카디널리티(대체로 문제 없음).

#### HOW-11. SDK 배포·버전관리 = **thin OTel wrapper, SemVer, 무료 퍼블릭 레지스트리(npm/PyPI/Maven Central)** [모호#8, §7-5 해소]
- **결정**: `klaro-apm` SDK는 OTel 공식 SDK/자동계측을 그대로 의존성으로 쓰는 **thin wrapper**다. 언어별 3개 패키지(`@klaro/apm`(npm) · `klaro-apm`(PyPI) · `io.klaro:klaro-apm`(Maven, groupId는 실제 도메인 확정 시 조정))는 동일한 wrapper 계약을 따른다:
  1. **설정**: `KLARO_OBS_KEY`(org 관측 키 시크릿, HOW-4) 환경변수 또는 코드 옵션 → OTLP exporter에 `klaro-obs-key` 헤더로 자동 첨부. Collector 엔드포인트도 옵션(기본값 제공).
  2. **호스트 식별**: `service.instance.id` resource attribute를 정규화 규칙(HOW-4의 `host_ident`와 정합 — 컨테이너는 pod uid 우선, 없으면 hostname+PID)으로 자동 설정.
  3. **자동계측 + 샘플링 기본값**: 언어별 OTel auto-instrumentation을 그대로 활성화, tail-based 샘플링 기본 on(APM-01 ≤2% 오버헤드 NFR 충족).
  4. **비블로킹 전송**: 비동기 배치 익스포트, 큐 포화 시 drop-oldest, Collector 연결 실패는 조용히 재시도(고객 앱에 예외 전파 금지 — APM-01).
  - **버전 정책**: SemVer. wrapper API breaking change만 major 상승 기준으로 삼고, 내부 OTel 코어 SDK 버전 추종은 minor/patch로 흡수(OTLP는 하위호환 보장되므로 SDK-Collector 결합도는 낮음).
  - **배포**: npm/PyPI/Maven Central 전부 **퍼블릭 레지스트리 게시 자체는 무비용**([COST-05] 위반 없음). 릴리스는 기존 스택의 GitHub Actions(무료 티어)로 자동화.
  - **레포 구조**: `sdk/klaro-apm-node/` · `sdk/klaro-apm-python/` · `sdk/klaro-apm-java/` — 3개 구현이 공유하는 "SDK 계약"(설정 키 이름·리소스 속성 규칙)을 별도 문서 1개로 관리해 드리프트 방지.
- **스코프(2026-08-23 갱신 — 사용자가 이연 대신 착수 지시)**:
  - vanilla OTel(문서만)로 시작하자는 대안을 제시했으나 사용자가 **커스텀 wrapper를 지금부터 구축**하기로 확정.
  - **언어 우선순위**: 3개 동시 착수 대신 **Python(FastAPI 연동) 단일 언어로 먼저 구현**, Node/Java는 검증 후 순차 추가.
  - **레포/오픈소스**: 별도 퍼블릭 레포 분리(오픈소스 배포) 대안을 제시했으나, 지금은 **klaro 모노레포 내부(`sdk/klaro-apm-python/`)에 유지**하기로 확정 — 오픈소스 전환은 나중 결정.
  - 착수 시 backend-builder(Go)가 아니라 Python 패키지 전용 구현이 필요 — 새 빌더 에이전트/스킬 필요 여부는 구현 착수 시 확인.

---

## 3. 데이터 모델 · 마이그레이션 초안 (`services/observability/migrations/`, org-scoped + RLS)

전제: `orgs`·`projects`·`plans`·`users`·`audit_logs`는 선행 의존성으로 존재 가정(S1과 공유). 모든 신규 테이블에 `org_id uuid NOT NULL` + RLS.

**RLS 공통 패턴**(S1과 동일 — 앱은 non-superuser 롤, 요청 진입 시 `SET LOCAL app.current_org`)
```sql
ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;
ALTER TABLE <t> FORCE ROW LEVEL SECURITY;
CREATE POLICY <t>_isolation ON <t>
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);
```

### 0001_observability_keys [OBS-02]
```
observability_keys(
  id uuid PK, org_id uuid NOT NULL,
  name text NOT NULL,
  key_prefix text NOT NULL,              -- 표시용(예: obsk_ab12…), 시크릿 아님
  key_hash text NOT NULL,                -- sha256(secret) — 시크릿은 1회 노출 후 미저장
  scope_label jsonb,                     -- {service?, env?} 태깅(계층 키 트리 아님)
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
  created_by uuid, last_used_at timestamptz,
  revoked_at timestamptz, created_at timestamptz DEFAULT now())
UNIQUE(org_id, name)
INDEX(org_id, status)
UNIQUE(key_hash)                          -- 전역 조회(Collector authz 빠른 경로)
+ RLS
```

### 0002_observability_hosts [OBS-02 쿼터-호스트수]
```
observability_hosts(
  id uuid PK, org_id uuid NOT NULL,
  key_id uuid REFERENCES observability_keys ON DELETE SET NULL,
  host_ident text NOT NULL,              -- 정규화된 service.instance.id / hostname
  service text, env text,
  first_seen_at timestamptz DEFAULT now(), last_seen_at timestamptz DEFAULT now())
UNIQUE(org_id, host_ident)
INDEX(org_id, last_seen_at)               -- 활성 호스트 카운트(윈도 내 last_seen)
+ RLS
```

### 0003_observability_usage_rollups [OBS-08/BILL-03 과금·쿼터-수집량]
```
observability_usage_rollups(
  id uuid PK, org_id uuid NOT NULL,
  period_start timestamptz NOT NULL, period_end timestamptz NOT NULL,
  signal text NOT NULL CHECK (signal IN ('metrics','traces','logs')),
  ingested_bytes bigint NOT NULL DEFAULT 0,
  series_count bigint, host_count_max int,
  created_at timestamptz DEFAULT now())
UNIQUE(org_id, period_start, signal)
INDEX(org_id, period_start)
+ RLS
```

### 0004_alert_rules [OBS-06]
```
alert_rules(
  id uuid PK, org_id uuid NOT NULL,
  name text NOT NULL,
  signal text NOT NULL DEFAULT 'metric' CHECK (signal IN ('metric','log','trace')), -- MVP: metric만 활성
  query text NOT NULL,                    -- MetricsQL 식(또는 구조화 표현 렌더 결과)
  comparator text NOT NULL CHECK (comparator IN ('gt','gte','lt','lte')),
  threshold numeric NOT NULL,
  for_duration_sec int NOT NULL DEFAULT 60,   -- 지속(평가 창)
  severity text NOT NULL DEFAULT 'warning' CHECK (severity IN ('info','warning','critical')),
  channels jsonb NOT NULL DEFAULT '[]',   -- [{type:'email'|'slack', target}]
  enabled bool NOT NULL DEFAULT true,
  created_by uuid, created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now())
UNIQUE(org_id, name)
INDEX(org_id, enabled)
+ RLS
```

### 0005_alert_events [OBS-07]
```
alert_events(
  id uuid PK, org_id uuid NOT NULL,
  rule_id uuid NOT NULL REFERENCES alert_rules ON DELETE CASCADE,
  state text NOT NULL CHECK (state IN ('firing','resolved')),
  value numeric, labels jsonb,
  notified_channels jsonb,
  started_at timestamptz NOT NULL DEFAULT now(), resolved_at timestamptz)
INDEX(org_id, started_at)
INDEX(org_id, rule_id, state)
+ RLS
```

### 0006_dashboards [OBS-09]
```
dashboards(
  id uuid PK, org_id uuid NOT NULL,
  name text NOT NULL, description text,
  spec jsonb NOT NULL DEFAULT '{"panels":[]}',   -- 패널 배열(쿼리·viz·layout) 단일 JSONB
  created_by uuid, created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now())
UNIQUE(org_id, name)
INDEX(org_id)
+ RLS
```

### 0007_plans_observability (plans 확장 — 선행 plans 테이블 ALTER) [OBS-08/BILL-03]
```
ALTER TABLE plans
  ADD COLUMN obs_metrics_retention_days int NOT NULL DEFAULT 1,   -- Free 1d
  ADD COLUMN obs_traces_retention_days  int NOT NULL DEFAULT 1,
  ADD COLUMN obs_logs_retention_days    int NOT NULL DEFAULT 1,
  ADD COLUMN obs_max_hosts              int,                       -- NULL=무제한(Enterprise)
  ADD COLUMN obs_max_ingest_gb_month    numeric;                   -- secondary guard
-- 기존 apm_retention_days는 MVP 스냅샷 호환 위해 존치(후속 deprecate).
-- 예시 값: Free(1d/1d/1d, 5 hosts) / Pro(14d, 50 hosts) / Enterprise(90d, NULL)
```
> 계약의 "Free 24h/Pro 14d/Enterprise 90d"는 메트릭 기준 예시. 트레이스/로그는 비용 고려해 더 짧게 설정 가능(플랜 시드에서 확정).

**감사 로그**: 키 발급/폐기/로테이션, 룰 생성/수정/삭제는 기존 `audit_logs`에 `action`(`obs.key.issue|obs.key.revoke|obs.key.rotate|obs.rule.create|...`)로 기록(SOC2, 계약 불변식).

**시계열 원본은 RDB 미저장**: metrics→VictoriaMetrics(AccountID=org), traces→Tempo(X-Scope-OrgID=org), logs→Loki(X-Scope-OrgID=org). Postgres는 위 참조/메타만.

---

## 4. 서비스 인터페이스

### 4.1 Ingestion 파이프라인 (SDK → Collector → 저장, org 인증·쿼터 집행)
```
[운영 서비스] klaro-apm SDK(OTel)
   │  OTLP/gRPC + mTLS,  헤더: klaro-obs-key: <org 키 시크릿>,  resource: service.instance.id
   ▼
[OTel Collector (gateway)]
   │  ① authz 확장: klaro-obs-key → CP /internal/authz/ingest-key 검증(캐시)
   │       → org_id 해석 · 키 status=active 확인 · 쿼터 스냅샷(호스트수/수집량) 체크
   │       → 초과 시 reject(429) 또는 flag(정책, §7-1)
   │  ② 라우팅 exporter:
   │       metrics → VictoriaMetrics(vminsert, AccountID=org tenant)
   │       traces  → Tempo(OTLP, X-Scope-OrgID=org)
   │       logs    → Loki(X-Scope-OrgID=org)
   │  ③ live 복제: metrics 샘플 → CP POST /internal/live-ingest (org 태깅)
   ▼
[obsplane CP]
   ├─ /internal/live-ingest → Redis publish klaro:obs:live:<org>:<stream> → WS fan-out(≤2s)
   ├─ 호스트 레지스트리 갱신(observability_hosts.last_seen_at)  [비동기]
   └─ usage 롤업 누적(observability_usage_rollups)              [비동기]
```
- **쿼터 집행 지점 = Collector authz 확장**(빠른 경로, CP가 제공하는 쿼터 스냅샷 캐시를 N초 주기 갱신). 초과 시 관측 신호(에러/응답 헤더 `X-Klaro-Quota: exceeded`/`alert_events` 또는 로그)를 남긴다.
- **키 검증**: Collector→CP `/internal/authz/ingest-key`(내부 mTLS). `key_hash` 조회로 org·status 확인, 결과 캐시.

### 4.2 REST (클라이언트 ↔ obsplane CP) — Gin
공통 에러 규약(httpx, S1 공유): `{code, message, details}` — `VALIDATION_ERROR`(422)/`CONFLICT`(409)/`QUOTA_EXCEEDED`(402|429)/`FORBIDDEN`(403)/`NOT_FOUND`(404)/`UNAUTHENTICATED`(401). 모든 경로 org 스코프(RLS + 세션 GUC).

| 메서드 | 경로 | 계약 | 응답 |
|---|---|---|---|
| POST | `/orgs/:orgId/obs/keys` | OBS-02 발급 | 201 `{id, name, key_prefix, secret(1회), status:"active"}` |
| GET | `/orgs/:orgId/obs/keys` | OBS-02 목록 | 200 `{data:[{id,name,key_prefix,status,last_used_at}]}` (secret 미포함) |
| POST | `/orgs/:orgId/obs/keys/:keyId/rotate` | OBS-02 로테이션 | 200 `{id, key_prefix, secret(1회), grace_until}` |
| DELETE | `/orgs/:orgId/obs/keys/:keyId` | OBS-02 폐기 | 204 (이후 구키 수집 401/거부) |
| GET | `/orgs/:orgId/obs/quota` | OBS-02 쿼터 | 200 `{active_hosts, host_limit, ingest_gb, ingest_limit_gb, period}` |
| GET | `/orgs/:orgId/obs/metrics/query` | OBS-03 | 200 `{series:[{labels, points:[[ts,val]]}]}` — `?from&to&filter&agg&step` |
| GET | `/orgs/:orgId/obs/traces` | OBS-04 | 200 `{data:[{trace_id, root_service, duration_ms, start}]}` — `?from&to&service&min_duration_ms` |
| GET | `/orgs/:orgId/obs/traces/:traceId` | OBS-04 | 200 `{trace_id, spans:[{span_id,parent_span_id,name,service,duration_ms,status}]}` (워터폴) |
| GET | `/orgs/:orgId/obs/logs` | OBS-05 | 200 `{data:[{ts,level,message,labels}], next}` — `?from&to&filter&query&limit` |
| GET/POST | `/orgs/:orgId/obs/alert-rules` | OBS-06 | 201 `{id, name, signal, query, comparator, threshold, for_duration_sec, enabled}` |
| GET/PATCH/DELETE | `/orgs/:orgId/obs/alert-rules/:ruleId` | OBS-06 | 200/204 · 미지원 신호/비교 → 422 |
| GET | `/orgs/:orgId/obs/alert-events` | OBS-07 | 200 `{data:[{rule_id,state,value,started_at,resolved_at}]}` — `?state&from&to` |
| GET/POST | `/orgs/:orgId/obs/dashboards` | OBS-09 | 201 `{id, name, spec}` |
| GET/PATCH/DELETE | `/orgs/:orgId/obs/dashboards/:dashId` | OBS-09 | 200/204 |

### 4.3 WebSocket (클라이언트 ↔ CP) — 라이브 [OBS-01/APM-02]
- `WS /orgs/:orgId/obs/live?stream=<metric|service>` — 접속 시 org 스코프 인가(밖이면 close 4403).
- 주기 메시지(≤2초): `{ts, stream, points:[{labels, value}]}` (Collector→Redis→fan-out).

### 4.4 내부 인터페이스 (Collector ↔ CP, mTLS)
| 경로 | 방향 | 용도 |
|---|---|---|
| `POST /internal/authz/ingest-key` | Collector→CP | 키 검증·org 해석·쿼터 스냅샷 반환(캐시) |
| `POST /internal/live-ingest` | Collector→CP | live 메트릭 복제 수신 → Redis publish |
| `POST /internal/alerts/webhook` | vmalert/Alertmanager→CP | 발동/해소 → `alert_events` + Notifier |

### 4.5 큐/pubsub (Redis — S1 Signaler 재사용)
| 채널/subject | 발행자 | 구독자 | payload |
|---|---|---|---|
| `klaro:obs:live:<org>:<stream>` | live.ingest | WS hub | `{ts, stream, points}` |
| `klaro.obs.alert` | alerting.receiver | Notifier(mailhog/slack) | `{event, org_id, rule_id, rule_name, signal, value, threshold, state, at, dashboard_url}` |
| `klaro.usage.emitted`(meter=observability_hosts/ingest_gb) | usage 롤업 훅 | Billing(외부) | `{org_id, meter, quantity, period, computation}` |

### 4.6 리포트(S4) 스냅샷 소비 경계 [OBS-10]
- S4 `AGGREGATING` 시, 리포트는 `snapshot` 어댑터를 통해 **지정 시간 구간**(예: 부하 테스트 실행 구간)의 메트릭/트레이스를 **Explorer 조회 경로로 읽어** 소비(소유·저장 아님).
- 기존 MVP `GET /projects/:id/apm/{traces,logs}`(project·Postgres)는 **리포트용 스냅샷으로 존치**(대체 아님). 상시 org Explorer(O3~O5)와 공존.
- 상시 수집/저장/보존은 리포트 성패와 무관하게 지속(리포트가 데이터 수명 좌우 금지). S1 부하 워커 메트릭(잡 수명과 소멸)과 상시 텔레메트리(상시 보존)는 경로·수명이 구분됨.

---

## 5. 불변식 준수 검증 (CLAUDE.md · 계약 체크리스트)

| 불변식 | 설계상 집행 지점 | 상태 |
|---|---|---|
| **RLS 멀티테넌시** | 신규 7개 테이블 전부 `org_id`+FORCE RLS, non-superuser 롤, `tenancy` 미들웨어가 `SET LOCAL app.current_org`. 크로스테넌트 조회/수집 불가. | ✅ |
| **내부 mTLS** | SDK↔Collector OTLP/gRPC mTLS 필수, Collector↔CP 내부 경로 mTLS. | ✅ |
| **시계열 RDB 밖** | metrics/traces/logs → VM/Tempo/Loki(native tenant=org). Postgres는 키/호스트/룰/이력/대시보드/롤업만. **MVP의 apm_spans/apm_logs Postgres 저장 위반을 상시 경로에서 복원.** | ✅ (MVP 경로는 스냅샷으로 격리 존치) |
| **워커 idle=0 (반대 적용)** | 관측 컴포넌트(Collector/VM/Tempo/Loki/obsplane)는 **상시 가동이 정상**. idle=0 회수 finalizer는 S1 잡 워커에만 적용 — 신규 서비스 분리로 혼동 원천 차단. | ✅ |
| **비용 [COST-05]** | 전 구간 OSS(VM/Tempo/Loki/OTel Collector/vmalert/Alertmanager/Redis/MailHog/Slack 스텁). Bedrock 외 신규 유료 의존 0. | ✅ |
| **보존 자동화 [BILL-03]** | native 리텐션(최대 플랜) + CP `retentionjob` 배치 삭제로 org/플랜별 집행. 개발/프로덕션 동일 정책. | ✅ |
| **APM-01/02/03 NFR** | ≤2% 오버헤드(SDK 비동기 배치), ≤2초 push(Collector 배치+Redis→WS), ≥3초 병목 트레이스(traces Explorer `min_duration_ms`). 플랫폼 전역 NFR로 유지(HOW-9). | ✅ |
| **감사 로그** | 키 lifecycle·룰 변경 → `audit_logs`. | ✅ |

---

## 6. 빌드 순서 (backend-builder 착수 그래프)

각 단계는 다음의 선행. S1 검증 패턴(Gin/Redis/RLS/httpx/Notifier) 복제로 시작.

1. **platform 기반** — `config`·`db`(pgx+RLS 세션)·`httpx`(에러 규약)·`mtls`·`redisx`(Signaler)·`audit`. 마이그레이션 0001~0007 실행 파이프라인. → 전 모듈 토대.
2. **tenancy + RLS 가드** — org 해석 미들웨어 + `SET LOCAL app.current_org` + `tenants`(org→VM AccountID/X-Scope-OrgID 매핑). RLS 크로스테넌트 차단 통합 테스트.
3. **ingestkey (OBS-02)** — 키 발급/폐기/로테이션·해시·grace, 쿼터 스냅샷 계산, `/internal/authz/ingest-key`. → 수집 인증 선행 게이트.
4. **OTel Collector 배포 + authz 확장 + 라우팅 exporter** — OTLP/mTLS 수신, 키 검증(3 의존), VM/Tempo/Loki 테넌트 라우팅, live 복제. → 상시 수집 파이프라인 성립(OBS-01).
5. **live + WS (OBS-01/APM-02)** — `/internal/live-ingest` → Redis publish → WS hub org+stream fan-out(≤2초). 4 의존.
6. **explorer (OBS-03/04/05)** — metrics/traces/logs 프록시 + org 매처 강제. 4 의존(데이터 존재). 3·6은 2 이후 병렬 가능.
7. **alerting (OBS-06/07)** — 룰 CRUD·검증, vmalert 룰그룹 렌더/동기화, `/internal/alerts/webhook` 수신 → `alert_events` + Notifier. 6 의존(쿼리 표현).
8. **retentionjob (OBS-08)** — 플랜별 신호별 보존 배치 삭제(cron), 로컬 자원 경보. 4 의존.
9. **usage 롤업 + 과금 발행 (BILL-03)** — `observability_usage_rollups` 누적 + `klaro.usage.emitted(meter=...)` 발행. 3·4 의존.
10. **dashboards (OBS-09)** — spec JSONB CRUD. 6 의존. **마일스톤 이연 후보(M2+)**.
11. **snapshot 어댑터 (OBS-10)** — S4 구간 질의 → Explorer 위임. 6 의존.
12. **deploy/docker-compose + otel-collector.yaml** — obsplane·collector·VM·tempo·loki·vmalert·alertmanager·redis·mailhog·postgres 통합 기동 · E2E(SDK→저장→Explorer→알림).

**병렬 가능**: 3·6(2 이후), 7·8·9(4 이후), 10·11(6 이후).

---

## 7. 열린 질문 — 사용자 확정 결과 (2026-08-23, 단계 c)

전부 확정됨. 최종 결정만 기록(권고와 다른 경우 표시):

1. **쿼터 초과 동작** — ~~기본 차단~~ **전 플랜 공통 overage 과금 허용**(차단 없음). *권고(Free/Pro 차단)와 다름 — 사용자가 VU-Minutes와 동일한 병행 정책을 선택.* → `02-data-model.md`(`obs_max_ingest_gb_month` 비고) 반영.
2. **다운샘플링** — ~~이연~~ **지금 확정·설계**: vmalert recording rule 기반 메트릭 전용 롤업(HOW-10). 트레이스/로그는 대상 아님(리텐션만).
3. **대시보드(OBS-09) 마일스톤** — 권고대로 **M2+ 이연** 확정.
4. **관측 미터** — 권고대로 **호스트수(primary) + 수집 GB(secondary) 둘 다 채택** 확정(HOW-6). Billing 이벤트 계약(`meter` 판별자)은 그대로.
5. **SDK 배포·버전관리** — ~~별도 스코프~~ **지금 이어서 설계**: thin OTel wrapper·SemVer·무료 퍼블릭 레지스트리(HOW-11). 실제 3개 언어 구현은 여전히 별도 마일스톤(설계만 이번 스코프).
6. **MVP→TSDB 이행 시점** — 권고대로 **상시 경로(services/observability/) 안정 즉시** 이행 확정. 안정 기준(예: E2E 통과 + N일 무중단)은 backend-builder 착수 시 구체화.
7. **서비스 경계** — 권고대로 **신규 `services/observability/` 분리** 승인 확정.
