# klaro 상시 관측 플랫폼 (S3) — Go 파운데이션

**최종 목표: Datadog 유사 상시 관측 제품.** 배포 적합성 리포트(S4)는 이 플랫폼의 데이터를
"특정 구간 스냅샷"으로 소비하는 하나의 소비자일 뿐이다.

설계 근거: `_workspace/05_architect_observability-design.md` ·
`docs/klaro/01-technical-design.md §2.4` · `docs/klaro/02-data-model.md §2.5`.

## 이 저장소 조각의 범위

설계 §6 빌드순서 **1~2단계만** 구현되어 있다.

| 단계 | 내용 | 상태 |
|------|------|------|
| 1 | platform(config·db·httpx·mtls·redisx·audit) + 마이그레이션 파이프라인 | ✅ |
| 2 | tenancy(org 컨텍스트 + `SET LOCAL app.current_org`) + tenants(백엔드 테넌트 매핑) | ✅ |
| 3 | ingestkey (OBS-02 키 발급/폐기/로테이션, `/internal/authz/ingest-key`) | 미착수 |
| 4 | OTel Collector 배포 + authz 확장 + 라우팅 exporter | 미착수 |
| 5~12 | live/WS · explorer · alerting · retentionjob · usage · dashboards · snapshot · deploy | 미착수 |

`services/load-test/`의 project 단위 APM 경로(`apm_agents`, `apm_spans`, `apm_logs`)는
**리포트 스냅샷 경로로 존치**한다. 대체가 아니라 공존이다(설계 §0·§4.6).

## 구조

```
cmd/obsplane/            상시 관측 Control Plane 엔트리(health/readyz + 미들웨어 체인)
internal/api/            Gin 라우터 · org 스코프 그룹
internal/platform/
  config/                환경변수 로딩 + 검증
  db/                    pgx 풀 · WithOrg(RLS 세션) · 마이그레이션 러너
  httpx/                 에러 규약 {code,message,details} (S1과 동일 봉투)
  mtls/                  내부 평면 mTLS(클라이언트 인증서 필수)
  redisx/                Signaler — org+stream 라이브 pub/sub (S1 패턴 재사용)
  audit/                 audit_logs 기록기(obs.key.* / obs.rule.*)
internal/tenancy/        org 해석 미들웨어 + 크로스테넌트 경로 차단
internal/tenants/        org_id → VM AccountID / X-Scope-OrgID 매핑
migrations/              0000 선행 의존성 + 0001~0007 (설계 §3 그대로)
deploy/                  postgres · redis · obsplane 최소 compose
```

## 멀티테넌시 (가장 중요)

격리는 **두 층**이고 둘 다 성립해야 한다.

1. `tenancy.Middleware` — 인증된 org와 경로의 `:orgId`가 다르면 `403 FORBIDDEN`. SQL 이전에 차단.
2. **Postgres RLS** — 7개 org 스코프 테이블 전부 `ENABLE` + **`FORCE`** ROW LEVEL SECURITY.

**앱은 반드시 `klaro_obs_app`(NOSUPERUSER·NOBYPASSRLS) 롤로 접속해야 한다.**
superuser/BYPASSRLS 롤로 붙으면 Postgres가 FORCE RLS를 면제하므로 SQL은 그대로인데
격리만 조용히 사라진다. `db.AssertRLSEnforced`가 부팅 시 이를 확인하고, 위반이면 기동을 거부한다.

DDL과 `CREATE ROLE`은 앱 롤 권한 밖이므로 **마이그레이션은 별도 관리자 DSN**
(`OBS_MIGRATE_DATABASE_URL`)으로 실행한다.

## 환경변수

| 변수 | 기본값 | 비고 |
|------|--------|------|
| `OBS_ADDR` | `:8090` | 공개 HTTP |
| `OBS_DATABASE_URL` | `postgres://klaro_obs_app:…/klaro_obs` | **non-superuser 롤 필수** |
| `OBS_MIGRATE_DATABASE_URL` | `postgres://klaro:…/klaro_obs` | 관리자 DSN(DDL) |
| `OBS_REDIS_ADDR` | `localhost:6379` | 라이브 pub/sub |
| `OBS_AUTO_MIGRATE` | `true` | 부팅 시 마이그레이션 |
| `OBS_DB_MAX_CONNS` | `10` | |
| `OBS_ACTIVE_HOST_WINDOW_SEC` | `900` | 활성 호스트 카운트 창 |
| `OBS_INTERNAL_ADDR` | `:8443` | 내부 mTLS 리스너(후속 단계에서 사용) |
| `OBS_TLS_CA_FILE` / `OBS_TLS_CERT_FILE` / `OBS_TLS_KEY_FILE` | — | 셋을 함께 설정해야 한다(부분 설정은 기동 실패) |
| `OBS_DEV_TOKEN` / `OBS_DEV_ORG_ID` | — | 개발 인증 스텁(S1과 동일 형태) |

## 빌드 · 테스트

로컬에 Go 툴체인이 없어도 된다. `docker` 이미지로 실행한다(`$PWD`는 이 디렉토리).

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.25 go build ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.25 go test ./...
```

Windows Git Bash에서는 경로 변환을 끄고 절대경로를 넘긴다.

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W):/src" -w //src golang:1.25 go test ./...
```

### 통합 테스트 (RLS 크로스테넌트 차단)

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
7개 테이블 전부 `FORCE` 확인 · `app.current_org`가 트랜잭션을 넘어 새지 않음 ·
스코프 없는 질의는 성공이 아니라 실패.

## 로컬 기동

```bash
cd deploy && docker compose up --build -d
curl -s localhost:8090/healthz   # {"status":"ok"}
curl -s localhost:8090/readyz    # {"status":"ready"}
curl -s -H 'Authorization: Bearer dev' \
  localhost:8090/orgs/00000000-0000-0000-0000-000000000001/obs/tenant
```

## 알려진 제약 (의도된 것)

- **인증은 개발 스텁**(고정 토큰 → 고정 org). JWT/OAuth/RBAC(`Org > Project > Resource`)는 후속.
- **`tenants.MemoryMapper`는 비영속**이다. VM `AccountID`를 순차 할당하고 프로세스 메모리에만 둔다.
  해시 방식을 쓰지 않은 이유는 32비트 충돌 시 두 org의 메트릭이 VM 안에서 **합쳐지기** 때문이다
  (Postgres 밖에서 일어나므로 RLS가 잡지 못한다). 재시작하면 번호가 바뀌므로,
  **Collector 라우팅(4단계) 착수 전에 영속 할당 테이블로 교체해야 한다.**
- `internal/mtls`는 구성만 제공한다. 내부 리스너 자체는 3~4단계에서 붙인다.
- 시계열 원본은 아직 어디에도 쓰지 않는다. 이 단계의 Postgres 테이블은 전부 참조·메타다.
