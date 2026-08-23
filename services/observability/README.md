# klaro 상시 관측 플랫폼 (S3) — Go 파운데이션 + 수집 경로

**최종 목표: Datadog 유사 상시 관측 제품.** 배포 적합성 리포트(S4)는 이 플랫폼의 데이터를
"특정 구간 스냅샷"으로 소비하는 하나의 소비자일 뿐이다.

설계 근거: `_workspace/05_architect_observability-design.md` ·
`docs/klaro/01-technical-design.md §2.4` · `docs/klaro/02-data-model.md §2.5`.

## 이 저장소 조각의 범위

설계 §6 빌드순서 **1~4단계**가 구현되어 있다.

| 단계 | 내용 | 상태 |
|------|------|------|
| 1 | platform(config·db·httpx·mtls·redisx·audit) + 마이그레이션 파이프라인 | ✅ |
| 2 | tenancy(org 컨텍스트 + `SET LOCAL app.current_org`) + tenants(**영속** 백엔드 테넌트 배정) | ✅ |
| 3 | ingestkey (OBS-02 키 발급/목록/로테이션/폐기 · 쿼터 · `/internal/authz/ingest-key`) | ✅ |
| 4 | OTel Collector 게이트웨이(authz 확장 + 테넌트 라우팅 + 라이브 복제) | ✅ |
| 5 | live WS fan-out (OBS-01/APM-02) — CP→Redis publish까지는 동작, WS 허브가 남음 | 부분 |
| 6~12 | explorer · alerting · retentionjob · usage · dashboards · snapshot | 미착수 |

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
```

테넌트는 **SDK가 보낸 값이 아니라 컨트롤 플레인이 발급한 값**이다. 이게 이 구조의 핵심이다:
`klarotenant`가 클라이언트가 붙인 `vm_account_id` / `klaro.org_id`를 무조건 덮어쓰므로
라벨 위조로 다른 org에 쓰는 경로가 없다.

## 구조

```
cmd/obsplane/            상시 관측 Control Plane 엔트리(공개 리스너 + 내부 mTLS 리스너)
internal/api/            Gin 라우터 · org 스코프 그룹 · 키/쿼터 핸들러 · 내부 플레인
internal/ingestkey/      OBS-02 관측 키: 발급·로테이션(grace)·폐기·해석·쿼터 스냅샷
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

# OTLP 전송 후 org 테넌트에서 조회 (vm_account_id는 /obs/tenant 로 확인)
curl -s -X POST localhost:4318/v1/metrics -H 'Content-Type: application/json' \
  -H "klaro-obs-key: <secret>" --data-binary @metrics.json
curl -s "localhost:8481/select/1/prometheus/api/v1/query?query=klaro_smoke_total"
curl -s -H "X-Scope-OrgID: $ORG" localhost:3200/api/traces/<trace-id>
curl -s -H "X-Scope-OrgID: $ORG" --get localhost:3100/loki/api/v1/query_range \
  --data-urlencode '{service_name="checkout"}'
```

라이브 복제는 `klaro:obs:live:<org>:metric` 채널로 나간다:
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

## 알려진 제약 (의도된 것)

- **인증은 개발 스텁**(고정 토큰 → 고정 org). JWT/OAuth/RBAC(`Org > Project > Resource`)는 후속.
- **개발 compose의 내부 홉은 평문**이다. obsplane은 `OBS_INTERNAL_INSECURE`, 게이트웨이는
  `klaroauth.tls.insecure`로 **명시적으로** 그렇게 요청해야 하고, 두 곳 다 프로덕션 mTLS 블록이
  주석으로 준비되어 있다. SDK↔Collector mTLS도 같은 자리에 있다.
- **라이브 경로는 절반**이다. Collector→CP→Redis publish는 동작하고, WS fan-out(설계 §6 5단계)이 남았다.
- **Explorer가 없다.** 데이터는 VM/Tempo/Loki에 들어가지만 조회 API(OBS-03/04/05)는 6단계다.
- 리텐션은 각 백엔드의 전역 설정(최대 플랜 기준)만 걸려 있다. 플랜별 집행은 8단계 `retentionjob`.
