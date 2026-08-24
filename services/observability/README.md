# klaro 상시 관측 플랫폼 (S3) — 트랙 A 구현 완료

**최종 목표: Datadog 유사 상시 관측 제품.** 배포 적합성 리포트(S4)는 이 플랫폼의 데이터를
"특정 구간 스냅샷"으로 소비하는 하나의 소비자일 뿐이다.

설계 근거: `_workspace/05_architect_observability-design.md` ·
`docs/klaro/01-technical-design.md §2.4` · `docs/klaro/02-data-model.md §2.5`.

## 이 저장소 조각의 범위

설계 §6 빌드순서 **1~12단계 전체**가 구현되어 있다.

| 단계 | 내용 | 상태 |
|------|------|------|
| 1 | platform(config·db·httpx·mtls·redisx·audit) + 마이그레이션 파이프라인 | ✅ |
| 2 | tenancy(org 컨텍스트 + `SET LOCAL app.current_org`) + tenants(**영속** 백엔드 테넌트 배정) | ✅ |
| 3 | ingestkey (OBS-02 키 발급/목록/로테이션/폐기 · 쿼터 · `/internal/authz/ingest-key`) | ✅ |
| 4 | OTel Collector 게이트웨이(authz 확장 + 테넌트 라우팅 + 라이브 복제) | ✅ |
| 5 | live + WS fan-out(org+stream 허브, ≤2초) (OBS-01/APM-02) | ✅ |
| 6 | explorer(metrics/traces/logs 얇은 프록시, 서버사이드 org 강제) (OBS-03/04/05) | ✅ |
| 7 | alerting(룰 CRUD · vmalert 룰그룹 렌더/sync · webhook→이벤트 · Notifier) (OBS-06/07) | ✅ |
| 8 | retention + 다운샘플링(플랜별 보존 집행 · 5m/1h 롤업 · explorer 폴백) (OBS-08) | ✅ |
| 9 | usage 발행(호스트수 + 수집GB → klaro.usage.emitted) (BILL-03) | ✅ |
| 10 | dashboards(spec JSONB 패널 CRUD, 패널은 explorer 쿼리 참조) (OBS-09) | ✅ |
| 11 | snapshot 어댑터(리포트용 구간 조회를 explorer에 위임, 소유·저장 없음) (OBS-10) | ✅ |
| 12 | 통합 기동 + 전 기능 관통 E2E(16건) | ✅ |

`services/load-test/`의 project 단위 APM 경로(`apm_agents`, `apm_spans`, `apm_logs`)는
**리포트 스냅샷 경로로 존치**한다. 대체가 아니라 공존이다(설계 §0·§4.6).

## 수집 경로 한눈에

```
[운영 서비스 + OTel SDK]
   │  OTLP  ·  헤더 klaro-obs-key: <org 키 시크릿>
   ▼
[klaro-otelcol]  (collector/ — 커스텀 배포판)
   │  ① klaroauth 확장  → CP /internal/authz/ingest-key 로 키 검증(캐시)
   │       ↳ org · VM AccountID · X-Scope-OrgID 를 요청 컨텍스트에 심는다
   │  ② klarotenant 프로세서 → 그 값을 리소스 속성으로 각인(클라이언트 값은 덮어씀)
   │  ③ 라우팅
   │       metrics → vminsert  (AccountID 헤더 = org 테넌트)
   │       traces  → Tempo     (X-Scope-OrgID  = org)
   │       logs    → Loki      (X-Scope-OrgID  = org)
   │       metrics 사본 → CP /internal/live-ingest
   ▼
[obsplane CP]  → org별 Redis 채널 publish(klaro:obs:live:<org>:metric)
   │
   ▼
[WS 허브]  WS /orgs/:orgId/obs/live?stream=metric  — org+stream 구독자에게 fan-out
```

조회는 반대 방향의 얇은 프록시다.

```
[대시보드]  GET /orgs/:orgId/obs/{metrics/query,traces,traces/:id,logs}
   │  구조화 파라미터만(raw MetricsQL/TraceQL/LogQL 없음)
   ▼
[obsplane explorer]  org를 서버사이드에서 주입
   │   metrics → vmselect /select/<AccountID>/…  + klaro_org_id 매처
   │   traces  → Tempo  X-Scope-OrgID
   │   logs    → Loki   X-Scope-OrgID          + klaro_org_id 매처
   ▼
[VictoriaMetrics · Tempo · Loki]
```

테넌트는 **SDK가 보낸 값이 아니라 컨트롤 플레인이 발급한 값**이다. 이게 이 구조의 핵심이다:
`klarotenant`가 클라이언트가 붙인 `vm_account_id` / `klaro.org_id`를 무조건 덮어쓰므로
라벨 위조로 다른 org에 쓰는 경로가 없다.

## 구조

```
cmd/obsplane/            상시 관측 Control Plane 엔트리(공개 리스너 + 내부 mTLS 리스너)
internal/api/            Gin 라우터 · org 스코프 그룹 · 키/쿼터 핸들러 · 내부 플레인
internal/ingestkey/      OBS-02 관측 키: 발급·로테이션(grace)·폐기·해석·쿼터 스냅샷
internal/live/           라이브 프레임 정의 + org＋stream 팬아웃 허브(OBS-01)
internal/explorer/       metrics/traces/logs 조회 프록시 — 쿼리 조립·org 강제·보존 클램프·롤업 폴백
internal/alerting/       룰 CRUD · vmalert 룰그룹 렌더/sync · webhook 수신 · Notifier(OBS-06/07)
internal/retention/      플랜별 보존 집행 잡(OBS-08)
internal/usage/          계량 수집 + klaro.usage.emitted 발행(BILL-03)
internal/plans/          org→플랜 보존/쿼터 조회(한 곳에서만 읽는다)
internal/dashboards/     저장된 패널 레이아웃 — spec JSONB CRUD + 쓰기 시 쿼리 검증(OBS-09)
internal/snapshot/       리포트(S4)용 구간 조회 어댑터 — explorer에 위임, 저장 없음(OBS-10)
internal/platform/
  config/                환경변수 로딩 + 검증
  db/                    pgx 풀 · WithOrg(RLS 세션) · 마이그레이션 러너(advisory lock)
  httpx/                 에러 규약 {code,message,details} (S1과 동일 봉투)
  mtls/                  내부 평면 mTLS(클라이언트 인증서 필수)
  redisx/                Signaler — org+stream 라이브 pub/sub (S1 패턴 재사용)
  audit/                 audit_logs 기록기(obs.key.* / obs.rule.*)
internal/tenancy/        org 해석 미들웨어 + 크로스테넌트 경로 차단
internal/tenants/        org_id → VM AccountID / X-Scope-OrgID 영속 매핑
migrations/              0000 선행 의존성 + 0001~0010
collector/               커스텀 OTel Collector 배포판(klaroauth 확장 · klarotenant 프로세서)
deploy/                  compose: postgres · redis · obsplane · collector · VM 클러스터 · Tempo · Loki
```

## 멀티테넌시 (가장 중요)

격리는 **세 층**이고 전부 성립해야 한다.

1. `tenancy.Middleware` — 인증된 org와 경로의 `:orgId`가 다르면 `403 FORBIDDEN`. SQL 이전에 차단.
   인증된 org는 **서명 검증을 통과한 JWT 클레임**에서 오므로, 헤더나 쿼리로 바꿀 수 없다.
2. **Postgres RLS** — org 스코프 테이블 전부 `ENABLE` + **`FORCE`** ROW LEVEL SECURITY.
3. **저장소 native 테넌시** — VictoriaMetrics `AccountID`, Tempo/Loki `X-Scope-OrgID`(HOW-8).
   이 값은 CP만 정한다. 라벨 매처를 빠뜨려서 생기는 사고를 구조적으로 없앤다.

"CP만 정한다"를 실제로 지키려면 **클라이언트가 보낸 테넌트 헤더를 지워야** 한다. 게이트웨이의
요청 메타데이터는 대소문자를 구분하지만 `client.NewMetadata`는 모든 키를 소문자로 접는다.
그래서 위조한 `Klaro-Vm-Account-Id`와 권위값 `klaro-vm-account-id`를 함께 두면 둘이 한 항목으로
합쳐지고, **승자는 Go 맵 순회 순서가 정한다** — 그 순간 SDK가 지정한 org로 쓰기가 간다.
`klaroauth`가 권위값을 각인하기 전에 테넌트 4키를 대소문자 무시로 제거하는 이유다
(`extension.go` `withTenant`, 회귀 테스트 `TestForgedTenantHeadersAreIgnored`).

