# klaro Phase 1 통합 QA 리포트 — 인증 + RLS 멀티테넌시 런타임 검증

**검증자** qa-verifier · **검증일** 2026-07-20 · **대상** `services/load-test`
**입력** `_workspace/03_backend-builder_manifest.md`, `_workspace/02_architect_design.md`, `_workspace/01_spec-analyst_contract.md`
**환경** Docker Desktop 4.80.0 (engine linux/amd64), postgres:16, redis:7, Go (호스트)

---

## 종합 판정: **PASS**

중대 결함 0. RLS가 **DB 레벨 · 통합테스트 · 라이브 HTTP** 세 층위 모두에서 실제로 테넌트를 격리함을 실증했다. 위양성(슈퍼유저 우회) 위험은 `klaro_app`의 `rolbypassrls=false, rolsuper=false`를 psql로 직접 확인해 배제했다. 부수적으로 코드 결함이 아닌 **환경/테스트 하네스 이슈 2건**을 기록한다.

| # | 항목 | 판정 |
|---|------|------|
| 1 | docker 기동 · 0001~0008 마이그레이션 · healthz | PASS (운영 함정 1건 기록) |
| 2 | DB 레벨 RLS 사실(롤/정책/세션 미설정 0건) | PASS |
| 3 | `go test -tags integration ./...` (klaro_app 비-슈퍼유저) | PASS (queue 위음성 1건 기록) |
| 4 | API 경계면 shape 정합성 | PASS |
| 5 | 크로스테넌트 IDOR 404 실증 | PASS |
| 6 | 기존 S1 부하 흐름 회귀 스모크 | PASS |

> docker는 **가용**했다. 전 항목을 실제 실행으로 검증했다(정적 대조 대체 불필요).

---

## 1. 환경 기동 · 마이그레이션 · healthz — PASS

```bash
cd services/load-test
docker compose up --build -d     # postgres/redis/api/worker 4개 기동
curl -s localhost:8080/healthz   # {"status":"ok"}
docker compose ps                # api/postgres(healthy)/redis/worker 전부 Up
```

- healthz → `{"status":"ok"}`.
- 4개 컨테이너 정상 기동(api, postgres[healthy], redis, worker).

### [운영 함정 · MEDIUM] 스테일 익명 볼륨 → 마이그레이션 미적용
첫 `up` 직후 org 스코프 테이블만 5개, `klaro_app`/`klaro_system` 롤이 **없었다**.

```bash
docker compose exec -T postgres psql -U klaro -d klaro -c \
  "SELECT rolname,rolbypassrls FROM pg_roles WHERE rolname LIKE 'klaro%'"
#  rolname | rolbypassrls
# ---------+--------------
#  klaro   | t              ← klaro_app/klaro_system 부재
```

- 원인: postgres 이미지의 익명 볼륨(`VOLUME /var/lib/postgresql/data`)이 이전(0004~0008 추가 이전) 실행분으로 남아 있어 `docker-entrypoint-initdb.d`가 **재실행되지 않음**. `docker-compose.yml`에 명명 볼륨이 없어 `docker compose down` 만으로는 초기화되지 않는다(initdb는 데이터 디렉토리가 비어야만 실행).
- 해결·재검증: `docker compose down -v` 후 재기동 → 0001~0008 사전순 전부 적용.

```bash
docker compose down -v && docker compose up -d
docker compose logs postgres | grep -E "0004|0005|0006|0007|0008"
# running /docker-entrypoint-initdb.d/0004_auth.sql
# running /docker-entrypoint-initdb.d/0005_org_id_backfill.sql
# running /docker-entrypoint-initdb.d/0006_rls.sql
# running /docker-entrypoint-initdb.d/0007_roles.sql
# running /docker-entrypoint-initdb.d/0008_dev_seed.sql
docker compose exec -T postgres psql -U klaro -d klaro -tc \
  "SELECT count(*) FROM pg_tables WHERE schemaname='public'"   # 15
```

(로그의 `FATAL: database "klaro" does not exist` / `system is shutting down`은 initdb 부트스트랩 단계의 정상 과도 메시지다.)

