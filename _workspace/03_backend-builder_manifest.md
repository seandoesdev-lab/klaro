# klaro Phase 1 — backend-builder 구현 매니페스트

**작성** backend-builder · **작성일** 2026-07-20 · **대상 모듈** `services/load-test` (Go, module `github.com/klaro/load-test`)
**입력** `_workspace/01_spec-analyst_contract.md`, `_workspace/02_architect_design.md`
**상태** 8단계 빌드 순서 완료. `go build ./... && go vet ./...` 그린, 유닛 테스트 그린, DB 통합 테스트(Docker Postgres+Redis) 그린.

---

## 0. 요약

기존 `authStub`(고정 dev-token→고정 project) 미들웨어를 **4단 체인**(authenticate → resolveOrg → authorize → tenancyTx)으로 교체하고, PostgreSQL **RLS 멀티테넌시**를 실효화했다. 앱은 `klaro_app`(NOBYPASSRLS) 롤로 접속하며 요청당 트랜잭션 + `set_config('app.current_org', <uuid>, true)`로 org 스코프를 강제한다. 부트스트랩(인증 전 users/membership 조회, 조직 생성, 공유 리포트, APM ingest)만 `klaro_system`(BYPASSRLS) 풀로 처리한다. 인증은 JWT Access + Redis 회전 Refresh + API Key + dev-token(APP_ENV=dev 한정), 인가는 owner>admin>member>viewer 서열 판정이다.

---

## 1. 추가/수정 파일

### 신규 마이그레이션 (`services/load-test/migrations/`)
| 파일 | 내용 |
|------|------|
| `0004_auth.sql` | `citext` 확장, `users`/`memberships`/`api_keys` 신설 + 인덱스(부분 UNIQUE oauth, key_hash UNIQUE) |
| `0005_org_id_backfill.sql` | 10개 org 스코프 테이블에 `org_id` 추가(nullable→부모조인 백필→NOT NULL→FK→인덱스). `IF NOT EXISTS`로 재실행 안전 |
| `0006_rls.sql` | 14개 테이블 `ENABLE`+`FORCE RLS` + `org_isolation` 정책(`current_setting('app.current_org',true)`) |
| `0007_roles.sql` | `klaro_app`(NOBYPASSRLS)·`klaro_system`(BYPASSRLS) 롤 + GRANT + DEFAULT PRIVILEGES |
| `0008_dev_seed.sql` | dev user(`…0003`, pgcrypto bcrypt) + dev-org owner 멤버십 백필(`ON CONFLICT DO NOTHING`) |

### 신규 Go 패키지
| 파일 | 내용 |
|------|------|
| `internal/tenancy/context.go` | `Principal{UserID,OrgID,Role,AuthType}`, gin ctx 키, `Tx(c)`/`OrgID(c)`/`UserID(c)`/`Role(c)`/`ProjectID(c)` 접근자 |
| `internal/auth/password.go` | bcrypt(cost 12) 해시/검증 |
| `internal/auth/jwt.go` | HS256 Access 토큰(15분) 발급/검증 (golang-jwt/v5) |
| `internal/auth/refresh.go` | 불투명 랜덤 Refresh + Redis 저장(sha256만) + 회전 + 재사용 감지(family 폐기) + 로그아웃(D-2) |
| `internal/auth/apikey.go` | API Key 생성(원문 1회)/sha256 해시/상수시간 비교 |
| `internal/auth/oauth.go` | GitHub/Google OAuth2 + 크리덴셜 미설정 시 dev mock 콜백(D-3) |
| `internal/auth/auth_test.go` | password/jwt/apikey/oauth mock 유닛 테스트 |
| `internal/rbac/role.go` | Role 서열(owner>admin>member>viewer), `Rank`/`AtLeast`/`Valid` |
| `internal/rbac/invariant.go` | `ErrLastOwner`, `WouldRemoveLastOwner`/`WouldDemoteLastOwner`(D-6) |
| `internal/rbac/rbac_test.go` | 서열·AtLeast·last-owner 불변식 유닛 테스트 |
| `internal/store/auth_store.go` | users/memberships/api_keys/orgs/projects CRUD (sys/app 풀 분리) |
| `internal/model/auth.go` | `User`/`Membership`/`ApiKey`/`Project`/`OrgSummary`/`MemberSummary` |
| `internal/api/auth_handlers.go` | signup/login/refresh/logout/oauthStart/oauthCallback |
| `internal/api/org_handlers.go` | orgs·members·api-keys 핸들러 |
| `internal/api/project_handlers.go` | projects CRUD 핸들러 |
| `internal/api/e2e_test.go` | (integration) signup→login→org CRUD→크로스 org 404 e2e |

