# klaro Phase 1 — 요구사항 계약: 인증 + PostgreSQL RLS 멀티테넌시 기반

**작성** spec-analyst · **작성일** 2026-07-20 · **상태** 초안(빌더 착수용)
**단일 진실 공급원** `docs/klaro/00~05` + `CLAUDE.md` 아키텍처 불변식
**확정 스택 전제** Control Plane = Go 유지, 테넌트 격리 = 공용 DB + RLS(스키마/DB 분리 아님)

> 본 계약은 "무엇을(WHAT) + 수용 기준"만 규정한다. 구현 방식(HOW)은 architect/builder 몫이며, 스택 미확정 항목은 `## architect 결정 필요 목록`으로 분리했다.

---

## 범위

Phase 1은 아래에만 한정한다. 부하/스캔/APM/리포트의 **기능 자체**는 범위 밖이나, 그 리소스에 org 스코프·RLS·RBAC를 소급 적용하는 부분은 포함한다.

1. **인증(AUTH)** — 회원가입, 로그인(JWT Access/Refresh 발급), 토큰 갱신·검증, OAuth 소셜 로그인(GitHub/Google), API Key 인증, 기존 `authStub`(dev-token) 대체.
2. **인가(RBAC)** — 계층 `Org > Project > Resource`, 역할 `owner/admin/member/viewer` 권한 매트릭스와 강제.
3. **멀티테넌시 RLS(TENANT)** — 모든 org 스코프 테이블에 `org_id` + Row-Level Security, 세션 변수 `app.current_org` 강제, store 계층 org 컨텍스트 전달.
4. **데이터 모델(DATA)** — `users`, `memberships`, `api_keys` 신설 및 기존 테이블 `org_id` 보강.
5. **인증/인가 API(API)** — 로그인·갱신·org/project/멤버 관리·초대·API Key.

**명시적 제외**: SAML(`POST /auth/saml/acs`, M3, `03-api-spec.md:35`), `github_installations` 연동(별도 GitHub 연동 페이즈), 과금(Billing) 로직 자체(단 `owner`-only 권한 존재는 매트릭스에 기록), 시계열 저장소 분리.

---

## 현재 상태 격차 요약 (근거: 실제 파일)

| 영역 | 문서 기대 | 현재 코드 | 격차 |
|------|-----------|-----------|------|
| 인증 | JWT+OAuth2 (`00-tech-stack.md:47`, `01-technical-design.md:142`) | 고정 dev-token→고정 project_id, org 개념 없음 (`internal/api/middleware.go:10-25`) | 전면 신설. `middleware.go:13` 주석이 "Replace with JWT/OAuth/RBAC in production (swap point)"로 교체 지점 명시 |
| 유저/멤버십 | `users`, `memberships`, `api_keys`, `github_installations` 테이블 (`02-data-model.md:41-75`) | 존재하지 않음. `organizations`만 `id/name/created_at` 단순형(`migrations/0001_init.sql:3-7`) | `users`/`memberships`/`api_keys` 미존재 → 신설 |
| org_id 보유 | "org_id 가진 모든 테이블에 RLS"(`02-data-model.md:272`, `CLAUDE.md` 불변식) | `projects`만 `org_id` 보유(`0001_init.sql:11`). `verified_domains`·`load_tests`·`load_test_results`·`scans`·`scan_findings`·`apm_agents`·`apm_spans`·`apm_logs`·`reports`·`report_shares`는 `project_id`만 있고 `org_id` 없음 | 아래 §RLS 격차표 |
| RLS 정책 | 전 테넌트 테이블 RLS + `SET app.current_org` (`01-technical-design.md:133`, `02-data-model.md:272`) | 마이그레이션 어디에도 `ENABLE ROW LEVEL SECURITY`/`CREATE POLICY`/`app.current_org` 없음(grep 확인, 0건) | 전면 신설 |
| 테넌트 스코핑(쿼리) | RLS로 강제 | store가 공유 pool로 `project_id`만 필터. `GetLoadTest`(`store.go:43-62`)·`GetDomain`(`store.go:159-169`)·`GetResult` 등은 **id만으로 조회(테넌트 필터 없음) → 크로스테넌트 읽기(IDOR) 위험** | RLS + store org 컨텍스트로 차단 |
| 역할 | owner/admin/member/viewer (`01-technical-design.md:70`) | 없음 | 신설 |
| 시드 | dev org/project 존재(`0001_init.sql:58-62`) | org·project만, **소유 user·membership 없음** → RLS 켜면 dev 흐름 조회 0건 | dev user+membership 시드 필요 |