- **권고(코드 결함 아님, 배포 함정)**: 매니페스트 §4/README에 "마이그레이션 갱신 시 `down -v` 필요" 명시 또는 명시적 마이그레이션 러너(golang-migrate 등) 도입. CI/신규 개발자에게 위양성 초록/빨강을 유발할 수 있어 기록.

---

## 2. DB 레벨 RLS 사실 확인 — PASS

### 2-1. 롤 권한
```bash
docker compose exec -T postgres psql -U klaro -d klaro -c \
  "SELECT rolname,rolbypassrls,rolsuper FROM pg_roles WHERE rolname LIKE 'klaro%' ORDER BY rolname"
#    rolname    | rolbypassrls | rolsuper
# --------------+--------------+----------
#  klaro        | t            | t
#  klaro_app    | f            | f      ← 앱 접속 롤: RLS 강제
#  klaro_system | t            | f      ← 부트스트랩 전용: BYPASSRLS
```
`klaro_app` = `rolbypassrls=false`, `klaro_system` = `rolbypassrls=true`. 매니페스트 §6 부합.

### 2-2. RLS enabled + forced + org_isolation 정책
```bash
docker compose exec -T postgres psql -U klaro -d klaro -c \
 "SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
    (SELECT count(*) FROM pg_policies p WHERE p.tablename=c.relname AND p.policyname='org_isolation')
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public' AND c.relkind='r' ORDER BY c.relname"
```
결과: `api_keys, apm_agents, apm_logs, apm_spans, load_test_results, load_tests, memberships, organizations, projects, report_shares, reports, scan_findings, scans, verified_domains` = 14개 테이블 모두 `relrowsecurity=t`, `relforcerowsecurity=t`, `org_isolation` 정책 1개씩. `users`는 전역 신원 테이블이라 RLS 없음(`f/f/0`) — 설계상 정확.

### 2-3. klaro_app + app.current_org 미설정 → org 스코프 SELECT 0건 (TENANT-02)
데이터가 존재함에도(dev 시드) 세션 미설정 상태에서 0건 = 정보 누출 없음.
```bash
# 데이터 존재(슈퍼유저 우회)
docker compose exec -T postgres psql -U klaro -d klaro -c \
  "SELECT (SELECT count(*) FROM organizations) orgs,(SELECT count(*) FROM projects) projects,
          (SELECT count(*) FROM memberships) memberships"
#  orgs | projects | memberships
#     1 |        1 |           1

# klaro_app, app.current_org UNSET → 전부 0
docker compose exec -T -e PGPASSWORD=klaro_app postgres psql -U klaro_app -d klaro -c \
  "SELECT count(*) FROM organizations; SELECT count(*) FROM projects;
   SELECT count(*) FROM memberships; SELECT count(*) FROM load_tests"
#  organizations=0, projects=0, memberships=0, load_tests=0
```

### 2-4. 양방향 스코핑
```bash
# 올바른 org 스코프 → 1건
docker compose exec -T -e PGPASSWORD=klaro_app postgres psql -U klaro_app -d klaro -c \
  "SELECT set_config('app.current_org','00000000-0000-0000-0000-000000000001',false);
   SELECT count(*) FROM organizations; SELECT count(*) FROM projects"   # 1, 1
# 잘못된 org 스코프 → 0건
docker compose exec -T -e PGPASSWORD=klaro_app postgres psql -U klaro_app -d klaro -c \
  "SELECT set_config('app.current_org','99999999-9999-9999-9999-999999999999',false);
   SELECT count(*) FROM organizations; SELECT count(*) FROM projects"   # 0, 0
```
근거 파일: `migrations/0006_rls.sql`(FORCE + `current_setting('app.current_org',true)`), `0007_roles.sql`(NOBYPASSRLS).

---

## 3. 통합 테스트 (`go test -tags integration ./...`) — PASS

### 3-1. 위양성 방지: TEST_DATABASE_URL이 klaro_app 비-슈퍼유저임을 확인
```bash
export TEST_DATABASE_URL="postgres://klaro_app:klaro_app@localhost:5432/klaro?sslmode=disable"
export TEST_SYSTEM_DATABASE_URL="postgres://klaro_system:klaro_system@localhost:5432/klaro?sslmode=disable"
export TEST_REDIS_ADDR="localhost:6379"

docker compose exec -T -e PGPASSWORD=klaro_app postgres psql -U klaro_app -d klaro -tc \
 "SELECT current_user,(SELECT rolbypassrls FROM pg_roles WHERE rolname=current_user),
         (SELECT rolsuper FROM pg_roles WHERE rolname=current_user)"
#  klaro_app | f | f      ← 비-슈퍼유저·NOBYPASSRLS 확정 → RLS 실효, 위양성 없음
```