### 수정 파일
| 파일 | 변경 |
|------|------|
| `internal/store/store.go` | 2풀(app/sys) + `Querier` 인터페이스 + `New(ctx,appDSN,sysDSN)` + `BeginOrg`/`RunInOrg`/`Sys()`/`App()` + 신규 에러(ErrConflict/ErrRevoked/ErrExpired). load_tests/domains 메서드 `q Querier` 파라미터화 + INSERT `org_id` |
| `internal/store/apm_store.go` | `q Querier` 파라미터화, INSERT org_id, `TouchAgent`→`ResolveAgentOrg`(sys, ingest 부트스트랩) |
| `internal/store/scans_store.go` | `q Querier` 파라미터화 + INSERT org_id |
| `internal/store/reports_store.go` | `q Querier` 파라미터화 + INSERT org_id, `GetShareBySlug`/`GetReportSys`(공유=sys 풀) |
| `internal/store/store_test.go` | (integration) `New(appDSN,sysDSN)` + RLS 격리 테스트 3종 추가 |
| `internal/api/middleware.go` | `authStub` 제거 → 4단 체인 + `authenticateIngest`. `projectID(c)`=`c.Param("id")` |
| `internal/api/router.go` | `Deps`에 JWT/Refresh/OAuth/AppEnv 추가. 그룹 재구성(public/authOnly/orgScoped) + 라우트별 `authorize(min)` |
| `internal/api/{loadtests,domains,scans,apm,reports}.go` | `tenancy.Tx(c)` 주입, org 전파, ingest 리팩터 |
| `internal/model/types.go` | `LoadTest.OrgID`, `Job.OrgID` 추가 |
| `internal/model/scan.go` | `ScanJob.OrgID` 추가 |
| `internal/worker/worker.go`, `scanworker.go` | DB write를 `RunInOrg(job.OrgID, …)`로 래핑(RLS 관통) |
| `internal/queue/redis.go` | `Client()` 노출(Refresh store 재사용) |
| `cmd/api/main.go`, `cmd/worker/main.go` | app/sys 2 DSN 배선, auth 컴포넌트 주입 |
| `docker-compose.yml` | 전체 migrations 마운트, 2 DSN + JWT/OAuth/APP_ENV env |
| `go.mod`/`go.sum` | golang-jwt/v5, x/oauth2(+google/github) 추가(전부 OSS·무비용) |
| `docs/klaro/02-data-model.md` | M-1 해소: org_id 컬럼/users 부가컬럼/api_keys.role/RLS 운영규칙 반영 |
| `README.md` | 인증/RLS/DSN/통합테스트 절차 갱신 |

---

## 2. 신규 엔드포인트 (경계면 — qa/frontend 참조)

에러 포맷은 기존 `{"error":{"code","message","details?}}` 유지. 리소스 라우트는 `Authorization: Bearer <token>` + `X-Org-Id: <uuid>` 필요(경로에 `:orgId`가 있으면 그것으로 스코프).