---

## 요구사항 계약

### A. 인증 (AUTH)

#### [AUTH-01] 회원가입 (이메일/비밀번호)
- 설명: 신규 유저 생성. 최초 가입 시 기본 조직 자동 생성 및 `owner` 멤버십 부여 여부는 결정 필요(아래 D-6).
- 관련 ID/근거: `03-api-spec.md:31`(`POST /auth/signup` public), `02-data-model.md:41-48`(users).
- 수용 기준:
  - `POST /v1/auth/signup {email, password}` → 201. 동일 email 재가입 시 `409 CONFLICT`.
  - `password_hash`는 단방향 해시로만 저장(평문 금지). email은 `citext UNIQUE`로 대소문자 무시 유일성.
  - 응답에 원문 비밀번호·해시 미포함.
- 데이터 모델 참조: `users`(§DATA-01).
- API 참조: `POST /auth/signup`.

#### [AUTH-02] 로그인 — JWT Access/Refresh 발급
- 설명: 자격 검증 후 Access/Refresh 토큰 발급.
- 관련 ID/근거: `03-api-spec.md:32`, `00-tech-stack.md:47`("JWT(Access/Refresh)").
- 수용 기준:
  - `POST /v1/auth/login {email, password}` → 200 `{access_token, refresh_token, expires_in}`. 실패 시 `401 UNAUTHENTICATED`(계정 존재 여부 노출 금지).
  - Access 토큰 클레임에 `user_id`(sub) 포함, org 스코프는 요청별 `X-Org-Id` + 멤버십 검증으로 결정(§RBAC-03).
  - 토큰 서명 검증 실패/만료 시 `401`.
- API 참조: `POST /auth/login`.

#### [AUTH-03] 토큰 갱신
- 관련 ID/근거: `03-api-spec.md:33`(`POST /auth/refresh` public).
- 수용 기준:
  - 유효 Refresh → 새 Access(및 회전 정책 시 새 Refresh) 발급. 무효/만료/폐기 Refresh → `401`.
  - 로그아웃/폐기된 Refresh 재사용 차단 가능해야 함(폐기 목록 또는 회전 감지 — 방식은 결정 필요 D-2).
- API 참조: `POST /auth/refresh`.

#### [AUTH-04] OAuth 소셜 로그인 (GitHub/Google)
- 설명: OAuth2 authorization code 흐름. 신규면 유저 생성(`oauth_provider`/`oauth_sub`), 기존이면 로그인.
- 관련 ID/근거: `03-api-spec.md:34`(`GET /auth/oauth/:provider`), `02-data-model.md:47`, `00-tech-stack.md:47`.
- 수용 기준:
  - `GET /v1/auth/oauth/:provider`(github|google) 개시 → 콜백에서 프로파일 획득 → 유저 매칭/생성 후 AUTH-02와 동일 토큰 발급.
  - OAuth 전용 유저는 `password_hash` NULL 허용(`02-data-model.md:46`).
  - 동일 email이 비밀번호 계정으로 이미 있을 때의 병합/충돌 처리는 결정 필요(D-4).
  - 개발 단계 무비용 원칙상 실제 OAuth 앱 크리덴셜 없이도 동작하는 dev/mock 경로 필요 여부는 결정 필요(D-3).
- API 참조: `GET /auth/oauth/:provider`.

#### [AUTH-05] API Key 인증 (CI/CLI)
- 설명: `Authorization: Bearer <API_KEY>`로 org 스코프 인증. JWT와 동일 미들웨어에서 분기.
- 관련 ID/근거: `03-api-spec.md:15`, `02-data-model.md:59-66`(api_keys), `03-api-spec.md:42-44`.
- 수용 기준:
  - API Key는 `key_hash`로만 저장, 발급 응답에서만 원문 1회 노출. `expires_at`/`revoked_at` 경과 시 `401`.
  - API Key 인증은 발급 org에 고정 스코프. 인증 시 `last_used_at` 갱신.
  - API Key 요청은 유저 대화형 전용 엔드포인트(예: 멤버 초대)와 권한 구분 가능해야 함(범위는 D-7).
- 데이터 모델 참조: `api_keys`(§DATA-03).
- API 참조: `GET/POST/DELETE /orgs/:orgId/api-keys`.