### 3-2. 실행 결과 (워커 컨테이너 정지 상태, 클린)
```bash
go test -tags integration -count=1 ./...
# ok  internal/api      5.698s   ← TestE2EAuthFlow
# ok  internal/auth     2.373s
# ok  internal/breaker  0.362s
# ok  internal/domainverify 1.990s
# ok  internal/model    0.652s
# ok  internal/queue    1.437s
# ok  internal/rbac     0.437s
# ok  internal/scenario 0.762s
# ok  internal/store    1.720s   ← RLS 격리 3종
# ok  internal/worker   0.836s
```
- `internal/store`: `TestRLSSessionUnsetReturnsZero`(세션 미설정 0건), `TestRLSCrossOrgBlocked`(크로스 org ErrNotFound + 동일 org 정상), `TestLoadTestLifecycle`(RLS tx 내 상태전이 + 불법전이 거부) — PASS.
- `internal/api`: `TestE2EAuthFlow`(signup 201 / 중복 409 / login 200 / 오류 401 / 프로젝트 CRUD / 타 org 스코프 404 / 타 org 리소스 404 / 미인증 401) — PASS. 테스트 라우터는 `AppEnv:"test"`로 구성되어 dev-token 인증 우회가 비활성 → 인증 위양성 없음(양호).

### [테스트 하네스 이슈 · LOW] queue.TestEnqueueDequeue 위음성(flaky)
워커 컨테이너 가동 중 첫 실행 시 `redis: nil`로 FAIL.
```
--- FAIL: TestEnqueueDequeue (5.11s)
    queue_test.go:29: redis: nil
```
- 원인: 워커가 `klaro:jobs` 리스트에 대해 `BRPop`하는 **경쟁 소비자**여서, 테스트가 `LPush`한 잡을 워커가 먼저 가져감(코드 정상 동작). RLS/Phase 1 회귀 아님.
- 검증: 워커 정지 후 재실행 → PASS.
```bash
docker compose stop worker
go test -tags integration -count=1 -run TestEnqueueDequeue ./internal/queue/   # ok 1.074s
```
- 권고: `internal/queue/queue_test.go`를 전용 리스트 키/Redis DB index로 격리하거나, 매니페스트 §5에 "통합 테스트는 워커 컨테이너 정지 상태에서 실행" 명시.

---

## 4. API 경계면 shape 정합성 — PASS

`internal/api/{auth_handlers,org_handlers,project_handlers}.go`의 실제 반환 shape과 매니페스트 §2 엔드포인트 표를 코드 교차 확인.

| 엔드포인트 | 매니페스트 shape | 핸들러 반환 | 일치 |
|-----------|-----------------|------------|------|
| POST /v1/auth/signup | `{user_id,org_id}` / 중복 409 | `gin.H{"user_id","org_id"}`, 중복 선검사 409 | ✔ |
| POST /v1/auth/login | `{access_token,refresh_token,expires_in}` / 401 | `issueTokens()` 동일, 실패 401 은닉 | ✔ |
| POST /v1/auth/refresh | 회전 3필드 / 재사용 401 | `Rotate()`+새 3필드, 실패 401 | ✔ |
| POST /v1/orgs | `{id}` | `gin.H{"id"}` | ✔ |
| POST /v1/orgs/:orgId/api-keys | `{id,name,role,key,expires_at,created_at}` | 동일(`key` 원문 1회) | ✔ |
| GET /v1/orgs/:orgId/api-keys | 원문·해시 미노출 | `ListApiKeys` — key_hash/revoked_at json 제외 | ✔ |
| POST /v1/projects | `{id,name}` | `gin.H{"id","name"}` | ✔ |
| GET /v1/projects/:id | RLS 0건 404 | `ErrNotFound → 404` | ✔ |

