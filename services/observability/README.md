# klaro 상시 관측 플랫폼 (S3) — 수집·라이브·조회 경로

**최종 목표: Datadog 유사 상시 관측 제품.** 배포 적합성 리포트(S4)는 이 플랫폼의 데이터를
"특정 구간 스냅샷"으로 소비하는 하나의 소비자일 뿐이다.

설계 근거: `_workspace/05_architect_observability-design.md` ·
`docs/klaro/01-technical-design.md §2.4` · `docs/klaro/02-data-model.md §2.5`.

## 이 저장소 조각의 범위

설계 §6 빌드순서 **1~6단계**가 구현되어 있다.

| 단계 | 내용 | 상태 |
|------|------|------|
| 1 | platform(config·db·httpx·mtls·redisx·audit) + 마이그레이션 파이프라인 | ✅ |
| 2 | tenancy(org 컨텍스트 + `SET LOCAL app.current_org`) + tenants(**영속** 백엔드 테넌트 배정) | ✅ |
| 3 | ingestkey (OBS-02 키 발급/목록/로테이션/폐기 · 쿼터 · `/internal/authz/ingest-key`) | ✅ |
| 4 | OTel Collector 게이트웨이(authz 확장 + 테넌트 라우팅 + 라이브 복제) | ✅ |
| 5 | live + WS fan-out(org+stream 허브, ≤2초) (OBS-01/APM-02) | ✅ |
| 6 | explorer(metrics/traces/logs 얇은 프록시, 서버사이드 org 강제) (OBS-03/04/05) | ✅ |
| 7~12 | alerting · retentionjob · usage · dashboards · snapshot | 미착수 |

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
internal/explorer/       metrics/traces/logs 조회 프록시 — 쿼리 조립·org 강제(OBS-03/04/05)
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
2. **Postgres RLS** — org 스코프 테이블 전부 `ENABLE` + **`FORCE`** ROW LEVEL SECURITY.
3. **저장소 native 테넌시** — VictoriaMetrics `AccountID`, Tempo/Loki `X-Scope-OrgID`(HOW-8).
   이 값은 CP만 정한다. 라벨 매처를 빠뜨려서 생기는 사고를 구조적으로 없앤다.

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
| GET | `/orgs/:orgId/obs/logs` | `filter` · `query`(리터럴 부분문자열) · `limit` · `from`/`to` |

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
| `OBS_INTERNAL_INSECURE` | `false` | 내부 평면 평문. **개발 전용**, 명시적으로 켜야 한다 |
| `OBS_VMSELECT_URL` | — | Explorer 메트릭 백엔드(vmselect). 비면 "백엔드 없음"으로 502 |
| `OBS_TEMPO_URL` | — | Explorer 트레이스 백엔드 |
| `OBS_LOKI_URL` | — | Explorer 로그 백엔드 |
| `OBS_EXPLORER_TIMEOUT_SEC` | `30` | 백엔드 질의 1건 상한 |
| `OBS_EXPLORER_MAX_ROWS` | `1000` | 질의당 행 상한(보존창 전체를 메모리로 끌어오지 못하게) |
| `OBS_DEV_TOKEN` / `OBS_DEV_ORG_ID` | — | 개발 인증 스텁(S1과 동일 형태) |

TLS 번들도 없고 `OBS_INTERNAL_INSECURE`도 아니면 내부 리스너는 **뜨지 않는다**(로그로 알린다).
mTLS가 계약이므로, 번들이 없다고 평문으로 조용히 내려앉지 않는다.

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
  -e E2E_OTLP_URL=http://otel-collector:4318 \
  -e E2E_VMSELECT_URL=http://vmselect:8481 \
  -e E2E_ORG_ID=00000000-0000-0000-0000-000000000001 \
  -e E2E_DEV_TOKEN=dev -e E2E_OBS_KEY="<발급한 secret>" \
  golang:1.25 go test -tags e2e -count=1 -timeout 10m -v -run TestE2E ./internal/api/
