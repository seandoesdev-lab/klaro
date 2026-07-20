# klaro Phase 1 — invariants-reviewer 최종 감사 판정

## 재감사(2차) 판정 — 2026-07-20

**입력** `_workspace/03_backend-builder_manifest.md §8`(수정 내역) + 실제 코드 재독(auth_handlers.go, oauth.go, errors.go, cmd/{api,worker}/main.go, org_handlers.go, auth_store.go, store.go, e2e_test.go).

### 종합 판정: **배포 가능 (DEPLOYABLE)**

1차 차단 사유(F-1 critical)와 high 2건(F-2/F-3)이 모두 해소되었고, 권장 medium 2건(F-4/F-5)도 반영됐다. critical/high 잔여 0건. 남은 F-6(low)·F-7(범위 밖)은 문서화된 후속 과제로 Phase 1 배포를 막지 않는다.

| # | 심각도 | 해소 여부 | 재감사 근거(코드 재독) |
|---|--------|-----------|------------------------|
| F-1 | critical | **해소** | `auth_handlers.go:182` mock 분기가 `d.AppEnv=="dev" && d.OAuth.DevMode(provider) && c.Query("mock_email")!=""` **삼중 게이트**. 그 외는 `else if code:=Query("code")`만 → 실 provider `Exchange`. 프로덕션(AppEnv!=dev)·크리덴셜 구성(DevMode=false) 어느 쪽이든 mock 비활성 → `?mock_email=`로 임의 계정 로그인 불가. 프로파일 위조 우회 경로 없음(profile은 dev-nocreds mock 또는 실 code 교환에서만 생성). |
| F-2 | high | **해소** | `oauthStart`(`:146-160`)가 `randState()`(24B) → httpOnly 쿠키 `klaro_oauth_state`(maxAge 600, non-dev는 Secure) 세팅. `oauthCallback`(`:171-177`)이 `cookieState==""` 또는 `Query("state")!=cookieState`면 **401**, 검증 통과 후 쿠키 즉시 만료(1회용). 쿠키 없이/불일치 시 거부 확인. |
| F-3 | high | **해소** | `errors.go:24-27 writeInternal` = 서버 로그만 남기고 응답은 상수 `"internal error"`. 500 경로의 `err.Error()` 노출 grep 잔여 **0건**(남은 3건은 400/422 사용자 입력 검증 메시지: `loadtests.go:25` scenario, `auth_handlers.go:155` provider, `domains.go:74` verify — 내부정보 아님). signup UNIQUE 경합: `store.go:25 isUniqueViolation`(23505) → `auth_store.go:61 ErrConflict` → `auth_handlers.go:67` **409** 매핑(선검사+INSERT 위반 양쪽). |
| F-4 | medium | **해소** | `cmd/api/main.go:25-41`·`cmd/worker/main.go:29-36`: `APP_ENV!=dev`에서 `SYSTEM_DATABASE_URL` 미설정 시 `log.Fatal`, appDSN과 동일 시에도 `log.Fatal`(별도 klaro_system 롤 강제). dev/test는 기본값 유지. |
| F-5 | medium | **해소** | `org_handlers.go:189-193`: API Key role를 `member`/`viewer`로만 허용, owner/admin 발급 시 400. D-7 "member 기본" 정합. |
| F-6 | low | 미해소(후속) | `main.go:59` `JWT_SECRET` 기본값 `dev-insecure-jwt-secret` 잔존. env 주입 가능하나 F-4식 non-dev fail-fast 미적용. **권장**: 프로덕션 승격 전 JWT_SECRET에도 동일 fail-fast 추가. Phase 1(dev 기반) 배포는 비차단. |
| F-7 | — (범위 밖) | 미해소(후속) | apm_spans/apm_logs PostgreSQL 저장. 기존 MVP 결정, Phase 1 회귀 아님. TSDB 도입은 후속. |

### 신규 회귀 테스트 커버리지 확인 (코드 실독)
- `TestOAuthDevMockFlow`(`e2e_test.go:149`, AppEnv=dev): start→302+state 쿠키+Location 확인 → **쿠키 없이 콜백 401**(F-2 실증) → 쿠키 포함 콜백 200+토큰(F-1 dev 정상). F-2를 실제로 강제.
- `TestOAuthMockRejectedOutsideDev`(`:182`, AppEnv=test): 유효 state 쿠키로 F-2 통과시킨 뒤에도 mock 분기 비활성 → code 부재 → **400**. F-1의 비-dev 차단을 실증(게이트가 state가 아니라 AppEnv/DevMode임을 분리 증명). `newTestRouterEnv(t, appEnv)`로 AppEnv 주입.
- 두 테스트 모두 결함을 실질적으로 커버(단순 스모크 아님). 빌드·vet·유닛·통합(klaro_app/klaro_system) 그린은 사전 확인됨.