**앱은 반드시 `klaro_obs_app`(NOSUPERUSER·NOBYPASSRLS) 롤로 접속해야 한다.**
superuser/BYPASSRLS 롤로 붙으면 Postgres가 FORCE RLS를 면제하므로 SQL은 그대로인데
격리만 조용히 사라진다. `db.AssertRLSEnforced`가 부팅 시 이를 확인하고, 위반이면 기동을 거부한다.

DDL과 `CREATE ROLE`은 앱 롤 권한 밖이므로 **마이그레이션은 별도 관리자 DSN**
(`OBS_MIGRATE_DATABASE_URL`)으로 실행한다.

### RLS를 한 곳에서만 비켜 가는 이유 (`obs_resolve_ingest_key`)

수집 인증은 "아직 org를 모르는 상태에서 org를 알아내는 조회"다. RLS로는 풀 수 없는 닭과 달걀이라,
마이그레이션 0009가 **NOLOGIN·BYPASSRLS 롤이 소유한 SECURITY DEFINER 함수 하나**만 열어 둔다.
이 함수는 정확히 일치하는 `key_hash` 한 건의 org/상태만 돌려주며 목록·검색·열거를 제공하지 않는다.
앱 롤에 BYPASSRLS를 주는 것보다 노출면이 훨씬 좁다.

## 관측 키와 쿼터 (OBS-02)

| 메서드 | 경로 | 응답 |
|---|---|---|
| POST | `/orgs/:orgId/obs/keys` | 201 `{id, name, key_prefix, status, secret}` — **시크릿은 여기서 한 번만** |
| GET | `/orgs/:orgId/obs/keys` | 200 `{data:[…]}` (시크릿 없음) |
| POST | `/orgs/:orgId/obs/keys/:keyId/rotate` | 200 신규 키 + `grace_until`(구키 만료 시각) |
| DELETE | `/orgs/:orgId/obs/keys/:keyId` | 204 — grace 없이 즉시 거부. 재요청도 204(멱등) |
| GET | `/orgs/:orgId/obs/quota` | 200 두 미터 스냅샷 |

- 저장되는 건 `sha256(secret)`뿐이다. 분실한 키는 복구가 아니라 재발급이다.
- 로테이션은 신규 키를 만들고 **구키에 grace 시계**를 건다(둘 다 통과 → 무중단 재배포).
  이름은 신규 키가 이어받고 구키는 `<이름> (rotated <uuid>)`로 개명된다.
- 쿼터 2축(HOW-4/6): **활성 호스트수 primary + 월 수집 GB secondary**.
  **초과해도 차단하지 않는다** — 전 플랜 overage 과금(§7-1 사용자 확정).
  트래픽이 튈 때 트레이스를 끊는 건 관측이 가장 필요한 순간에 관측을 끄는 일이다.
- 키 발급/로테이션/폐기는 `audit_logs`에 `obs.key.*`로, **변경과 같은 트랜잭션에서** 기록된다.

## 라이브 스트림 (OBS-01/APM-02)

`WS /orgs/:orgId/obs/live?stream=<metric|service>` — 프레임은 `{ts, stream, points:[{labels, value}]}`.

- **자격증명은 헤더 또는 서브프로토콜로 온다.** 브라우저 WebSocket API는 핸드셰이크에
  헤더를 붙일 수 없어서, 이 라우트 하나만 `Sec-WebSocket-Protocol: klaro-bearer, <토큰>`도
  읽는다(`tenancy.SubprotocolToken`, 라우트 단위 opt-in). 헤더가 있으면 헤더가 이기고,
  서버는 선택한 서브프로토콜을 응답에 에코한다 — 에코가 없으면 브라우저가 인증에 성공한
  연결을 스스로 끊는다. 쿼리 파라미터는 받지 않는다: URL에 실린 토큰은 프록시 로그·히스토리·
  Referer에 남고, 여기서 토큰 하나는 org 하나다.
- **인가 실패는 close 코드로 알린다.** 브라우저 WebSocket API는 핸드셰이크 상태코드를
  노출하지 않아서(거절되면 그냥 1006), 다른 org를 요청한 인증된 클라이언트는 소켓을
  받은 뒤 **4403**으로 닫힌다. 자격증명이 아예 없으면 소켓 없이 401이다 —
  익명 호출자에게는 알려줄 것이 없다.
- OTLP→프레임 변환은 **수집 시점에 한 번** 한다. 한 org를 스무 화면이 보고 있어도
  Collector 플러시마다 OTLP를 스무 번 파싱하지 않는다.
- 허브는 org+stream당 Redis 구독 **하나**를 공유하고, 마지막 구독자가 떠나면 해제한다.
  느린 탭은 **가장 오래된 프레임을 버린다**(drop-oldest) — 뒤처진 라이브 뷰는 놓친 것을
  재생하는 게 아니라 지금을 보여줘야 한다.
- `stream=service`는 채널만 존재한다(발행자는 후속 마일스톤).

## Explorer (OBS-03/04/05)

| 메서드 | 경로 | 파라미터 |
|---|---|---|
| GET | `/orgs/:orgId/obs/metrics/query` | `metric` · `filter`(반복/콤마) · `agg` · `step`(초) · `from`/`to` |
| GET | `/orgs/:orgId/obs/traces` | `service` · `min_duration_ms` · `limit` · `from`/`to` |
| GET | `/orgs/:orgId/obs/traces/:traceId` | — (span 워터폴) |
| GET | `/orgs/:orgId/obs/traces/:traceId/correlated` | `pad_sec`(≤900) · `log_limit` · `step` · `metric`(반복) — 스팬 + 그 trace_id의 로그 + 서비스/호스트 메트릭 |
| GET | `/orgs/:orgId/obs/logs` | `filter` · `query`(리터럴 부분문자열) · `limit` · `from`/`to` |

## 인프라 (호스트 인벤토리 · hostmap · 업타임 SLO)

| 메서드 | 경로 | 파라미터 |
|---|---|---|
| GET | `/orgs/:orgId/obs/hosts` | — (레지스트리 + 최신 cpu/mem/disk/load) |
| GET | `/orgs/:orgId/obs/hosts/:hostIdent/metrics` | `step`(초) · `from`/`to` |
| GET | `/orgs/:orgId/obs/slo/uptime` | `step`(초) · `target`(0<t≤1) · `host` · `from`/`to` |

수집은 `deploy/otel-hostmetrics.yaml`의 **hostmetrics 리시버**다. 별도 에이전트 컨테이너
(`otel-hostagent`)가 다른 SDK와 **똑같이** `klaro-obs-key`로 게이트웨이에 OTLP를 보내므로,
org 각인·쿼터 계량·VM 테넌트 라우팅이 전부 기존 경로 그대로다. 게이트웨이 안에 스크레이프
파이프라인을 두지 않는 이유는 그 파일 머리말에 있다 — 스크레이프에는 테넌트를 실어 줄 요청
컨텍스트가 없고, 고정 org를 각인하는 우회로를 뚫는 것은 "테넌트는 CP가 준 값뿐"이라는
불변식을 깨는 일이다.

조인 키는 `host_ident`(= `service.instance.id` = 시리즈의 `instance` 라벨) 하나뿐이고,
조인은 애플리케이션(`internal/inventory`)에서 한다 — 시계열은 RDB 밖이므로 SQL로 조인할
대상이 없다. 레지스트리에는 있는데 시계열이 없는 호스트는 **행이 남고 값만 null**이다:
보고가 끊긴 호스트가 화면에서 사라지는 것은, 사람이 그것을 알아채야 할 바로 그 순간에
사라지는 것이다.

업타임 SLO의 가용률은 **보고 커버리지**다(도달성이 아니다). 분모는 응답에 온 버킷 수가
아니라 요청 구간에서 유도하며, 데이터가 없으면 100%가 아니라 빈 측정을 돌려준다.

`from`/`to`는 RFC3339 · unix 초 · unix 밀리초를 모두 받고, 없으면 최근 1시간이다.

**raw 쿼리는 받지 않는다.** MetricsQL/TraceQL/LogQL은 전부 서버가 조립하고, org는
서버사이드에서 주입한다(설계 HOW-2). 이유는 하나다 — raw 쿼리는 호출자가 라벨 매처를
**벗길 수 있는** 쿼리다.