#### [AUTH-06] 통합 인증 미들웨어 — dev-token 스텁 대체 (MUST)
- 설명: `authStub`를 JWT/API Key 검증 미들웨어로 교체. 검증 성공 시 `user_id` + 확정 `org_id`(+ 역할)를 컨텍스트에 주입하고, 후속 DB 접근 전 세션 org 스코프를 설정(§TENANT-03).
- 관련 ID/근거: `internal/api/middleware.go:14-25`, `internal/api/router.go:44`(`authStub(d.DevToken)`), `CLAUDE.md`("dev-token 스텁 대체").
- 수용 기준:
  - 기존 `c.Set("project_id", devProjectID)` 고정 주입(`middleware.go:22`) 제거. 컨텍스트는 `user_id`+`org_id`(+role) 기준으로 재구성.
  - 토큰/키 없음·무효 → `401 UNAUTHENTICATED`(기존 에러 포맷 `errors.go` 유지).
  - dev 환경 편의를 위한 dev-token 경로 유지 여부는 결정 필요(D-1); 유지하더라도 반드시 실제 user/org/membership에 매핑되어야 하며 고정 project 직접 주입은 금지.

---

### B. 인가 / RBAC (RBAC)

#### [RBAC-01] 역할 계층 및 서열 (MUST)
- 설명: 역할 `owner > admin > member > viewer` 서열 정의. 상위는 하위 권한 포함.
- 관련 ID/근거: `01-technical-design.md:70`, `02-data-model.md:56`(memberships.role enum).
- 수용 기준: `memberships.role` enum = `(owner, admin, member, viewer)`. 권한 판정은 "요구 최소 역할 이상"으로 평가.

#### [RBAC-02] 권한 매트릭스 (Phase 1 엔드포인트, MUST)
- 근거: `03-api-spec.md:36-49`, `:83-85`의 "권한" 열. 접근 등급 정의: `public`(비인증) < `user`(임의 인증 유저, org 무관) < `viewer` < `member` < `admin` < `owner`.

| 엔드포인트 | 최소 권한 | 근거 |
|-----------|-----------|------|
| POST `/auth/*` (signup/login/refresh/oauth) | public | `03-api-spec.md:31-34` |
| POST `/orgs` (조직 생성) | user | `03-api-spec.md:37` |
| GET `/orgs` (내 조직 목록) | member | `03-api-spec.md:36` |
| GET `/orgs/:orgId/members` | member | `03-api-spec.md:38` |
| POST `/orgs/:orgId/members` (초대) | admin | `03-api-spec.md:39` |
| PATCH `/orgs/:orgId/members/:userId` (역할 변경) | admin | `03-api-spec.md:40` |
| DELETE `/orgs/:orgId/members/:userId` | admin | `03-api-spec.md:41` |
| GET/POST `/orgs/:orgId/api-keys` | admin | `03-api-spec.md:42-43` |
| DELETE `/orgs/:orgId/api-keys/:id` | admin | `03-api-spec.md:44` |
| GET/POST `/projects` | member | `03-api-spec.md:45-46` |
| GET/PATCH `/projects/:id` | member | `03-api-spec.md:47-48` |
| DELETE `/projects/:id` | admin | `03-api-spec.md:49` |
| POST `/orgs/:orgId/subscription` (플랜 변경, 참고) | owner | `03-api-spec.md:84` |
| GET `/orgs/:orgId/subscription`·`/usage` (참고) | admin | `03-api-spec.md:83,85` |

- 수용 기준:
  - 권한 미달 요청은 `403 FORBIDDEN`(`03-api-spec.md:23`). 미인증은 `401`.
  - `viewer`는 명시적 매트릭스에 없음 → 읽기(GET) 전용으로 취급, 쓰기(POST/PATCH/DELETE) 불가. (해석 근거 부재 — D-5 참조)
  - 역할 변경 시 마지막 `owner` 강등/제거 방지(조직에 owner 0명 금지). 문서 미규정이나 무결성 필수로 못 박음 → D-6.