### 인증 (public, prefix `/v1/auth`)
| 메서드·경로 | 최소권한 | 요청 | 응답 |
|-------------|----------|------|------|
| POST `/v1/auth/signup` | public | `{email, password, name?}` | `201 {user_id, org_id}` / 중복 `409` |
| POST `/v1/auth/login` | public | `{email, password}` | `200 {access_token, refresh_token, expires_in}` / 실패 `401` |
| POST `/v1/auth/refresh` | public | `{refresh_token}` | `200 {access_token, refresh_token, expires_in}` / 무효·재사용 `401` |
| POST `/v1/auth/logout` | public | `{refresh_token}` | `204` |
| GET `/v1/auth/oauth/:provider` | public | — | `302` (provider 또는 dev mock 콜백) |
| GET `/v1/auth/oauth/:provider/callback` | public | `?code=` 또는 `?mock_email=&mock_sub=` | `200 {access_token, refresh_token, expires_in}` |

### 조직·멤버·키
| 메서드·경로 | 최소권한 | 요청 | 응답 |
|-------------|----------|------|------|
| GET `/v1/orgs` | user(인증) | — | `200 {data:[{id,name,role}]}` |
| POST `/v1/orgs` | user(인증) | `{name}` | `201 {id}` (+owner 멤버십+default project) |
| GET `/v1/orgs/:orgId/members` | member | — | `200 {data:[{user_id,email,role}]}` |
| POST `/v1/orgs/:orgId/members` | admin | `{email, role?}` | `201 {user_id,role}` / 미가입 `404` / 중복 `409` |
| PATCH `/v1/orgs/:orgId/members/:userId` | admin | `{role}` | `200` / 마지막 owner 강등 `409` |
| DELETE `/v1/orgs/:orgId/members/:userId` | admin | — | `204` / 마지막 owner 제거 `409` |
| GET `/v1/orgs/:orgId/api-keys` | admin | — | `200 {data:[{id,name,role,last_used_at,expires_at,created_at}]}` (원문·해시 미노출) |
| POST `/v1/orgs/:orgId/api-keys` | admin | `{name, role?, expires_at?}` | `201 {id,name,role,key,expires_at,created_at}` (`key` 원문 1회) |
| DELETE `/v1/orgs/:orgId/api-keys/:id` | admin | — | `204` |

### 프로젝트
| 메서드·경로 | 최소권한 | 요청 | 응답 |
|-------------|----------|------|------|
| GET `/v1/projects` | viewer | — | `200 {data:[{id,org_id?,name,created_at}]}` |
| POST `/v1/projects` | member | `{name}` | `201 {id,name}` |
| GET `/v1/projects/:id` | viewer | — | `200 {…}` / RLS 0건 `404` |
| PATCH `/v1/projects/:id` | member | `{name}` | `200 {id,name}` |
| DELETE `/v1/projects/:id` | admin | — | `204` |

### 기존 리소스 라우트 (경로 유지, 이제 org 스코프 + RBAC)
`X-Org-Id` 헤더 필수. 권한: **GET=viewer**, **POST/PATCH/DELETE(변경)=member**(단 도메인 verify/스캔 트리거 등 쓰기 = member).
`/projects/:id/domains*`, `/projects/:id/load-tests`, `/load-tests/:id`(+`/abort`,`/results`), `/projects/:id/scans`, `/scans/:id`(+`/findings*`), `/projects/:id/apm/*`, `/projects/:id/reports`, `/reports/:id`(+`/share*`). 삭제 없음.
특례(그룹 밖): `POST /projects/:id/apm/ingest`(X-Ingest-Token), `GET /shared/:slug`(public), `GET /load-tests/:id/stream`(WS).

> 경로 규약 주의(frontend 합의 필요): **신규 auth/org/project는 `/v1` prefix**, **기존 리소스 라우트는 무prefix**(설계 §5.3 "경로 유지"). 후속 페이즈에서 리소스 라우트의 `/v1` 통일을 권장.

---

## 3. 마이그레이션 요약

- 러너: `migrations/*.sql` 사전순 적용(0001…0008). `docker-compose.yml`는 `./migrations` 디렉토리 전체를 `/docker-entrypoint-initdb.d`로 마운트.
- org_id 대상(비정규화, D-9): verified_domains, load_tests, load_test_results, scans, scan_findings, apm_agents, apm_spans, apm_logs, reports, report_shares. (organizations=id, projects/memberships/api_keys=기존 org_id)
- RLS 정책 14개(`org_isolation`) + organizations(id 스코프). 전부 `FORCE`.
- 롤: `klaro_app`(RLS 종속), `klaro_system`(BYPASSRLS 부트스트랩). 개발 비밀번호 고정, 프로덕션은 `.env` 주입.