- `filter`는 `label=value` / `label!=value`만. 값은 리터럴(정규식 아님)이고
  `strconv.Quote`로 인용되므로 인용부호를 닫고 새 매처를 열 수 없다.
- `klaro_org_id` · `vm_account_id` · `vm_project_id` · `__name__`은 **예약**이다.
  호출자가 다시 쓸 수 있으면 다른 org로도 쓸 수 있다 → 422.
- `agg` 화이트리스트: `rate` `avg` `sum` `count` `p50` `p95` `p99`. 전부 `*_over_time`
  계열이다(step이 있으니 "각 시리즈를 이 버킷으로 요약"이라는 뜻).
- 응답에는 생성된 쿼리(`query`)가 함께 온다. org 매처가 실제로 붙었는지 **눈으로
  확인 가능**하게 만드는 장치다.
- 내부 라벨(`klaro_org_id`·`vm_account_id`·`vm_project_id`)은 응답에서 제거된다.
- 백엔드 장애는 502(`telemetry backend unavailable`)다. 업스트림 본문은 절대 전달하지
  않는다 — 다른 테넌트의 라벨명이나 내부 호스트명이 실려 올 수 있다.

## 알림 (OBS-06/07)

| 메서드 | 경로 |
|---|---|
| POST · GET | `/orgs/:orgId/obs/alert-rules` |
| GET · PATCH · DELETE | `/orgs/:orgId/obs/alert-rules/:ruleId` |
| GET | `/orgs/:orgId/obs/alert-events` — `state` · `rule_id` · `from`/`to` · `limit` |

룰도 **raw 쿼리를 받지 않는다.** explorer와 같은 구조화 파라미터(`query_spec`)만 받고
서버가 org 매처를 주입해 MetricsQL로 렌더한다 — raw 식을 받으면 explorer에서 막아둔
구멍이 알림 경로로 되돌아온다. 렌더 결과는 응답의 `query`로 확인할 수 있다.

- **MVP는 `metric` 신호만.** vmalert가 메트릭 전용이므로 `log`/`trace`는 422다.
  컬럼은 미리 있지만, 저장해두면 "조용히 절대 발동하지 않는 룰"이 되므로 거부한다.
- `comparator`는 `gt`/`gte`/`lt`/`lte`, `severity`는 `info`/`warning`/`critical`.
- `for_duration_sec: 0`은 "첫 위반에 즉시 발동"이다(기본값은 60초).
- 이벤트는 (룰, 라벨셋) 지문으로 **중복 제거**된다. vmalert는 발동 중인 알림을
  재전송 주기마다 다시 보내므로, 없으면 문제가 지속되는 동안 1분에 한 행씩 쌓인다.
- 알림 페이로드는 `klaro.obs.alert`로 발행되고 Notifier가 구독해 전달한다(MailHog/Slack).
  발송을 요청 경로에서 하지 않는 이유: 느린 SMTP가 vmalert를 붙잡으면 안 된다.
- 룰 lifecycle은 `audit_logs`에 `obs.rule.*`로, 변경과 같은 트랜잭션에서 기록된다.

### vmalert 멀티테넌시 (스파이크 결과)

v1.150.0 OSS vmalert에는 **`-clusterMode`도 룰그룹 `tenant:` 필드도 없다**(전체 `-help`에서
tenant 언급은 경로 템플릿 예시뿐). 즉 인스턴스 하나가 네이티브로 테넌트별 격리 평가를
하지 못한다. 그렇다고 org마다 인스턴스를 띄울 필요는 없었다 — 스파이크로 확인한 구성은:

```
단일 vmalert
  -datasource.url   = …/select/multitenant/prometheus   (읽기)
  -remoteWrite.url  = …/insert/multitenant/prometheus   (쓰기)
룰그룹 = org당 하나, CP가 렌더
  expr   에 klaro_org_id 매처 강제 주입   → 읽기 격리
  labels 에 vm_account_id                → 쓰기 격리(org 테넌트로 라우팅)
```

2개 org로 실측: tenant 1 롤업 = 10(orgA만), tenant 2 = 99(orgB만), 두 룰 `health=ok`.
격리는 웨이브2~3에서 이미 쓰던 두 층 그대로 성립하고 컨테이너는 하나로 끝난다.

룰 파일은 **JSON으로 쓰고 확장자만 `.yaml`**이다. YAML은 JSON의 상위집합이라 vmalert가
그대로 파싱하고, 표준 라이브러리가 이스케이프를 처리한다. 고객 라벨 문자열이 들어가는
값에 YAML 인용을 손으로 붙이는 코드는 프로덕션에서만 파스 에러를 낸다.

## 보존과 다운샘플링 (OBS-08)

보존은 **층으로** 집행한다. 백엔드 셋 중 시간범위 삭제가 가능한 건 하나뿐이기 때문이다.

| 층 | 무엇을 | 왜 |
|---|---|---|
| explorer 창 클램프 | 모든 조회를 플랜 창으로 좁힌다 | 계약이 요구하는 "플랜 초과 데이터 조회 불가"를 **정확·즉시** 성립시킨다 |
| Loki delete API | org별 시간범위 삭제 | 시간범위 삭제가 가능한 유일한 백엔드 |
| VM `delete_series` | **완전히 만료된 org**의 시리즈 삭제 | VM은 시간범위 삭제가 없다. 여전히 보내는 org는 건드리지 않고, 클러스터 전역 리텐션(최대 플랜)으로 늙힌다 |
| Tempo | 전역 `block_retention`만 | per-tenant delete API가 없다. 호출할 것이 없으므로 그렇다고 로그로 말한다 |

위 셋의 빈틈은 OSS 백엔드의 실제 제약이고 미완성 의도가 아니다. 고객 약속을 지키는
층은 explorer 클램프다.

**다운샘플링**(HOW-10)은 vmalert recording rule로 만든다. `klaro_rollup5m`과
`klaro_rollup1h` 두 시리즈이고, 원래 메트릭 이름은 `klaro_metric` 라벨에 담긴다 —
recording rule이 `__name__`을 롤업 이름으로 덮어쓰기 때문에 이름을 라벨에 옮겨두지
않으면 잃는다. 카디널리티는 raw와 같다. 1h는 5m에서 파생한다(raw 1시간을 다시 읽는 건
같은 답에 12배의 일이다). 5m 룰은 `__name__!~"klaro_rollup…"`로 **자기 출력을 제외**한다 —
없으면 매 주기 자기 롤업을 다시 롤업해 시리즈가 무한히 늘어난다.

explorer는 요청 창이 raw 보존을 넘으면 자동으로 롤업으로 폴백하고, 응답에
`resolution: raw|5m|1h`과 `clamped`을 실어 보낸다. 저해상도를 raw인 것처럼 주면
성긴 버킷이 "조용한 시스템"처럼 보인다.

**트레이스/로그는 다운샘플링 대상이 아니다.** OSS 생태계에 표준 개념이 없고, 있는 척하면
백엔드가 지킬 수 없는 기대를 만든다. 두 신호는 리텐션만 적용한다.

## 과금 계량 (BILL-03)

미터 2축(HOW-6): **활성 호스트수**(플랜 가격의 기준)와 **월 수집 GB**(보조 가드).

```
[게이트웨이 klarousage 프로세서]  신호별 바이트·건수 + host_ident 집계
   │  POST /internal/usage (내부 플레인)
   ▼
[obsplane]  observability_hosts upsert + observability_usage_rollups 누적
   │  주기 emitter
   ▼
klaro.usage.emitted  {org_id, meter, quantity(증분), period, computation}
```

- 계량이 **게이트웨이**에 있는 이유: 세 신호가 모두 지나는 유일한 지점이다. 메트릭만
  CP로 복제되고 트레이스·로그는 저장소로 직행하므로, CP에서 세면 셋 중 하나만 센다.
- 바이트는 **OTLP protobuf 크기**다. 고객이 실제로 보낸 양이고, 클라이언트가 어떤 압축을
  켰는지에 좌우되지 않는다.
- 발행은 **증분**이고 `observability_usage_emissions` 원장이 멱등성 키다. 재시작·크론
  겹침·중복 실행이 모두 같은 델타를 계산하거나 아무것도 발행하지 않는다. 청구에서는
  두 번 하는 것이 안 하는 것보다 나쁘다.
- 호스트 미터도 증분이다 — 최고수위(max)의 증가분을 싣기 때문에 Billing이 더하면
  그 달의 peak가 된다.