#### [RBAC-03] org 스코프 해석 (MUST)
- 설명: 유저는 여러 org에 소속 가능(`02-data-model.md:21` N:M). 요청별로 활성 org를 확정.
- 관련 ID/근거: `03-api-spec.md:16`("헤더 `X-Org-Id` 또는 경로로 스코프"), `02-data-model.md:50-57`.
- 수용 기준:
  - 경로에 `:orgId`가 있으면 그 값, 없으면 `X-Org-Id` 헤더로 org 확정.
  - 확정 org에 대한 유저 멤버십이 없으면 `403`(존재 자체 은닉이면 `404`도 허용 — D-8).
  - 확정 org의 `id`가 이후 DB 세션 스코프(§TENANT-03)에 사용됨.

#### [RBAC-04] Project 하위 스코프 (MUST)
- 설명: `:id`(project) 대상 요청은 해당 project가 활성 org 소속인지 검증 후 처리.
- 관련 ID/근거: `01-technical-design.md:70`(`Org > Project > Resource`).
- 수용 기준: project가 활성 org 소속이 아니면 RLS로 조회 0건 → `404 NOT_FOUND`. 경로 리소스(load-test/scan/report 등)도 org 스코프 밖이면 `404`(현재 `GetLoadTest` id-only 조회의 IDOR를 RLS가 차단).

---

### C. 멀티테넌시 RLS (TENANT)

#### [TENANT-01] org 스코프 테이블에 `org_id` 보강 (MUST)
- 설명: 아래 테이블은 현재 `project_id`만 보유 → RLS 정책 대상이 되도록 `org_id`를 도입한다.
- 관련 ID/근거: `02-data-model.md:272`, `CLAUDE.md` 불변식("org_id를 가진 모든 테이블에 RLS").

RLS 격차표 (실제 마이그레이션 기준):

| 테이블 | 현재 org_id | 근거(file:line) | 조치 |
|--------|-------------|-----------------|------|
| organizations | (자체가 테넌트 루트, `id`가 스코프 키) | `0001_init.sql:3-7` | RLS: `id = current_org` |
| projects | 있음 | `0001_init.sql:9-14` | RLS 대상 |
| verified_domains | 없음 | `0001_init.sql:16-25` | org_id 추가 |
| load_tests | 없음 | `0001_init.sql:27-39` | org_id 추가 |
| load_test_results | 없음(load_test 경유) | `0001_init.sql:42-55` | org_id 추가 또는 조인 정책 — D-9 |
| scans | 없음 | `0002_scans.sql:2-14` | org_id 추가 |
| scan_findings | 없음(scan 경유) | `0002_scans.sql:17-29` | org_id 추가 또는 조인 정책 — D-9 |
| apm_agents | 없음 | `0003_apm_reports.sql:5-13` | org_id 추가 |
| apm_spans | 없음 | `0003_apm_reports.sql:15-27` | org_id 추가 |
| apm_logs | 없음 | `0003_apm_reports.sql:31-38` | org_id 추가 |
| reports | 없음 | `0003_apm_reports.sql:42-53` | org_id 추가 |
| report_shares | 없음(report 경유; public 조회는 slug) | `0003_apm_reports.sql:56-64` | org_id 추가 또는 조인 정책 — D-9 |
| users | (전역, org 비귀속) | `02-data-model.md:41` | RLS 비대상 |
| memberships | org_id 보유(신설) | `02-data-model.md:50-57` | RLS 대상 |
| api_keys | org_id 보유(신설) | `02-data-model.md:59-66` | RLS 대상 |

- 수용 기준:
  - 위 org 스코프 테이블 전부에 `org_id uuid NOT NULL` 존재(직접 컬럼 또는 상위 조인). 신규 마이그레이션(`0004_*` 이상)으로 추가하고 기존 시드 데이터 백필.
  - 데이터 모델 문서(`02-data-model.md`)와 실제 스키마의 일치. (문서에는 이들 테이블에 org_id가 명시되지 않았고 §3만 "org_id 가진 모든 테이블"이라 기술 → 문서-스키마 모순, `## 모호/모순` M-1 참조.)

#### [TENANT-02] Row-Level Security 정책 (MUST)
- 설명: org 스코프 전 테이블에 RLS 활성 + 정책 생성.
- 관련 ID/근거: `02-data-model.md:272`, `01-technical-design.md:133`.
- 수용 기준:
  - 각 대상 테이블 `ENABLE ROW LEVEL SECURITY` + `FORCE ROW LEVEL SECURITY`(테이블 소유자도 우회 금지).
  - `USING (org_id = current_setting('app.current_org')::uuid)` 및 INSERT `WITH CHECK` 동일 조건.
  - 세션 변수 미설정 상태에서 org 스코프 테이블 SELECT는 0건(정보 누출 방지).
  - RLS 우회가 필요한 인증 전 경로(로그인 시 users 조회, `GET /shared/:slug` 공유 리포트 `router.go:42`, APM ingest `router.go:40`)의 처리 규칙 명시 필요 — D-10.