### 신규 결함
- 없음. 재감사 중 새 critical/high/medium 미발견.
- 참고(비차단): OAuth state 쿠키는 사용자 세션 비바인딩(double-submit) 방식 — 로그인 CSRF 방어로는 표준·충분. 프로덕션에서 `SameSite=Lax` 명시를 권고(선택).

### 1줄 요약
**배포 가능** — F-1(critical)·F-2·F-3(high) 완전 해소, F-4·F-5(medium) 반영, 신규 회귀 테스트가 F-1/F-2를 실증 커버, 잔여는 비차단 후속(F-6 JWT_SECRET fail-fast 권장, F-7 TSDB).

---

<!-- 이하 1차(차단) 판정 원문 보존 -->

# klaro Phase 1 — invariants-reviewer 최종 감사 판정

**작성** invariants-reviewer · **작성일** 2026-07-20 · **대상** `services/load-test` (인증 + RLS 멀티테넌시)
**입력** `_workspace/01~03`, 실제 코드(migrations 0004~0008, internal/{auth,rbac,tenancy,store,api}, cmd/*, docker-compose.yml, go.mod)

---

## 종합 판정: **차단 (BLOCKED)**

critical 1건(OAuth mock 인증 우회)으로 병합 차단. 이 1건을 제외한 RLS/멀티테넌시/비용 게이트 설계·구현은 견고하다. critical 수정 + high 2건 처리 후 재감사하면 배포 가능 궤도.

| # | 심각도 | 요약 | 파일:라인 | 담당 |
|---|--------|------|-----------|------|
| F-1 | **critical** | OAuth 콜백 `mock_email`이 환경·크리덴셜 무관하게 활성 → 임의 계정 로그인(계정 탈취) | `internal/api/auth_handlers.go:152` | backend-builder |
| F-2 | **high** | OAuth state CSRF 미검증(고정 "state") | `internal/api/auth_handlers.go:135`, `internal/auth/oauth.go:78` | backend-builder |
| F-3 | **high** | 500 응답이 `err.Error()` 원문 노출(DB 스키마·제약명 정보 누출) | 전 핸들러 다수(`auth_handlers.go:48,66` 등) | backend-builder |
| F-4 | medium | `store.New` sysDSN 공란 시 appDSN 재사용 — 오구성 시 RLS 무력화 여지 | `internal/store/store.go:47-49` | backend-builder |
| F-5 | medium | API Key role를 owner/admin으로 발급 가능 → D-7 "member 기본" 초과 권한 | `internal/api/org_handlers.go:186-192` | backend-builder |
| F-6 | low | JWT_SECRET 기본값 `dev-insecure-jwt-secret` 하드코딩(dev 한정, prod 승격 리스크) | `cmd/api/main.go:38`, `docker-compose.yml:31` | backend-builder |
| F-7 | low | 시계열(apm_spans/apm_logs) PostgreSQL 저장 — 불변식 "시계열 RDB 밖" 대비 미정합(MVP·기존 결정, Phase 1 회귀 아님) | `migrations/0003`, `apm_store.go` | (범위 밖, 후속) |

---

## 1. RLS 멀티테넌시 — 통과

- **정책 완비**: `0006_rls.sql`이 org_id 보유 13개 테이블 + organizations(id 스코프)에 `ENABLE`+`FORCE ROW LEVEL SECURITY`+`org_isolation`(USING/WITH CHECK 동일 `current_setting('app.current_org',true)::uuid`) 적용. 계약 TENANT-01/02 대상 테이블(verified_domains, load_tests, load_test_results, scans, scan_findings, apm_agents, apm_spans, apm_logs, reports, report_shares, projects, memberships, api_keys) 전부 포함. 누락 없음.
- **NOBYPASSRLS 앱 롤**: `0007_roles.sql:8` `klaro_app ... NOSUPERUSER NOBYPASSRLS`. `0007:12` `klaro_system ... BYPASSRLS`. `cmd/api/main.go:24-25`·`docker-compose.yml:26-27`이 app=klaro_app, sys=klaro_system로 배선. 슈퍼유저(klaro)는 초기화·마이그레이션 전용, 런타임 접속 아님 → RLS 실효.
- **tx 스코프 강제**: `store.go:71-81 BeginOrg`가 `SELECT set_config('app.current_org',$1,true)`(tx-local)로 스코프. HTTP는 `middleware.go:122 tenancyTx`가 요청당 tx로 감싸고 상태≥400/에러 시 롤백. org 스코프 핸들러는 전부 `tenancy.Tx(c)` 주입 확인(projects/loadtests/scans/apm/reports/org·member·key). 워커는 `worker.go:47`·`scanworker.go:55` `RunInOrg(job.OrgID,…)`로 관통.
- **미설정 0건**: `current_setting(...,true)` NULL→false 패턴으로 세션 미설정 SELECT 0건(정보 누출 방지). 근거표 및 통합테스트(`TestRLSSessionUnsetReturnsZero`)와 정합.

## 2. 테넌트 크로스 차단(IDOR) — 통과

- id-only 조회(`GetLoadTest`/`GetProject`/`GetScan`/`GetReport`/`GetDomain`/`GetResult`)는 전부 `q Querier`=org 스코프 tx에서 실행 → RLS가 org 밖 행 0건 → `ErrNotFound`→404. 크로스테넌트 읽기 봉쇄(RBAC-04).
- INSERT는 `org_id`를 `current_setting('app.current_org',true)::uuid`로 세팅 + RLS WITH CHECK → 타 org로의 쓰기 불가.
- **BYPASSRLS(sys) 풀 사용처 전수 확인**(grep): `GetUserByEmail`/`GetUserByID`(로그인 전), `GetMembership`(resolveOrg, 스코프 확정 전), `ListOrgsByUser`/`CreateOrgWithOwner`(org 목록·생성, 스코프 이전), `GetApiKeyByHash`/`TouchApiKey`(인증), `SignupWithOrg`/`CreateOAuthUserWithOrg`/`LinkOAuth`(가입), `GetShareBySlug`/`GetReportSys`(공유 slug public), `ResolveAgentOrg`(ingest). **전부 D-10 부트스트랩 한정**, 일반 org 스코프 조회에 오용 없음. `ListOrgsByUser`/`GetMembership`은 인증된 user_id로 필터해 크로스테넌트 누출 없음. → sys 풀 오용 없음.
- APM ingest: `ResolveAgentOrg`가 (project_id, ingest_token) 쌍을 검증하고 그 agent의 org를 반환 → 이후 app tx로 전환해 org 스코프 write. 토큰-프로젝트 불일치 시 401. 크로스 org write 불가.

## 3. 비밀정보 비노출 — 통과

- `model.User.PasswordHash`/`OAuthSub` `json:"-"`, `ApiKey.RevokedAt`/`OrgID` `json:"-"`. password/refresh/key 원문·해시 응답 미노출.
- password bcrypt(cost12, `password.go`), API Key sha256+`subtle.ConstantTimeCompare`(`apikey.go`), Refresh sha256만 Redis 저장·회전·재사용 감지·family 폐기(`refresh.go`). 회전 로직·로그아웃 멱등 정상.
- 로그인 실패 계정 은닉: `login`은 유저부재/해시불일치를 단일 `401 invalid credentials`로 통일(`auth_handlers.go:83`). org 미소속 404 은닉(D-8). 
- JWT `Verify`가 HMAC 메서드 강제(alg 혼동/none 방지, `jwt.go:46-49`).
- 단, F-3(err.Error() 노출)은 비밀 자체는 아니나 정보 누출 → medium.

## 4. 비용 게이트 [COST-05] — 통과

- `go.mod` 신규: golang-jwt/v5, x/oauth2, x/crypto — 전부 OSS·무비용. Bedrock/Stripe/유료 SaaS SDK 신규 추가 0건. (indirect `mongo-driver` 등은 전이 의존, 유료 호출 아님.)
- OAuth 크리덴셜 미설정 시 실외부 호출 없이 dev mock(`oauth.go:63,68-80`). 개발 무비용 원칙 준수. — 단 이 mock 게이팅이 F-1의 원인(아래).

## 5. 기존 불변식 회귀 — 통과(단 F-7 유의)

- **도메인 소유권 게이트(SC-01)**: `loadtests.go:36-44`(부하)·`scans.go:46-54`(DAST)가 `IsDomainVerified` 선행 강제 유지. 미검증 시 403 DOMAIN_NOT_VERIFIED. 회귀 없음.
- **서킷 브레이커**: `worker.go:92 brk.ShouldAbort` 유지.
- **Ephemeral/워커 idle=0/mTLS**: Phase 1 미접촉, 회귀 도입 없음.
- **시계열 RDB 분리**: apm_spans/apm_logs가 PostgreSQL 저장(F-7). 기존 MVP 결정이며 Phase 1이 새로 훼손한 것은 아님 → 회귀 아님, 후속 페이즈 과제로만 기록.

## 6. 인가 정확성 — 통과

- 서열 `owner>admin>member>viewer`(`rbac/role.go`), `AtLeast`는 미지 역할(rank 0) 거부. GET=viewer/변경=member/멤버·키=admin/삭제=admin 라우트 매핑 정확(`router.go:70-113`). viewer 읽기전용(D-5) 충족.
- 마지막 owner 강등/제거 방지(D-6): `WouldDemoteLastOwner`/`WouldRemoveLastOwner` + 핸들러 409(`org_handlers.go:123,153`). 정확.
- API Key는 user 전용 `/v1/orgs`(목록·생성)에서 `uid==""`로 400 거부(구분 가능, AUTH-05). — 단 F-5(관리 라우트는 role 기반이라 owner/admin 키로 접근 가능).

---

## 위반 상세 및 수정 방향

### F-1 (critical) — OAuth mock 인증 우회 / 계정 탈취
`auth_handlers.go:144-168 oauthCallback`은 쿼리 `mock_email` 존재 시 **무조건** mock 프로파일 분기를 탄다:
```
if mockEmail := c.Query("mock_email"); mockEmail != "" {
    profile = d.OAuth.MockProfile(provider, mockEmail, c.Query("mock_sub"))
}
```
이 분기는 `d.AppEnv`도, provider 크리덴셜 설정 여부(`DevMode`)도 확인하지 않는다. 따라서 **실제 GitHub/Google 크리덴셜이 구성된 프로덕션에서도** 공격자가
`GET /v1/auth/oauth/github/callback?mock_email=victim@corp.com`
호출만으로 그 이메일 계정의 access/refresh 토큰을 발급받는다(기존 계정이면 로그인, 없으면 계정+org 생성). dev-token 경로는 `middleware.go:33`에서 `d.AppEnv=="dev"`로 올바르게 게이팅되지만 OAuth mock은 누락. 인증 전체 우회로 임의 사용자 사칭·계정 탈취가 가능.
**왜 위험한가**: 인증 게이트가 환경 오구성 하나(크리덴셜 미주입) 또는 아예 무조건에 의해 무력화 → 멀티테넌시 격리의 상위 전제(누가 로그인했는가)가 붕괴. RLS가 완벽해도 공격자가 피해자 org의 owner로 로그인하면 무의미.
**수정**: mock 분기를 `d.AppEnv=="dev" && d.OAuth.DevMode(provider)`일 때만 허용. 그 외 환경에서는 `code` 교환 경로만. AppEnv를 핸들러에서 참조 가능(Deps.AppEnv).

### F-2 (high) — OAuth state CSRF 미검증
`oauthStart`가 고정 `"state"`(`auth_handlers.go:135`)를 쓰고 `oauthCallback`은 state를 검증하지 않는다. OAuth 로그인 CSRF(피해자에게 공격자 계정 강제 연결/로그인) 가능. 매니페스트 §7이 인지한 TODO지만 실 provider 경로 활성 시 필수.
**수정**: 요청별 랜덤 state를 서명 쿠키/Redis에 저장하고 콜백에서 일치 검증. F-1 수정과 함께 처리.

### F-3 (high) — 내부 오류 원문 노출
다수 핸들러가 `writeError(c,500,"INTERNAL",err.Error(),nil)`로 pgx/제약 위반 메시지(테이블·컬럼·제약명)를 클라이언트에 그대로 반환. 스키마 정보 누출 + 동시 가입 시 UNIQUE 위반이 409 대신 원문 500으로 노출.
**수정**: 500 응답 메시지를 상수화("internal error")하고 상세는 서버 로그로만. 가입 UNIQUE 위반은 409로 매핑.

### F-4 (medium) — sysDSN 폴백
`store.go:47-49` sysDSN 공란 시 appDSN 재사용. 주석이 "RLS 강제 안 됨"을 인정. 운영자가 실수로 DATABASE_URL에 슈퍼유저/BYPASSRLS 롤을 넣고 SYSTEM_DATABASE_URL을 비우면 전 경로 RLS 무력화.
**수정**: 프로덕션(APP_ENV!=dev)에서 sysDSN 공란이면 기동 실패(fail-fast). 또는 두 DSN 롤명이 다름을 검증.

### F-5 (medium) — API Key 과권한 발급
`createApiKey`가 role을 owner/admin으로 허용(`rbac.Valid`만 검사). D-7 "member 기본" 취지를 넘어 admin/owner 키가 멤버·키 관리까지 수행 가능.
**수정**: API Key role를 member/viewer로 제한(관리 권한 키 금지) 또는 최소한 owner 발급 차단.

### F-6 (low) — JWT_SECRET 기본값
dev 편의 하드코딩. 프로덕션 승격 시 강제 주입/기동 검증 필요.

---

## 재감사 조건 (backend-builder 처리 대상)
1. **F-1** `auth_handlers.go:152` — mock 분기 AppEnv+DevMode 게이팅(필수, 차단 해제 조건).
2. **F-2** OAuth state 검증 도입.
3. **F-3** 500 오류 원문 비노출 + 가입 409 매핑.
4. (권장) F-4/F-5 처리.

F-1~F-3 수정 후 재제출 시, 해당 경로 재감사 + `go test -tags integration` 그린 확인으로 판정 갱신 예정.