- **쿼터 초과는 아무것도 막지 않는다**(§7-1 사용자 확정). 계량이 초과를 청구 항목으로
  바꾸는 것이고, 트레이스를 버리는 것이 아니다.
- `observability_hosts`를 쓰는 주체가 생긴 것도 이 단계다. 그전까지 활성 호스트 카운트는
  빈 테이블을 읽어 모든 org가 유휴로 보였다.

## 대시보드 (OBS-09)

| 메서드 | 경로 |
|---|---|
| POST · GET | `/orgs/:orgId/obs/dashboards` |
| GET · PATCH · DELETE | `/orgs/:orgId/obs/dashboards/:dashId` |

대시보드는 **JSONB 문서 하나**다(HOW-3). `dashboard_panels` 테이블은 없다 — 패널은 자신을
담은 대시보드 밖에서 정체성이 없고, 렌더링은 klaro 프런트가 Explorer API로 한다.
Grafana 임베드가 아니므로 패널이 외부에서 주소 지정될 필요가 없다.

```json
{"name":"checkout","spec":{
  "range_sec":3600, "refresh_sec":30,
  "panels":[{"id":"latency","title":"p95","type":"timeseries",
             "layout":{"x":0,"y":0,"w":6,"h":4},
             "query":{"signal":"metrics","metric":"http_server_duration",
                      "agg":"p95","step_sec":60,
                      "filters":[{"label":"service_name","value":"checkout"}]}}]}}
```

**패널 쿼리는 쓰기 시점에 Explorer와 같은 규칙으로 검증한다.** 이게 이 패키지의 핵심이다 —
없으면 대시보드가 "Explorer가 거부할 쿼리를 저장하는 수단"이 되고(예약 라벨을 적은 패널
포함), 거부는 나중에 그 대시보드를 연 사람에게만 드러난다.

- `signal`은 `metrics`/`traces`/`logs`, `type`은 `timeseries`/`stat`/`table`/`logs`/`traces`.
- 다른 신호의 필드는 무시하지 않고 **거부**한다. 무시된 필드는 뭔가 한 것처럼 보인다.
- 예약 라벨(`klaro_org_id` 등)은 422. 패널이 org 매처를 다시 쓸 수 있으면 다른 org로도
  쓸 수 있고, 그게 **저장**된다.
- 패널 수·그리드·step·limit·refresh 모두 상한이 있다. 대시보드는 매 페이지 로드마다
  읽히는 행에 담긴 호출자 제공 JSON이다.
- lifecycle은 `audit_logs`에 `obs.dashboard.*`로 기록된다(패널 문서 자체는 복사하지 않는다 —
  감사에 필요한 건 누가 언제 무엇을 바꿨는지다).

## 리포트 스냅샷 (OBS-10)

`POST /internal/snapshot` (내부 플레인) — 배포 리포트(S4)가 "14:02~14:09에 이 서비스는
어땠는가"를 묻는 경로다(설계 §4.6).

```json
{"org_id":"…","from":"…","to":"…",
 "metrics":[{"key":"latency","metric":"http_server_duration","agg":"p95","step_sec":15}],
 "traces":{"service":"checkout","min_duration_ms":3000,"limit":20}}
```

어댑터는 **아무것도 소유하지 않고 아무것도 저장하지 않는다.** 조회를 Explorer에 위임할
뿐이다. 이 결정에는 두 방향의 이유가 있다:

- Explorer를 거치므로 org 테넌트 강제·플랜 보존 클램프·롤업 폴백이 여기에도 그대로
  적용된다. 두 번째 조회 경로는 그것들을 잊을 두 번째 장소다.
- 저장하지 않으므로 리포트가 텔레메트리의 소유자가 될 수 없다. 상시 수집·저장·보존은
  리포트가 성공하든 실패하든 아예 실행되지 않든 계속된다 — 리포트는 구간의 소비자이고,
  데이터가 존재하는 이유가 아니다. 결합하면 리포트 삭제가 관측 데이터를 함께 가져가거나,
  실패한 리포트가 플랜을 넘긴 데이터를 살려두게 된다.
- 창이 플랜 보존을 넘으면 응답에 `partial: true`와 `notes`가 실린다. 짧은 선을 그리고
  말하지 않으면 읽는 사람은 나머지가 조용했다고 이해한다.
- 메트릭 쿼리 하나가 실패해도 스냅샷은 실패하지 않는다. 차트 다섯 중 넷이 있는 리포트는
  쓸모가 있고, 에러 페이지인 리포트는 없다. 무엇이 없는지는 `notes`에 적는다.
- Go 패키지가 아니라 HTTP로도 노출하는 이유: S4는 다른 모듈의 다른 서비스라서, import할
  수 없는 패키지는 종이 위의 설계다.

## 환경변수

| 변수 | 기본값 | 비고 |
|------|--------|------|
| `OBS_ADDR` | `:8090` | 공개 HTTP |
| `OBS_DATABASE_URL` | `postgres://klaro_obs_app:…/klaro_obs` | **non-superuser 롤 필수** |
| `OBS_MIGRATE_DATABASE_URL` | `postgres://klaro:…/klaro_obs` | 관리자 DSN(DDL) |
| `OBS_REDIS_ADDR` | `localhost:6379` | 라이브 pub/sub |
| `OBS_AUTO_MIGRATE` | `true` | 부팅 시 마이그레이션(advisory lock으로 직렬화) |
| `OBS_DB_MAX_CONNS` | `10` | |
| `OBS_ACTIVE_HOST_WINDOW_SEC` | `900` | 활성 호스트 카운트 창 |
| `OBS_KEY_ROTATION_GRACE_SEC` | `86400` | 로테이션 grace |
| `OBS_AUTHZ_CACHE_TTL_SEC` | `30` | Collector 캐시 상한 = 폐기 반영 최대 지연 |
| `OBS_KEY_TOUCH_WINDOW_SEC` | `60` | `last_used_at` 기록 주기 |
| `OBS_INTERNAL_ADDR` | `:8443` | 내부 리스너(Collector·vmalert) |
| `OBS_TLS_CA_FILE` / `OBS_TLS_CERT_FILE` / `OBS_TLS_KEY_FILE` | — | 셋을 함께 설정해야 한다(부분 설정은 기동 실패) |
| `OBS_INTERNAL_INSECURE` | `false` | 내부 평면 **전송**만 평문. 개발 전용, 명시적으로 켜야 하고 `OBS_INTERNAL_TOKEN` 없이는 기동 실패 |
| `OBS_INTERNAL_TOKEN` | — | 내부 평면 공유 비밀(24자 이상). Collector·vmalert가 `Authorization: Bearer`로 제시 |
| `OBS_VMSELECT_URL` | — | Explorer 메트릭 백엔드(vmselect). 비면 "백엔드 없음"으로 502 |
| `OBS_TEMPO_URL` | — | Explorer 트레이스 백엔드 |
| `OBS_LOKI_URL` | — | Explorer 로그 백엔드 |
| `OBS_EXPLORER_TIMEOUT_SEC` | `30` | 백엔드 질의 1건 상한 |
| `OBS_EXPLORER_MAX_ROWS` | `1000` | 질의당 행 상한(보존창 전체를 메모리로 끌어오지 못하게) |
| `OBS_RULE_FILE` | — | vmalert가 읽는 룰 파일. 비면 룰 sync를 끈다 |
| `OBS_VMALERT_RELOAD_URL` | — | 즉시 reload. 비면 vmalert 자체 폴링에 맡긴다 |
| `OBS_RULE_SYNC_INTERVAL_SEC` | `60` | 룰 파일 전체 재렌더 주기 |
| `OBS_DASHBOARD_BASE_URL` | — | 알림에 실리는 링크 접두사 |
| `OBS_SMTP_ADDR` / `OBS_SMTP_FROM` | — / `alerts@klaro.local` | 개발은 MailHog. 인증 없음 |
| `OBS_SLACK_WEBHOOK_URL` | — | 비면 로그 no-op으로 폴백(다른 채널을 막지 않는다) |
| `OBS_RETENTION_INTERVAL_SEC` | `21600` | 보존 집행 주기 |
| `OBS_RETENTION_DRY_RUN` | `false` | 삭제 없이 로그만. 새 환경 첫 실행에 쓴다 |
| `OBS_USAGE_INTERVAL_SEC` | `3600` | 과금 미터 발행 주기 |
| `OBS_ENV` | `development` | `development` \| `production`. 프로덕션은 아래 개발용 완화를 전부 금지한다 |
| `OBS_JWT_ALG` | `HS256` | `HS256` \| `RS256`. **토큰이 선언한 alg가 아니라 이 값으로만 검증한다** |
| `OBS_JWT_HS_SECRET` / `OBS_JWT_HS_SECRET_FILE` | — | HS256 비밀(32바이트 이상). 둘 중 하나만 |
| `OBS_JWT_PUBLIC_KEY_FILE` | — | RS256 검증 공개키(PEM). RS256이면 필수 |
| `OBS_JWT_ISSUER` / `OBS_JWT_AUDIENCE` | — | 설정하면 `iss`/`aud`가 일치해야 한다 |
| `OBS_JWT_LEEWAY_SEC` | `60` | `exp`/`nbf`/`iat` 시계 오차 허용 |
| `OBS_DEV_AUTH` | `false` | 개발 인증 스텁을 켠다. **프로덕션 프로파일에서는 금지** |
| `OBS_DEV_TOKEN` / `OBS_DEV_ORG_ID` / `OBS_DEV_ROLE` | — / — / `owner` | 스텁 설정. `OBS_DEV_AUTH`일 때 앞 둘은 필수 |
| `OBS_DEV_CORS_ORIGINS` | — | 교차출처 허용 오리진(콤마 구분, `scheme://host[:port]`). 대시보드 로컬 개발용. **프로덕션 프로파일에서는 금지** |