---

## 4. 적용/실행 방법

```bash
cd services/load-test
docker compose up --build -d          # postgres(0001~0008 자동 적용) + redis + api + worker
curl -s localhost:8080/healthz        # {"status":"ok"}

# 회원가입 → 로그인 → 프로젝트 생성
curl -s -X POST localhost:8080/v1/auth/signup -H 'Content-Type: application/json' \
  -d '{"email":"me@x.com","password":"pw123456","name":"Me"}'      # 201 {user_id, org_id}
TOK=$(curl -s -X POST localhost:8080/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"me@x.com","password":"pw123456"}' | jq -r .access_token)
ORG=<org_id>
curl -s -X POST localhost:8080/v1/projects -H "Authorization: Bearer $TOK" -H "X-Org-Id: $ORG" \
  -H 'Content-Type: application/json' -d '{"name":"p1"}'

# psql RLS 검증 (세션 미설정 시 0건, klaro_app)
docker compose exec postgres psql -U klaro_app -d klaro -c "SELECT count(*) FROM load_tests"   # 0
docker compose exec postgres psql -U klaro   -d klaro -c \
  "SELECT rolname,rolbypassrls FROM pg_roles WHERE rolname LIKE 'klaro%'"
```

dev-token 경로(APP_ENV=dev): `Authorization: Bearer dev` → dev user/org(`…0003`/`…0001`), `X-Org-Id` 생략 시 dev-org 기본.

---

## 5. 검증 결과

- `go build ./...` → **그린**
- `go vet ./...` → **그린** (경고 0)
- `gofmt -l internal cmd` → 정리 완료(잔여 0)
- `go test ./...` (유닛, DB 불필요) → **그린** (auth·rbac·model·store·api·worker·scenario·breaker·domainverify)
- `go test -tags integration ./...` (Docker Postgres+Redis, klaro_app/klaro_system/redis 배선) → **그린**
  - `TestRLSSessionUnsetReturnsZero` — 세션 미설정 SELECT 0건(TENANT-02)
  - `TestRLSCrossOrgBlocked` — 크로스 org 조회 `ErrNotFound`(IDOR/RBAC-04), 동일 org 정상
  - `TestLoadTestLifecycle` — RLS tx 안 상태머신 전이 + 불법 전이 거부
  - `TestE2EAuthFlow` — signup(201)/중복(409)/login(200)/오류(401)/프로젝트 CRUD/타 org 스코프(404)/타 org 리소스(404)/미인증(401)
- DB 레벨 직접 검증: `klaro_app` bypassrls=f, `klaro_system` bypassrls=t, load_tests RLS enable+force, org_isolation 정책 14개, dev owner 멤버십, `klaro_app` 미설정 세션 load_tests 0건.

통합 테스트 실행법:
```bash
# 컨테이너 기동 후
export TEST_DATABASE_URL="postgres://klaro_app:klaro_app@localhost:5432/klaro?sslmode=disable"
export TEST_SYSTEM_DATABASE_URL="postgres://klaro_system:klaro_system@localhost:5432/klaro?sslmode=disable"
export TEST_REDIS_ADDR="localhost:6379"
go test -tags integration ./...
```
> `TEST_DATABASE_URL`은 반드시 **klaro_app**(NOBYPASSRLS)이어야 RLS가 실효한다. 슈퍼유저(klaro)로 지정하면 RLS 우회로 격리 테스트가 실패한다.

---

## 6. 불변식 준수 근거 (계약 §불변식 / 설계 §8)