권한 매트릭스(`router.go`) ↔ 매니페스트 §2 일치 확인:
- 프로젝트 GET=viewer, POST/PATCH=member, DELETE=admin.
- 멤버 GET=member, 멤버 POST/PATCH/DELETE=admin, API키(GET/POST/DELETE)=admin.
- 기존 리소스 GET=viewer, 변경(도메인 verify·부하 생성·스캔 트리거 등)=member.
- 4단 체인 `authenticate(401) → resolveOrg(404 은닉) → authorize(403) → tenancyTx` (`middleware.go`).

---

## 5. 크로스테넌트 IDOR 404 실증 (라이브 HTTP) — PASS

org A / org B 실계정 2개를 라이브 API에 signup→login 후 curl로 실증.

```bash
# signup A → {org_id, user_id}, 중복 → 409, login A → access_token(len 188)
# bad login → 401, create project A → {id,name} 201, get own → 200
```
| 시나리오 | 기대 | 실제 |
|---------|------|------|
| B가 A org 헤더로 프로젝트 목록 | 404(멤버십 은닉) | **404** |
| B가 자기 org에서 A의 project id 조회 | 404(RLS 0건) | **404** |
| B가 A org 헤더로 A의 project id 조회 | 404(멤버십 은닉) | **404** |
| 미인증 요청 | 401 | **401** |
| X-Org-Id 누락 | 400 | **400** |

RBAC 라이브 실증(B를 org A에 viewer로 추가 후):
| 시나리오 | 기대 | 실제 |
|---------|------|------|
| viewer B GET /v1/projects | 200 | **200** |
| viewer B POST /v1/projects | 403 | **403** |
| viewer B GET members | 403(member 필요) | **403** |
| viewer B GET api-keys | 403(admin 필요) | **403** |

```bash
curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/v1/projects/$PROJA \
  -H "Authorization: Bearer $TOKB" -H "X-Org-Id: $ORGB"   # 404
```

---

## 6. 기존 S1 부하 흐름 회귀 스모크 — PASS

```bash
# 6-1. 미검증 도메인 부하 생성 → 도메인 게이트 차단
curl -s -X POST localhost:8080/projects/$PROJA/load-tests \
  -H "Authorization: Bearer $TOKA" -H "X-Org-Id: $ORGA" \
  -d '{"target_url":"https://staging.example.com","scenario":{...}}' -w "\nHTTP %{http_code}"
# {"error":{"code":"DOMAIN_NOT_VERIFIED","message":"target domain must be verified"}}
# HTTP 403

# 6-2. 검증 도메인 시드(klaro_system) 후 재생성 → 상태머신 전이
#   INSERT verified_domains(...,status='verified')
# {"id":"...","status":"validating"}  HTTP 202
# poll: validating → running → completed
```
- 도메인 소유권 검증 게이트(DDoS 악용 차단 불변식) 관통 확인.
- 검증 도메인에서는 202 생성 후 워커가 `RunInOrg(org)` RLS 스코프로 write → 상태머신 `validating→running→completed` 정상 전이. RLS 관통 후에도 S1 부하 흐름·워커 경로 정상. 불법 전이 거부는 `TestLoadTestLifecycle`로 이미 검증.

---

## 후속 권고 (모두 non-blocking)

1. **[MEDIUM] 마이그레이션 재적용 함정**(항목 1): `docker compose down -v` 필요 명시 또는 명시적 마이그레이션 러너 도입. 대상: `services/load-test/docker-compose.yml`, `README.md`, `_workspace/03_backend-builder_manifest.md §4`.
2. **[LOW] 통합 테스트 워커 경쟁**(항목 3): `internal/queue/queue_test.go` 격리(전용 키/Redis DB) 또는 실행 절차에 "워커 정지" 명시. 대상: 매니페스트 §5.
3. 매니페스트 §7 기재 항목(경로 prefix `/v1` 불일치, OAuth 실 provider 미검증)은 이번 RLS 검증 범위와 무관하며 Phase 2 처리 타당.

## 미해결 결함
없음. Phase 1(인증 + RLS 멀티테넌시)은 런타임 격리 증명을 포함해 통합 정합성 검증을 통과했다.

## 검증 종료 처리
`docker compose down -v`로 환경을 클린 정리했다(재현 절차는 위 명령으로 재기동 가능).