TLS 번들도 없고 `OBS_INTERNAL_INSECURE`도 아니면 내부 리스너는 **뜨지 않는다**(로그로 알린다).
mTLS가 계약이므로, 번들이 없다고 평문으로 조용히 내려앉지 않는다.

### 배포 프로파일이 무엇을 막는가

`OBS_ENV=production`은 개발용 완화 세 개를 **기동 실패**로 바꾼다. 경고 로그가 아니라 실패인
이유는, 이 셋 중 어느 것도 프로덕션에서 "알고 감수하는 위험"이 될 수 없기 때문이다.

| 완화 | 프로덕션에서 | 왜 |
|------|-------------|-----|
| `OBS_DEV_AUTH` | 거부 | 고정 문자열 하나가 org owner로 인증된다 |
| `OBS_INTERNAL_INSECURE` | 거부 | 내부 토큰과 모든 페이로드가 평문으로 흐른다 |
| DSN `sslmode=disable`/`prefer`/`allow`, 또는 미지정 | 거부 | 전 테넌트의 행과 격리 스코프가 평문으로 흐른다. `prefer`/`allow`는 조용히 평문으로 내려앉는다 (F-5) |

기본 DSN의 `sslmode`도 프로파일을 따른다: 개발은 `disable`, 프로덕션은 `require`.

### 인증과 인가 (공개 평면)

기본이자 프로덕션의 유일한 경로는 **서명된 Bearer JWT**다. 스텁은 `OBS_DEV_AUTH` 뒤에만 있다.

검증은 표준 라이브러리로 구현했다(`platform/jwtauth`). 유료 의존이 아니라 유지보수 판단이다
([COST-05]는 유료만 금지한다) — 어차피 한 줄씩 읽어야 하는 코드라면, 감사할 의존성을 늘리지
않는 편이 낫다. 거절하는 것과 그 이유:

- **헤더의 `alg`가 설정값과 다르면** 거절. 토큰이 선언한 알고리즘을 따라가는 것이 전형적인 JWT
  파괴다 — `"alg":"none"`은 누구나 인증되고, RS256 배포에 `"alg":"HS256"`을 보내면 **공개키로
  서명**할 수 있다. 비교 대상이 "우리가 할 수 있는 알고리즘 목록"이 아니라 **설정된 단 하나**인
  것이 이 두 공격을 한 줄로 닫는다.
- **`exp` 없는 토큰** 거절. 기다려서 폐기할 수 없는 자격증명은 유출이 곧 영구 유출이다.
- **`org_id`/`role` 없거나 못 쓰는 값** 거절. 이 둘이 RLS 스코프와 인가 결정이 되므로, 이상한
  값은 기본값으로 내려앉는 대신 닫는다. 네 역할 밖의 `role`은 "낮은 역할"이 아니라 **역할 없음**
  이다(viewer로 조용히 강등되지 않는다).

`org_id`는 **경로가 고르지 않는다**. 스코프는 자격증명이 보증한 org이고 경로의 `:orgId`는 그것을
지목만 할 수 있다(`tenancy.Resolve`). 그래서 위조 `org_id`는 두 겹에 막힌다 — 서명 없이는 인증
자체가 안 되고, 발급자가 서명한 토큰이라도 자기 org만 열리므로 다른 org 경로에 겨누면 403이다.

**역할별 인가**(설계 §2.1 `owner / admin / member / viewer`)는 라우터에서 두 그룹으로 갈린다:

| 게이트 | 최소 역할 | 대상 |
|--------|----------|------|
| 조회 | `member` | 테넌트 프로브, 키 목록·쿼터, Explorer(메트릭·트레이스·로그), 룰·이벤트 조회, 대시보드 조회, 라이브 WS |
| 변경 | `admin` | 키 발급·로테이션·폐기, 알림 룰 생성·수정·삭제, 대시보드 생성·수정·삭제 |

조회 하한이 `viewer`가 아니라 `member`인 것은 의도다: 이 평면은 테넌트의 관측 이력 전체를
노출하므로 열람이 최저 권한일 수 없다. `viewer`는 klaro 전체 RBAC의 역할이고 여기서는 아직
부여가 없다 — 바꾸려면 `router.go`의 `read` 그룹 인자 한 개다.

라이브 WebSocket도 같은 `member+` 문턱이지만 거절을 **close code**로 알린다. 브라우저
WebSocket API는 핸드셰이크 상태를 노출하지 않아서, HTTP 상태로 거절하면 클라이언트가 1006만
본다(설계 §4.3).

## 내부 평면: 전송과 인증은 별개다 (F-3)

`/internal/*`은 수집 키를 해석하고, 임의 org의 라이브 프레임을 발행하고, 과금 행을 쓰고,
알림 이벤트를 주입한다. 예전에는 `OBS_INTERNAL_INSECURE` 하나가 **암호화와 인증을 동시에**
껐다 — 포트에 닿을 수 있는 무엇이든 위의 전부를 할 수 있었다. 이제 둘은 분리되어 있다.

`api.InternalAuth`가 모든 `/internal/*` 라우트에서 **둘 중 하나**를 요구한다:

- **검증된 클라이언트 인증서** — `mtls.ServerConfig`가 `RequireAndVerifyClientCert`이므로
  `VerifiedChains`가 비어 있지 않다는 것은 TLS 계층이 이미 피어를 CA로 인증했다는 뜻이다.
  프로덕션 경로이고, 토큰이 따로 필요하지 않다.
- **`OBS_INTERNAL_TOKEN`** — 상수 시간 비교. TLS를 끈 채로도 인증이 유지되는 이유다.
  `OBS_INTERNAL_INSECURE`를 토큰 없이 켜면 `config.Load`가 기동을 거부한다.

토큰이 비어 있으면 **아무도** 통과하지 못한다(전원 통과가 아니다). 비밀이 빠졌을 때의
실패 모드는 닫힌 문이어야 한다. `/healthz`만 열려 있다 — 데이터를 읽지도 반환하지도 않는
컨테이너 프로브다.

한 가지 진단 장치: 이 평면의 401에는 `WWW-Authenticate: Bearer realm="klaro-internal"`이
붙는다. `/internal/authz/ingest-key`에서 401은 두 가지를 뜻할 수 있는데 — 고객의 수집 키가
거절됐거나, **게이트웨이 자신이** 거절됐거나 — 후자를 전자로 착각하면 멀쩡한 키를 negative
캐시에 넣고 로그에는 "전 고객 키가 폐기됨"처럼 남는다. `klaroauth`가 이 realm을 보고 갈라낸다.

클라이언트 쪽 설정: `klaroauth.internal_token`, `klarousage.internal_token`(파일 형태는
`*_token_file`), 라이브 복제 익스포터는 `headers.Authorization`, vmalert는
`-notifier.bearerToken`. 네 곳이 obsplane의 `OBS_INTERNAL_TOKEN`과 같은 값을 공유한다.
게이트웨이 쪽 두 프로세서는 `tls.insecure`인데 토큰이 없으면 **기동 시** 실패한다 — 계량은
fire-and-forget이라 401이 조용히 누적되고, 그건 "데이터를 안 보내는 org"처럼 보인다.