#### [TENANT-03] 세션 org 스코프 강제 — store org 컨텍스트 (MUST)
- 설명: 인증 미들웨어 확정 org를 DB 커넥션 세션 변수 `app.current_org`로 설정해야 RLS가 작동. 현재 store는 공유 `pgxpool`을 직접 사용하며 세션 변수 설정 지점이 없음(`store.go:19-29`, 각 메서드 `s.pool.QueryRow/Exec`).
- 관련 ID/근거: `store.go` 전반, `CLAUDE.md`("SET app.current_org = <uuid>로 스코프 강제").
- 수용 기준:
  - 모든 org 스코프 쿼리는 `app.current_org`가 설정된 커넥션에서 실행됨이 보장(pool 커넥션 재사용에도 누출 없음).
  - 요청 종료 시 세션 변수가 다음 요청에 승계되지 않음(트랜잭션 `SET LOCAL` 또는 커넥션 획득/리셋 — 방식은 architect 결정 D-11).
  - store 시그니처는 org 컨텍스트(또는 org 바인딩 실행자)를 받도록 변경. 미들웨어→핸들러→store로 org 전달 경로 확립.
  - 회귀: 기존 `services/load-test` 테스트(`store_test.go` 등)가 RLS/org 컨텍스트 반영해 통과.

#### [TENANT-04] dev 시드 정합 (MUST)
- 설명: RLS 활성 후에도 기존 dev org/project 흐름이 동작하도록 dev user + membership 시드.
- 관련 ID/근거: `0001_init.sql:57-62`(org/project 시드만 존재, user/membership 없음).
- 수용 기준: dev org(`...0001`)에 dev user + `owner` membership 시드. dev-token 경로 유지 시(D-1) 이 user/org로 매핑.

---

### D. 데이터 모델 (DATA)

#### [DATA-01] users 테이블
- 근거: `02-data-model.md:41-48`.
- 수용 기준: `id uuid PK`, `email citext UNIQUE`, `password_hash text NULL`, `oauth_provider text NULL`, `oauth_sub text NULL`, `created_at`. `citext` 확장 활성. (`name` 컬럼은 문서에 없음 — 필요 시 D-12.) `(oauth_provider, oauth_sub)` 유일성 인덱스 필요 여부 D-12.

#### [DATA-02] memberships 테이블
- 근거: `02-data-model.md:50-57`.
- 수용 기준: `id uuid PK`, `org_id FK`, `user_id FK`, `role enum(owner,admin,member,viewer)`, `UNIQUE(org_id, user_id)`. 인덱스: `(user_id)`(내 org 목록 조회), `(org_id)`.

#### [DATA-03] api_keys 테이블
- 근거: `02-data-model.md:59-66`.
- 수용 기준: `id uuid PK`, `org_id FK`, `key_hash text`, `name text`, `last_used_at/expires_at/revoked_at timestamptz NULL`. `key_hash` 조회용 인덱스.

> `github_installations`(`02-data-model.md:68-75`)는 Phase 1 제외(§범위).

---

## 불변식 체크리스트 (Phase 1 관련, MUST)

- [ ] **RLS 멀티테넌시**: org 스코프 전 테이블 RLS + `app.current_org` 세션 강제(TENANT-02, TENANT-03). 세션 미설정 시 0건.
- [ ] **테넌트 크로스 차단**: id-only 조회(`store.go:43`,`159` 등)가 RLS로 차단되어 크로스테넌트 읽기 불가(RBAC-04).
- [ ] **dev-token 스텁 제거**: 고정 project 주입(`middleware.go:22`) 폐기, 실제 인증으로 대체(AUTH-06).
- [ ] **최소 권한**: 권한 매트릭스 위반 시 403/401(RBAC-02).
- [ ] **비밀정보 비노출**: password/API key 원문·해시 미노출, 실패 응답 계정 은닉(AUTH-01,02,05).
- [ ] **비용 정책**: 유료 외부 서비스 신규 추가 금지([COST-05]). OAuth/JWT 라이브러리는 OSS·무비용. (Bedrock 무관)
- [ ] 도메인 소유권 게이트·서킷 브레이커·Ephemeral·워커 idle=0·mTLS·시계열 RDB 분리 = Phase 1 직접 대상 아님(기존 유지, 위반 도입 금지).