| 불변식 | 근거 |
|--------|------|
| RLS 멀티테넌시(세션 미설정 0건) | `0006_rls.sql` `current_setting(...,true)` NULL 처리 + `klaro_app` NOBYPASSRLS(`0007`). 통합 테스트 `TestRLSSessionUnsetReturnsZero` + psql 0건 확인 |
| 테넌트 크로스 차단(IDOR) | id-only 조회가 org 스코프 tx에서 실행 → RLS 0건 → 404. `TestRLSCrossOrgBlocked` + e2e 타 org 리소스 404 |
| dev-token 스텁 제거 | `middleware.go` 고정 project 주입 폐기, dev user/org 매핑(D-1), APP_ENV=dev 한정 |
| 최소 권한 403/401 | `authenticate`(401) + `authorize(min)`(403) 4단 체인, 라우트별 min 부착(GET=viewer, 변경=member, 멤버·키=admin) |
| 비밀정보 비노출 | password는 bcrypt만 저장·응답 미포함, 로그인 실패 은닉(401); API Key는 sha256만 저장·원문 1회, `key_hash`/`revoked_at` json 제외; Refresh는 sha256만 Redis 저장 |
| RLS 우회 최소화(D-10) | `klaro_system`은 인증 전 users/membership 조회, 조직 생성, 공유 slug, ingest 해석에만 사용. 그 외 전부 app tx |
| 비용 정책([COST-05]) | 추가 라이브러리 전부 OSS(golang-jwt/v5, x/oauth2, x/crypto/bcrypt, crypto/sha256, 기존 redis). 신규 유료 의존 0. OAuth 크리덴셜 미설정 시 dev mock(오프라인) |
| 기존 기능 회귀 | S1 부하/서킷브레이커/WS, S2 스캔 경로는 `RunInOrg`로 RLS 관통, 유닛/통합 테스트 그린. 워커 `model.Job/ScanJob`에 OrgID 전파 |

---

## 7. 미완료 / 후속 항목 (명시적 제외 및 TODO)

- **초대 pending(`invitations`) 테이블**: Phase 2 연기(D-6). 현재 `POST /orgs/:orgId/members`는 **기존 가입 user만** email로 즉시 추가, 미가입 → `404`.
- **SAML(`/auth/saml/acs`)**: 범위 밖(M-4).
- **OAuth 실 provider 경로**: dev mock은 완전 동작·테스트됨. 실제 GitHub/Google 크리덴셜 경로(`Exchange`+userinfo)는 구현했으나 오프라인/무비용 원칙상 실호출 미검증 — 크리덴셜 주입 시 통합 검증 필요.
- **경로 prefix 불일치**: 신규(`/v1/*`) vs 기존 리소스(무prefix). frontend-builder와 경계면 합의 후 통일 권장.
- **subscription/billing owner 라우트**: 권한 매트릭스에만 기록, 미구현(범위 밖).
- **OAuth state CSRF 검증**: dev 단계 고정 state. 프로덕션은 state 저장/검증 추가 필요.
- **API Key admin/owner 관리 접근**: API Key는 유저 없음(UserID 빈 값) → `GET/POST /v1/orgs`(user 전용)는 API Key로 400. 멤버/키 관리 라우트는 role 기반(admin)으로 API Key도 role이 admin이면 접근 가능 — 설계 D-7 "member 기본"과 정합.
- **apm_spans/apm_logs**: MVP상 PostgreSQL 저장(TSDB 미도입). org_id+RLS 적용 완료.

---

## 8. 재감사 수정 내역 (F-1~F-5 + QA 운영)

invariants-reviewer 판정(`_workspace/06_invariants-reviewer_verdict.md`, 차단)과 qa-verifier 운영 이슈(`_workspace/05_qa-verifier_report.md`)를 반영해 차단 결함(F-1~F-3) + 권장(F-4/F-5) + QA 운영 2건을 수정했다.