## mTLS를 어디서 강제하나

`CLAUDE.md`는 SDK↔Collector를 **mTLS 필수**로 못박는다. 그 강제가 실제로 일어나는
지점은 딱 한 곳이다:

| 홉 | 강제 지점 | 파일 |
|----|-----------|------|
| **SDK → Collector** | `receivers.otlp.protocols.{grpc,http}.tls.client_ca_file` | `deploy/otel-collector.prod.yaml` |
| Collector → CP(authz) | `extensions.klaroauth.tls` + `https://` 엔드포인트 | 같은 파일 (`klaroauth` Validate가 강제) |
| Collector → CP(usage·live) | `processors.klarousage.tls` · `exporters.otlp_http/live.tls` | 같은 파일 |
| CP 내부 리스너 | `OBS_TLS_CA_FILE`/`CERT`/`KEY` (없으면 리스너가 뜨지 않음) | 환경변수 |

`client_ca_file`이 비어 있지 않으면 `configtls.ServerConfig`가
`tls.RequireAndVerifyClientCert`를 세팅한다(`ServerConfig.LoadTLSConfig`). 즉 **이 키
하나가 강제 스위치**이고, 클라이언트 인증서 없이 붙는 SDK는 핸드셰이크에서 끊긴다.
별도의 `require_client_cert` 키는 collector `configtls` v1.65.0 스키마에 **없다** —
넣으면 알 수 없는 필드로 기동이 실패한다.

mTLS와 관측 키는 층이 다르다: mTLS는 "연결해도 되는가", `klaro-obs-key`는 "어느
org인가"를 답한다. SDK 기본값이 insecure인 것은 `SDK_CONTRACT.md` §4의 승인된
결정이므로 그대로 두고, 강제는 서버 쪽에서 한다.

**개발과 프로덕션의 갈림은 설정 파일 한 장**이다. `deploy/otel-collector.yaml`은
평문 기준선이고, 프로덕션은 그 위에 오버레이를 겹친다(Collector가 `--config`를
순서대로 딥머지한다).

```bash
klaro-otelcol --config /etc/klaro/otel-collector.yaml \
              --config /etc/klaro/otel-collector.prod.yaml
```

머지 결과는 기동 전에 확인할 수 있다. `validate`는 파싱만 하지 않고 컴포넌트까지
만들어 보므로, **인증서 파일이 실제로 있어야** 통과한다 — 오타뿐 아니라 빠진 번들도
잡힌다. 두 설정과 `tls/` 번들을 한 디렉터리(`$BUNDLE`)에 두고 넘긴다.

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$BUNDLE:/etc/klaro" \
  --entrypoint /usr/local/bin/klaro-otelcol klaro-otelcol \
  validate --config /etc/klaro/otel-collector.yaml \
           --config /etc/klaro/otel-collector.prod.yaml
```

실측으로 확인한 것: 이 구성으로 게이트웨이를 띄우고 클라이언트 인증서 **없이**
4317·4318에 붙으면 TLS1.3 `alert 116 (certificate required)`로 끊기고, sdk-ca가 서명한
인증서로 붙으면 핸드셰이크가 성립한다.

리시버의 mTLS는 `tls` 블록의 **존재**로 켜지므로 환경변수 하나로 끄고 켤 수 없다.
그래서 주석으로 준비만 해두는 대신 오버레이 파일로 분리했다 — 한 번도 적용된 적
없는 설정이 주석으로 남아 있으면 켜져 있다고 착각하기 쉽다. 개발에서 오버레이를
빼면 평문이 되지만, 그때는 베이스의 `insecure: true`가 명시적 요청 표시다.
저장소 계층(vminsert/tempo/loki)의 TLS는 아직 이 오버레이 밖이다.

## 빌드 · 테스트

로컬에 Go 툴체인이 없어도 된다. `docker` 이미지로 실행한다(`$PWD`는 이 디렉토리).

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.25 go test ./...
```

Windows Git Bash에서는 경로 변환을 끄고 절대경로를 넘긴다.

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W):/src" -w //src golang:1.25 go test ./...
```

Collector 컴포넌트는 별도 모듈이다.

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W)/collector:/src" -w //src golang:1.25 go test ./...
```

### 통합 테스트 (RLS 격리 · 키 수명주기 · 쿼터)

실제 Postgres가 필요하다. 두 DSN을 준다 — 관리자(마이그레이션·시드)와 앱 롤(격리 검증).

```bash
docker network create klaro-obs-test
docker run -d --name klaro-obs-pg --network klaro-obs-test \
  -e POSTGRES_USER=klaro -e POSTGRES_PASSWORD=klaro -e POSTGRES_DB=klaro_obs postgres:16

MSYS_NO_PATHCONV=1 docker run --rm --network klaro-obs-test -v "$(pwd -W):/src" -w //src \
  -e TEST_DATABASE_ADMIN_URL="postgres://klaro:klaro@klaro-obs-pg:5432/klaro_obs?sslmode=disable" \
  -e TEST_DATABASE_URL="postgres://klaro_obs_app:klaro_obs_app@klaro-obs-pg:5432/klaro_obs?sslmode=disable" \
  golang:1.25 go test -tags integration ./...
```

검증 항목: 앱 롤이 RLS를 우회하지 않음 · 크로스테넌트 읽기/쓰기/소유권 이전 차단 ·
org 스코프 테이블 전부 `FORCE` 확인 · `app.current_org`가 트랜잭션을 넘어 새지 않음 ·
키 발급/해석/로테이션 grace/즉시 폐기 · org별로 다른 VM AccountID 배정 ·
쿼터 2축 계산과 초과 시에도 수집 허용 · 감사 로그 기록.

### E2E (수집→저장→조회, 라이브 소켓)

`deploy/`의 스택이 떠 있어야 한다. 부품이 실제로 맞물리는지 확인하는 유일한 테스트다 —
게이트웨이 인증 → 올바른 저장소 테넌트 → WebSocket 복제 → Explorer 조회.

```bash
# 스택 기동 + org 시드 + 키 발급은 아래 "로컬 기동" 절 참고
MSYS_NO_PATHCONV=1 docker run --rm --network deploy_default -v "$(pwd -W):/src" -w //src \
  -e E2E_OBSPLANE_URL=http://obsplane:8090 \
  -e E2E_INTERNAL_URL=http://obsplane:8443 \
  -e E2E_OTLP_URL=http://otel-collector:4318 \
  -e E2E_VMSELECT_URL=http://vmselect:8481 \
  -e E2E_MAILHOG_URL=http://mailhog:8025 \
  -e E2E_REDIS_ADDR=redis:6379 \
  -e E2E_ROLLUPS=1 \
  -e E2E_ORG_ID=00000000-0000-0000-0000-000000000001 \
  -e E2E_DEV_TOKEN="$TOKEN" -e E2E_OBS_KEY="<발급한 secret>" \
  golang:1.25 go test -tags e2e -count=1 -timeout 10m -v -run TestE2E ./internal/api/
```

검증 항목: 라이브 소켓이 수집 직후 프레임을 받음(내부 라벨 미노출) · 메트릭/트레이스/로그가
Explorer로 되읽힘 · 생성된 쿼리에 org 매처가 붙어 있음 · 워터폴에 위치와 폭이 있음 ·
크로스테넌트 조회 403 · 예약 라벨 필터 422 · **메트릭이 VictoriaMetrics 기본 계정(0)으로
새지 않음** · 룰 생성→vmalert 발동→alert_event 기록(값 포함, 라우팅 라벨 미노출, 재전송
dedup으로 열린 이벤트 1건) · 알림이 MailHog 도착 · 롤업 시리즈 생성과 Explorer 폴백
(resolution이 raw가 아니고 org 매처 유지) · 보존 창 안에서는 raw 유지 · 게이트웨이 계량이
쿼터에 반영되고 klaro.usage.emitted가 발행됨 · 대시보드 생성→조회→목록→PATCH→삭제에서
패널 문서가 그대로 왕복하고 예약 라벨 패널은 422 · 스냅샷이 리포트 구간을 돌려주고
(org 매처 유지, 내부 라벨 미노출, 플랜 내 창은 partial 아님) 잘못된 요청은 백엔드를
건드리기 전에 422. 총 16건.