---

## architect 결정 필요 목록

> 문서에 근거 없음 → architect가 결정하거나 사용자에게 확인. backend-builder는 이 결정 전 스택 특정 코드 착수 금지.

- **D-1 dev-token 경로 유지 여부**: 프로덕션 인증 도입 후에도 dev 편의용 dev-token을 남길지, 남긴다면 실제 user/org 매핑 방식.
- **D-2 Refresh 토큰 저장·회전 전략**: 무상태 JWT vs Redis(`00-tech-stack.md:27` 세션 캐시) 저장 폐기목록/회전 감지. 로그아웃 처리.
- **D-3 OAuth dev 모드**: 실제 GitHub/Google 앱 크리덴셜 없이 개발할 mock 경로 필요 여부(무비용 원칙과 연계).
- **D-4 OAuth·비밀번호 계정 병합**: 동일 email 충돌 시 병합/거부 정책.
- **D-5 viewer 권한 세분화**: 문서가 endpoint별 viewer를 규정하지 않음(매트릭스 전부 member 이상). viewer=읽기전용 가정 확정 여부.
- **D-6 회원가입 시 기본 org 자동 생성 + owner 부여** 여부, 및 "org에 owner 최소 1명" 무결성 강제 지점.
- **D-7 API Key 권한 등급**: API Key가 어떤 역할/스코프로 매핑되는지(예: member 상당? org 전체?).
- **D-8 org 미소속 접근 응답**: 403 vs 404(존재 은닉) 정책.
- **D-9 조인 리소스 org_id 전략**: `load_test_results`/`scan_findings`/`report_shares` 등 자식 테이블에 org_id 비정규화 컬럼을 둘지, 부모 조인 기반 RLS 정책을 쓸지.
- **D-10 RLS 우회 경로**: 로그인 전 users 조회, `GET /shared/:slug`(`router.go:42`) public 조회, APM ingest(`router.go:40`, X-Ingest-Token)에서 RLS를 어떻게 처리(BYPASSRLS 롤 vs 별도 커넥션 vs 정책 예외).
- **D-11 RLS 세션 설정 메커니즘(pgxpool)**: 요청당 트랜잭션 `SET LOCAL app.current_org` vs 커넥션 acquire+set+reset. 성능/누출 트레이드오프.
- **D-12 users 부가 컬럼**: `name`/`updated_at`/`(oauth_provider,oauth_sub)` 유일 인덱스 필요 여부(문서 미명시).
- (참고) 잔여 전역 스택 결정(`00-tech-stack.md:119-125`): 로컬 오케스트레이션, 큐(NATS vs Kafka), Bedrock 모델 — Phase 1 직접 영향 낮으나 미확정.

---

## 모호 / 모순

- **M-1 문서-스키마 모순(org_id)**: `02-data-model.md`의 개별 테이블 정의(§2.3~2.6)는 `verified_domains`/`load_tests`/`scans`/`apm_*`/`reports` 등에 `project_id`만 명시하고 `org_id` 컬럼을 두지 않는다. 그러나 §3(`:272`)과 `CLAUDE.md` 불변식은 "org_id 가진 모든 테이블에 RLS"라 규정한다. → RLS를 project-only 테이블에 적용하려면 org_id 비정규화가 필요(D-9). 문서 기준(불변식 우선)으로 org_id 보강을 계약화했으나, 데이터 모델 문서도 함께 갱신되어야 링크 그래프 정합.
- **M-2 초대(invitation) 흐름 불명**: `POST /orgs/:orgId/members`(`03-api-spec.md:39`)는 "멤버 초대"지만, 미가입(user 미존재) 대상 초대를 위한 `invitations` 테이블/토큰·수락 흐름이 데이터 모델에 없다. 기존 user만 email로 즉시 membership 추가하는지, pending 초대를 지원하는지 불명 → 결정 필요(사실상 D-6과 연계).
- **M-3 org별 default project**: dev 시드는 org 1개 = project 1개지만, 신규 org 생성 시 default project 자동 생성 여부 문서에 없음.
- **M-4 SAML/`saml/acs`**: 문서에 public으로 존재(`03-api-spec.md:35`)하나 M3 표기 → Phase 1 제외로 처리. 상충 아님, 범위 처리로 기록.