```

검증 항목: 라이브 소켓이 수집 직후 프레임을 받음(내부 라벨 미노출) · 메트릭/트레이스/로그가
Explorer로 되읽힘 · 생성된 쿼리에 org 매처가 붙어 있음 · 워터폴에 위치와 폭이 있음 ·
크로스테넌트 조회 403 · 예약 라벨 필터 422 · **메트릭이 VictoriaMetrics 기본 계정(0)으로
새지 않음**.

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
  -c "INSERT INTO organizations (id,name,plan_code) VALUES ('$ORG','dev','free') ON CONFLICT DO NOTHING"

# 키 발급 — secret은 이 응답에만 있다.
curl -s -X POST localhost:8090/orgs/$ORG/obs/keys -H 'Authorization: Bearer dev' \
  -H 'Content-Type: application/json' -d '{"name":"smoke"}'

# SDK 대신 OTLP를 직접 밀어넣는다.
curl -s -X POST localhost:4318/v1/metrics -H 'Content-Type: application/json' \
  -H "klaro-obs-key: <secret>" --data-binary @metrics.json

# Explorer로 되읽기 — org는 서버가 주입하므로 클라이언트가 넘길 것이 없다.
curl -s -H 'Authorization: Bearer dev' \
  "localhost:8090/orgs/$ORG/obs/metrics/query?metric=klaro_smoke_total&step=60"
curl -s -H 'Authorization: Bearer dev' \
  "localhost:8090/orgs/$ORG/obs/traces?service=checkout&min_duration_ms=1000"
curl -s -H 'Authorization: Bearer dev' \
  "localhost:8090/orgs/$ORG/obs/logs?filter=service_name%3Dcheckout&limit=50"

# 저장소를 직접 확인하고 싶으면(테넌트 번호는 /obs/tenant 로 확인)
curl -s "localhost:8481/select/1/prometheus/api/v1/query?query=klaro_smoke_total"
curl -s -H "X-Scope-OrgID: $ORG" localhost:3200/api/traces/<trace-id>
```

라이브 소켓은 `ws://localhost:8090/orgs/$ORG/obs/live?stream=metric`이고, 그 밑의
Redis 채널은 `klaro:obs:live:<org>:metric`이다:
`docker compose exec redis redis-cli psubscribe 'klaro:obs:live:*'`.

## Collector 배포판을 왜 직접 만드나

klaro 컨트롤 플레인에 OTLP를 인증시키는 컴포넌트가 upstream에 없다. 그 하나(`klaroauth`) 때문에
커스텀 배포판을 만들고, 나머지는 전부 stock OSS다. 매니페스트는 `collector/builder-config.yaml`.

- `klaroauth` (extension) — `klaro-obs-key` → CP 검증, 결과 캐시(성공 TTL·실패 TTL 분리),
  해석된 테넌트를 요청 컨텍스트에 심는다. **시크릿은 컨텍스트에서 제거**하므로 익스포터가
  저장소로 흘려보낼 수 없다. 캐시 키도 시크릿이 아니라 그 다이제스트다.
  컨트롤 플레인 장애(5xx)는 "키가 나쁘다"로 캐시하지 않는다 — 장애 중 정상 텔레메트리를
  버리면 안 되기 때문이다.
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

- **인증은 개발 스텁**(고정 토큰 → 고정 org). JWT/OAuth/RBAC(`Org > Project > Resource`)는 후속.
- **개발 compose의 내부 홉은 평문**이다. obsplane은 `OBS_INTERNAL_INSECURE`, 게이트웨이는
  `klaroauth.tls.insecure`로 **명시적으로** 그렇게 요청해야 하고, 두 곳 다 프로덕션 mTLS 블록이
  주석으로 준비되어 있다. SDK↔Collector mTLS도 같은 자리에 있다.
- **`stream=service`는 발행자가 없다.** 채널과 구독 경로는 있지만 아직 아무도 쓰지 않는다.
- **Explorer는 구조화 파라미터만** 받는다. raw 쿼리 개방은 label-enforcement 프록시를
  앞단에 두는 별도 결정이다(설계 HOW-2 트레이드오프).
- **알림·보존·과금 발행이 없다**(설계 §6 7~9단계). 데이터는 쌓이지만 룰 평가와 플랜별
  삭제, 미터 발행은 아직 없다.
- 리텐션은 각 백엔드의 전역 설정(최대 플랜 기준)만 걸려 있다. 플랜별 집행은 8단계 `retentionjob`.