마지막 항목은 제품 표면에서는 보이지 않는 불변식이라 vmselect를 직접 본다(Explorer는
호출자 자신의 테넌트만 조회할 수 있고, 그게 바로 이 유출을 아무도 눈치채지 못하는
이유다). 실제로 하나 잡았다 — 아래 "메트릭 테넌시" 절.

## 로컬 기동 (전체 수집 스택)

```bash
cd deploy && docker compose up --build -d
```

postgres · redis · obsplane · klaro-otelcol · VictoriaMetrics 클러스터(vmstorage/vminsert/vmselect) ·
Tempo · Loki가 뜬다. 전부 OSS라 비용은 ₩0이다([COST-05]).

수집을 한 번 통과시켜 보는 절차:

```bash
ORG=00000000-0000-0000-0000-000000000001

# 개발 스텁 org는 시드되어 있지 않다.
docker compose exec -T postgres psql -U klaro -d klaro_obs \
  -c "INSERT INTO organizations (id,name,plan_code) VALUES ('$ORG','dev','pro') ON CONFLICT (id) DO UPDATE SET plan_code='pro'"

# 키 발급 — secret은 이 응답에만 있다.
curl -s -X POST localhost:8090/orgs/$ORG/obs/keys -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"smoke"}'

# SDK 대신 OTLP를 직접 밀어넣는다.
curl -s -X POST localhost:4318/v1/metrics -H 'Content-Type: application/json' \
  -H "klaro-obs-key: <secret>" --data-binary @metrics.json

# Explorer로 되읽기 — org는 서버가 주입하므로 클라이언트가 넘길 것이 없다.
curl -s -H "Authorization: Bearer $TOKEN" \
  "localhost:8090/orgs/$ORG/obs/metrics/query?metric=klaro_smoke_total&step=60"
curl -s -H "Authorization: Bearer $TOKEN" \
  "localhost:8090/orgs/$ORG/obs/traces?service=checkout&min_duration_ms=1000"
curl -s -H "Authorization: Bearer $TOKEN" \
  "localhost:8090/orgs/$ORG/obs/logs?filter=service_name%3Dcheckout&limit=50"

# 저장소를 직접 확인하고 싶으면(테넌트 번호는 /obs/tenant 로 확인)
curl -s "localhost:8481/select/1/prometheus/api/v1/query?query=klaro_smoke_total"
curl -s -H "X-Scope-OrgID: $ORG" localhost:3200/api/traces/<trace-id>
```

라이브 소켓은 `ws://localhost:8090/orgs/$ORG/obs/live?stream=metric`이다. 브라우저는
핸드셰이크에 Authorization 헤더를 붙일 수 없으므로 `Sec-WebSocket-Protocol: klaro-bearer,<토큰>`으로
보내고(위 "로컬 풀스택 실행" 참고), curl/Go 클라이언트는 헤더를 그대로 쓴다. 그 밑의
Redis 채널은 `klaro:obs:live:<org>:metric`이다:
`docker compose exec redis redis-cli psubscribe 'klaro:obs:live:*'`.

알림은 vmalert UI(`localhost:8880`)에서 룰 상태를, MailHog UI(`localhost:8025`)에서
발송된 알림을 볼 수 있다. 대시보드와 스냅샷은:

```bash
# 대시보드 저장 → 목록
curl -s -X POST localhost:8090/orgs/$ORG/obs/dashboards \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"checkout","spec":{"panels":[]}}'
curl -s localhost:8090/orgs/$ORG/obs/dashboards -H "Authorization: Bearer $TOKEN"

# 리포트 스냅샷 (내부 플레인 — 개발 compose는 평문 8443)
FROM=$(date -u -d '-15 min' +%Y-%m-%dT%H:%M:%SZ)
TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl -s -X POST localhost:8443/internal/snapshot -H 'Content-Type: application/json' \
  -d "{\"org_id\":\"$ORG\",\"from\":\"$FROM\",\"to\":\"$TO\",
       \"metrics\":[{\"key\":\"latency\",\"metric\":\"klaro_smoke_total\",\"agg\":\"avg\"}]}"
```

## 로컬 풀스택 실행 (백엔드 + 대시보드)

브라우저에서 실제 데이터가 보이는 상태까지 한 번에 가는 경로다. 위 "로컬 기동"이
백엔드만 띄우는 절차라면, 이쪽은 `apps/observability-dashboard`까지 연결한다.

```bash
# 전부 한 번에: compose 기동 → 시드 → 합성 메트릭 주입 → REST/WS 확인 → next build
./scripts/e2e-fullstack.sh

# 그 다음 대시보드 개발 서버
cd apps/observability-dashboard && npm run dev     # http://localhost:3100/live
```

수동으로 같은 일을 하려면 네 단계다.

```bash
# 1) 스택 기동 — obsplane 포함 11개 서비스
cd services/observability/deploy && docker compose up --build -d

# 2) org 시드 + JWT 발급 + 수집 키 발급 + 대시보드 .env.local 작성
./scripts/seed-dev.sh
#   stdout: KLARO_ORG_ID / KLARO_OBS_TOKEN / KLARO_OBS_KEY
#   파일:   apps/observability-dashboard/.env.local  (gitignore 대상)

# 3) 합성 메트릭 1건 주입 + 라이브 WS·Explorer 확인 (위 3개 값을 환경변수로)
eval "$(./scripts/seed-dev.sh)" && node ../../../scripts/verify-live.mjs

# 4) 대시보드
cd ../../../apps/observability-dashboard && npm run dev
```

| 주소 | 무엇 |
|------|------|
| http://localhost:3100 | 대시보드 (Next dev) |
| http://localhost:8090 | obsplane 공개 API (REST + 라이브 WS) |
| http://localhost:8443 | obsplane 내부 평면 (개발 평문) |
| http://localhost:4317 · :4318 | Collector OTLP (gRPC · HTTP) |
| http://localhost:8481 | vmselect (저장소 직접 확인용) |
| http://localhost:3101 | Loki — **호스트 포트 3101**. 3100은 대시보드가 쓴다 |
| http://localhost:8880 | vmalert UI |
| http://localhost:8025 | MailHog UI (발송된 알림) |

### 이 경로에서 실제로 막혀 있던 세 가지

프런트와 백엔드가 각자 정상인데도 브라우저에서는 아무것도 보이지 않는 상태였다.
원인은 서로 다른 세 개이고, 브라우저에서는 셋 다 "백엔드 없음"처럼 보였다.

1. **WebSocket 자격증명.** 브라우저 WebSocket API는 핸드셰이크에 헤더를 붙일 수 없다.
   그래서 라이브 소켓은 `Sec-WebSocket-Protocol: klaro-bearer, <토큰>`도 읽는다
   (`tenancy.SubprotocolToken`). 쿼리 파라미터가 아닌 이유는 하나다 — URL에 실린
   토큰은 경로상 모든 프록시의 액세스 로그와 브라우저 히스토리, Referer에 남고,
   여기서 토큰 하나는 org 하나다. 이 확장은 **라우트 단위 opt-in**이라
   (`tenancy.AllowWSSubprotocolCredential`, `getLive`에서만 호출) 나머지 REST
   라우트는 여전히 Authorization 헤더만 읽는다. 헤더가 있으면 헤더가 이긴다.
   서버는 선택한 서브프로토콜을 **응답에 에코**한다(RFC 6455 §4.2.2). 에코가 없으면
   브라우저는 인증에 성공한 연결을 스스로 끊는다 — 서버 장애처럼 보이는 실패다.
   토큰은 되돌려주지 않는다; 에코하는 값은 `klaro-bearer` 하나다.
2. **교차 출처.** 대시보드(:3100)와 obsplane(:8090)은 다른 오리진이라 모든 fetch가
   핸들러에 닿기도 전에 브라우저에서 막혔다. `OBS_DEV_CORS_ORIGINS`로 켜는
   `api.DevCORS`가 이를 푼다 — 명시적 허용 목록만, 와일드카드 없음,
   `Allow-Credentials`는 **보내지 않는다**(자격증명이 페이지가 직접 붙이는 Bearer
   토큰이라 브라우저가 자동으로 실어 보낼 것이 없다 — 에코한 오리진 옆에서 이걸 켜는
   것이 관대한 CORS를 위험하게 만드는 조합이다). 프리플라이트는 인증 **앞에서** 답한다 —
   규격상 자격증명이 없는 요청이라 tenancy 미들웨어까지 보내면 모든 교차출처 호출이
   401이 되고, 브라우저 콘솔에는 CORS 오류로만 보인다.
   프로덕션 프로파일은 이 변수를 **거부**한다(`config.validateDevCORS`).