| # | 심각도 | 수정 파일:라인(수정 후) | 조치 |
|---|--------|------------------------|------|
| **F-1** | critical | `internal/api/auth_handlers.go:174-181` (oauthCallback mock 분기) | mock 분기를 `d.AppEnv=="dev" && d.OAuth.DevMode(provider) && mock_email!=""`로 게이팅. 프로덕션/크리덴셜 구성 시 mock_email 무시 → code 교환 경로만. 임의 계정 로그인/탈취 차단 |
| **F-2** | high | `internal/api/auth_handlers.go:132-165` (oauthStart/oauthCallback) | 요청별 랜덤 state(`randState()`, 24B) → httpOnly 쿠키 `klaro_oauth_state`(maxAge 600s, prod는 Secure) 저장. 콜백에서 query state와 쿠키 일치 검증, 불일치 시 401. 검증 후 쿠키 즉시 폐기(1회용) |
| **F-3** | high | `internal/api/errors.go:22-26` + api 9개 파일 61개 콜사이트 | `writeInternal(c, err)` 헬퍼 신설(서버 로그만 남기고 응답은 상수 `"internal error"`). 500 `err.Error()` 원문 노출 전부 제거(잔여 0). signup UNIQUE 경합은 `store.SignupWithOrg`(`auth_store.go:58-65`)가 23505→`ErrConflict`, 핸들러(`auth_handlers.go:64-68`)가 409 매핑(선검사+INSERT 위반 양쪽) |
| **F-4** | medium | `cmd/api/main.go:22-42`, `cmd/worker/main.go:26-38` | `APP_ENV!=dev`이면 `SYSTEM_DATABASE_URL` 미설정 시 fail-fast(`log.Fatal`), 또한 app DSN과 동일하면 거부(별도 klaro_system 롤 강제). dev/test는 기존 기본값 유지 |
| **F-5** | medium | `internal/api/org_handlers.go:186-192` (createApiKey) | API Key role를 `member`/`viewer`로만 허용. owner/admin 키 발급 시 400. D-7 "member 기본" 취지 준수 |
| QA 운영1 | medium | `docker-compose.yml`(postgres volumes + top-level `volumes: klaro_pgdata`), `README.md` 로컬 실행 절 | 익명→명명 볼륨. "마이그레이션 변경 시 `docker compose down -v`" 명시 |
| QA 운영2 | low | `internal/queue/queue_test.go` | 테스트를 전용 Redis DB index(15) + FlushDB로 격리 → 워커(DB 0) 경쟁 소비 위음성 제거 |

**F-6/F-7 (범위 밖, 후속)**: F-6(JWT_SECRET dev 기본값 하드코딩) — 프로덕션 승격 시 강제 주입/기동 검증 필요, 후속. F-7(apm_spans/apm_logs PostgreSQL 저장, 시계열 RDB 밖 불변식) — 기존 MVP 결정·Phase 1 회귀 아님, 후속 TSDB 도입 과제.

### 신규 회귀 테스트
- `internal/api/e2e_test.go`
  - `TestOAuthDevMockFlow` (AppEnv=dev): start→state 쿠키+Location→**쿠키 없이 콜백 401(F-2)**→쿠키 포함 콜백 200+토큰(F-1 dev 정상)
  - `TestOAuthMockRejectedOutsideDev` (AppEnv=test): 유효 state로 F-2 통과시켜도 mock 분기 비활성 → code 부재 → 400 (F-1 프로덕션 차단 실증)
  - `newTestRouterEnv(t, appEnv)`로 AppEnv 주입 가능하게 리팩터

### 재검증 결과 (Docker Postgres+Redis, klaro_app/klaro_system + Redis)
- `go build ./...` → **그린**, `go vet ./...` → **그린**, `gofmt -l` 잔여 0
- 유닛 테스트(auth/rbac 등) → **그린**
- `go test -tags integration -count=1 ./...` → **전 패키지 그린** (api/auth/breaker/domainverify/model/queue/rbac/scenario/store/worker)
  - 신규 `TestOAuthDevMockFlow`·`TestOAuthMockRejectedOutsideDev`·기존 `TestE2EAuthFlow` PASS(verbose 확인)
  - `internal/queue` 워커 컨테이너 정지 없이도 PASS(DB 격리 반영)
- 잔여 500 `err.Error()` 누출: `grep` 0건 확인