3. **토큰이 없었다.** 대시보드가 보낼 유효한 JWT를 만드는 방법이 문서에 없었다.
   `deploy/scripts/dev-token.mjs`가 HS256 개발 시크릿으로 `org_id`·`role`·
   `exp`(`iss`/`aud` 포함) 토큰을 찍는다. 의존성 없이 node `crypto`만 쓴다.

**개발 스텁(`OBS_DEV_AUTH`)을 쓰지 않는 이유**: 스텁은 검증기를 문자열 비교로 갈아
끼우므로, 실제로 배포되는 JWT 경로가 로컬에서 한 번도 실행되지 않는다. compose는
그래서 스텁 대신 `OBS_JWT_HS_SECRET`을 넣고, 개발도 프로덕션과 **같은 코드 경로**로
인증한다. 스텁은 코드에 남아 있고 여전히 `OBS_DEV_AUTH`로만 켜진다.

### 검증된 것 (실측)

`./scripts/e2e-fullstack.sh`가 매번 다시 확인하는 항목이다.

| 단계 | 확인 내용 |
|------|-----------|
| compose up | obsplane 포함 11개 서비스 기동 |
| `/readyz` | 부팅 마이그레이션 후 Postgres 도달 가능 |
| seed | org 행 + HS256 JWT + 수집 키, `.env.local` 작성 |
| CORS | `OPTIONS` 프리플라이트가 자격증명 없이 204 + 오리진·Authorization 허용 |
| 라이브 WS | 서브프로토콜 인증으로 핸드셰이크 성립 + `klaro-bearer` 에코 |
| 수집 | Collector 경유 OTLP 1건(위조 `klaro.org_id` 포함) |
| 라이브 프레임 | 열려 있던 소켓으로 도착, 내부 라우팅 라벨 미노출 |
| Explorer | `/obs/metrics/query`가 샘플 반환 + 생성 쿼리에 org 매처 |
| 프런트 | `next build` 통과 |

## Collector 배포판을 왜 직접 만드나

klaro 컨트롤 플레인에 OTLP를 인증시키는 컴포넌트가 upstream에 없다. 그 하나(`klaroauth`) 때문에
커스텀 배포판을 만들고, 나머지는 전부 stock OSS다. 매니페스트는 `collector/builder-config.yaml`.

- `klaroauth` (extension) — `klaro-obs-key` → CP 검증, 결과 캐시(성공 TTL·실패 TTL 분리),
  해석된 테넌트를 요청 컨텍스트에 심는다. **시크릿은 컨텍스트에서 제거**하므로 익스포터가
  저장소로 흘려보낼 수 없다. 캐시 키도 시크릿이 아니라 그 다이제스트다.
  컨트롤 플레인 장애(5xx)는 "키가 나쁘다"로 캐시하지 않는다 — 장애 중 정상 텔레메트리를
  버리면 안 되기 때문이다.
- `klarousage` (processor) — org별로 신호별 바이트·건수와 host_ident를 세어 주기적으로
  CP `/internal/usage`에 보고한다. 데이터를 건드리지 않고 세기만 한다. 실패한 보고는
  재시도하지 않는다 — 다시 큐에 넣으면 고객 사용량을 두 번 셀 위험이 있고, 한 번의
  과소 보고는 복구 가능하지만 과다 보고는 환불이다.
- `klarotenant` (processor) — 그 테넌트를 리소스 속성으로 각인하고, 클라이언트가 보낸
  테넌트 속성은 덮어쓴다. 테넌트를 못 찾으면 배치를 **버린다**: 라벨 없는 메트릭은
  VictoriaMetrics 기본 계정(전 org 공용 버킷)으로 들어가므로, 유실보다 나쁜 결과가 된다.
  `batch`보다 **반드시 앞**에 있어야 한다(batch 이후에는 요청 컨텍스트가 사라진다).

## 메트릭 테넌시: 헤더가 아니라 라벨 (실측으로 얻은 결론)

VictoriaMetrics 클러스터는 멀티테넌트 쓰기에서 테넌트를 **AccountID 헤더**로도,
**`vm_account_id` 라벨**로도 받는다. 처음에는 헤더 방식을 썼는데, 그건 조용히 깨졌다.

`prometheusremotewrite` 익스포터는 export를 다른 고루틴에서 수행하고, 그 홉에서
요청의 client 메타데이터가 사라진다. `headers_setter`가 읽을 값이 없으니 AccountID
헤더가 빠지고, vminsert는 **기본 계정 0 — 전 org 공용 버킷**에 쓴다. 에러 로그도
없고, 익스포터는 `204`를 받고 성공을 보고한다. 실측 결과는 정확히 이랬다:
`target_info`만 org 테넌트로 가고 실제 시리즈는 전부 계정 0.

그래서 메트릭의 테넌트는 **데이터에 실어 보낸다**. `klarotenant`가 리소스 속성으로
`vm_account_id`를 각인하고(클라이언트가 보낸 값은 덮어씀),
`resource_to_telemetry_conversion`이 라벨로 바꾸고, vminsert가 라우팅에 쓴 뒤 시리즈에서
제거한다. 라벨은 비동기 홉을 살아서 통과한다. 카디널리티 비용도 없다 — org당 값 하나다.

Tempo/Loki는 `otlp`/`otlphttp` 익스포터가 컨텍스트를 유지하므로 `X-Scope-OrgID` 헤더로
충분하다(E2E가 실제로 확인한다).

이 부류의 사고는 눈에 보이지 않으므로, E2E에 "계정 0으로 새지 않는다"를 못박아 두었다.

## 알려진 제약 (의도된 것)

- **인증은 서명된 Bearer JWT**(HS256/RS256)다. 개발도 같은 경로를 쓴다 —
  `deploy/scripts/dev-token.mjs`로 로컬 토큰을 발급한다. 고정 토큰 스텁은
  `OBS_DEV_AUTH` 뒤에만 남아 있고 프로덕션 프로파일에서는 금지된다.
  OAuth2 발급자 연동과 `Org > Project > Resource` 3단 인가는 후속이다.
- **CORS는 개발 프로파일 전용**이다. 프로덕션 대시보드는 동일 오리진으로 서빙하거나
  게이트웨이가 자체 정책을 갖는다 — obsplane에 두 번째 오리진 정책을 두지 않는다.
- **개발 compose의 내부 홉은 평문**이다. obsplane은 `OBS_INTERNAL_INSECURE`, 게이트웨이는
  `klaroauth.tls.insecure`로 **명시적으로** 그렇게 요청해야 한다. 프로덕션 mTLS(SDK↔Collector
  포함)는 `deploy/otel-collector.prod.yaml` 오버레이가 켠다 — 위 "mTLS를 어디서 강제하나"
  참고. compose는 그 오버레이를 싣지 않으므로 개발은 평문 그대로다.
- **저장소 계층(vminsert/tempo/loki) 홉의 TLS는 아직 오버레이 밖**이다. `CLAUDE.md`가 필수로
  못박은 SDK↔Collector 경로는 강제되지만, 클러스터 내부 저장소 홉 전환은 별도 인프라 작업이다.
- **`stream=service`는 발행자가 없다.** 채널과 구독 경로는 있지만 아직 아무도 쓰지 않는다.
- **Explorer는 구조화 파라미터만** 받는다. raw 쿼리 개방은 label-enforcement 프록시를
  앞단에 두는 별도 결정이다(설계 HOW-2 트레이드오프).
- **알림은 메트릭 전용**이다. vmalert가 메트릭만 평가한다. 로그/트레이스 알림은 소형
  자체 폴러를 붙이는 별도 결정이다(HOW-1).
- **보존의 물리 삭제에는 빈틈이 있다**: VM은 시간범위 삭제가 없어 완전 만료 org만
  지우고, Tempo는 per-tenant delete API가 없다. 계약을 지키는 층은 explorer 클램프다.
- **다운샘플링은 메트릭 전용**이다(트레이스/로그에는 표준 개념이 없다).
- **대시보드는 저장·검증만 한다**. 렌더링은 프런트가 Explorer API로 하며, 이 저장소에
  차트 코드는 없다(HOW-3: Grafana 임베드가 아니다).
