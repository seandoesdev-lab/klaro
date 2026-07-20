# klaro Phase 1 — 아키텍처·구현 설계: 인증 + PostgreSQL RLS 멀티테넌시

**작성** architect · **작성일** 2026-07-20 · **상태** 빌더 착수용(확정)
**입력** `_workspace/01_spec-analyst_contract.md`, `docs/klaro/00~03`, `services/load-test/**`
**확정 스택** Go 유지 · 공용 DB + RLS · Docker Compose · NATS 큐 승격은 범위 밖(Phase 1 = 인증/RLS만) · Bedrock 무관
**대상 모듈** 단일 Go 모듈 `services/load-test`(module path `github.com/klaro/load-test`)에 인증/인가/테넌시 계층을 증축한다. Phase 1은 별도 서비스 분리 없음.

> backend-builder는 이 문서만으로 착수할 수 있다. 결정(D-1~D-12)은 전부 확정했고, 되물을 항목 없음.

---

## 0. 설계 요약 (한 문단)

기존 `authStub`(고정 dev-token→고정 project) 미들웨어 체인을 **4단 체인**(authenticate → resolveOrg → authorize → tenancyTx)으로 교체한다. 인증은 무상태 **JWT Access + Redis 저장/회전 Refresh**, CI용 **API Key**를 같은 미들웨어에서 분기한다. 테넌시는 **공용 DB + RLS**로 강제하되, 앱은 `BYPASSRLS`가 없는 **`klaro_app` 롤**로 접속하고 요청마다 **트랜잭션 + `set_config('app.current_org', <uuid>, true)`**(`SET LOCAL` 등가)로 스코프를 건다. 인증 전/공유/ingest 부트스트랩 경로는 `BYPASSRLS`를 가진 **`klaro_system` 롤 전용 풀**로 처리한다. `store`는 공유 pool 직접 사용에서 **`Querier`(pgx.Tx | pool) 주입** 패턴으로 바꿔 org 스코프 tx를 관통시킨다. 자식 테이블에는 **`org_id` 비정규화 컬럼**을 두어 RLS 정책을 평면 등식(`org_id = current_org`)으로 단순화한다.

---

## 1. 스택 결정 (D-1 ~ D-12 전부 확정)

| ID | 결정 | 근거 (한 줄) | 기각한 대안 |
|----|------|--------------|-------------|
| **D-1** dev-token | **유지**. dev-token은 시드된 dev user(`...0003`)+dev-org(`...0001`) owner 멤버십에 매핑. `X-Org-Id` 없으면 dev-org로 기본. **고정 project 주입 폐기**. `DEV_TOKEN`은 `APP_ENV=dev`에서만 활성. | 완전 제거 → 기존 통합테스트·프로토타입 단일테넌트 흐름 파괴. |
| **D-2** Refresh 전략 | Access=**무상태 JWT(HS256, 15분)**. Refresh=**불투명 랜덤(32B) 토큰**, Redis에 `sha256(token)`만 저장(`rt:{jti}` → user_id, family_id, TTL 14d). 갱신 시 **회전**(신규 발급+구 폐기), **재사용 감지**(폐기된 jti 재사용 시 family 전체 무효화), 로그아웃 시 family 폐기. | 무상태 Refresh(JWT) → 폐기·회전 불가(AUTH-03 재사용 차단 위반). |
| **D-3** OAuth dev 모드 | `golang.org/x/oauth2` 사용. provider 크리덴셜 env(`GITHUB_CLIENT_ID` 등) **미설정 시 dev mock 콜백**(`?mock_email=&mock_sub=`)으로 결정적 프로파일 발급. | 실제 앱 크리덴셜 필수화 → 오프라인·무비용 개발 원칙([COST-05]) 위반. |
| **D-4** OAuth·비번 계정 병합 | **자동 링크**. 동일 `email`이 이미 있으면 그 user에 `oauth_provider/oauth_sub` 결합(provider가 email 소유를 보증하므로 안전). password_hash는 보존. | 하드 거부 → UX 저하. 중복 user 생성 → 테넌시 파편화. |
| **D-5** viewer 권한 | **읽기 전용 확정**. `viewer`는 GET만, 쓰기(POST/PATCH/DELETE) 403. RBAC-02 매트릭스의 "member 이상"은 viewer 배제. | 세분 권한 테이블 → Phase 1 근거 부재·과설계. |
| **D-6** 가입 시 org + owner 무결성 | 회원가입 시 **개인 org 자동 생성 + owner 멤버십 + default project(M-3 해소)** 원자 생성. **"org당 owner ≥ 1" 불변식**: 마지막 owner 강등/제거 시 `409 CONFLICT`(rbac 계층에서 검사). 초대(M-2)는 **기존 user만 email로 즉시 membership 추가**; pending `invitations` 테이블은 Phase 2 연기. | org 없는 가입 → 이후 모든 org 스코프 API 사용 불가. |
| **D-7** API Key 권한 | **발급 org 고정 스코프 + 역할 `member`**(기본). `api_keys.role` 컬럼 추가(기본 `member`)로 확장 여지. API Key는 admin/owner 전용(멤버·키·구독 관리) 접근 불가 → AUTH-05 "구분 가능" 충족. | owner급 키 → 과권한 위험. 완전 configurable → 스코프 확대. |
| **D-8** org 미소속 응답 | **404**(org 존재 은닉). 멤버십 없음 → 404. 멤버십은 있으나 역할 미달 → **403**. RBAC-03의 404 허용을 채택. | 일괄 403 → org 존재 유무 노출. |
| **D-9** 조인 리소스 org_id | **자식 테이블에 `org_id` 비정규화 컬럼 추가**(`load_test_results`/`scan_findings`/`apm_spans`/`apm_logs`/`report_shares`). RLS 정책은 평면 등식. | 부모 조인 정책(USING 서브쿼리) → 느리고 실수 유발, 인덱스 활용 저하. |
| **D-10** RLS 우회 경로 | **`klaro_system`(BYPASSRLS) 전용 커넥션 풀**로 부트스트랩 처리: (a) 로그인/회원가입 users 조회, (b) 멤버십 조회(스코프 확정 전), (c) `GET /shared/:slug` 공유 리포트, (d) APM ingest 토큰→org 해석. ingest는 org 해석 후 **app 풀로 전환**해 org 스코프로 write. | 정책 예외(`WITH CHECK true`) → 누출·감사 곤란. |
| **D-11** 세션 설정 메커니즘 | **요청당 트랜잭션 + `SELECT set_config('app.current_org',$1,true)`**(=`SET LOCAL`, 파라미터 바인딩 가능). 커밋/롤백 시 자동 소멸 → 풀 재사용 누출 원천 차단. | acquire+SET+reset → reset 누락 시 크로스테넌트 누출. |
| **D-12** users 부가 컬럼 | `name text NULL`, `updated_at timestamptz`, **부분 유니크 인덱스 `(oauth_provider, oauth_sub) WHERE oauth_provider IS NOT NULL`** 추가. | 미추가 → OAuth 정체성 유일성·프로파일 표시 불가. |

**전역 잔여 결정(Phase 1 영향 낮음, 참고)**: 로컬 오케스트레이션=Docker Compose 유지, 큐=NATS(승격 범위 밖, Phase 1은 기존 Redis 큐 유지), Bedrock 모델=미해당.

### 1.1 인증 라이브러리 (전부 OSS·무비용, [COST-05] 준수)
- JWT: `github.com/golang-jwt/jwt/v5`
- 비밀번호 해시: `golang.org/x/crypto/bcrypt` (cost 12)
- OAuth2: `golang.org/x/oauth2` (+ `/github`, `/google` endpoints)
- API Key 해시: `crypto/sha256`(키는 고엔트로피 랜덤이라 bcrypt 불필요), 상수시간 비교
- Redis: 기존 `internal/queue/redis.go`의 클라이언트 재사용(신규 유료 의존 0)

---

## 2. 데이터 모델·마이그레이션 설계

### 2.1 마이그레이션 파일 계획 (신규 `0004`~`0008`, 순서 = 의존순)

기존 러너는 `migrations/*.sql`을 **파일명 사전순**으로 적용한다. 마이그레이션은 소유자 롤(`klaro`, 슈퍼유저)로 실행되므로 백필·시드는 RLS를 우회(슈퍼유저는 항상 bypass)해 정상 동작한다.

| 파일 | 목적 | 롤백 고려 |
|------|------|-----------|
| `0004_auth.sql` | `citext` 확장, `users`/`memberships`/`api_keys` 신설 + 인덱스 | DROP TABLE 역순. FK 없음(참조만 함) |
| `0005_org_id_backfill.sql` | org 스코프 자식 테이블에 `org_id` 컬럼 추가(nullable→백필→NOT NULL→FK+인덱스) | 컬럼 DROP. 백필은 조인 UPDATE라 재실행 안전(idempotent 아님 주의) |
| `0006_rls.sql` | 전 org 스코프 테이블 `ENABLE`+`FORCE RLS` + `org_isolation` 정책 | `DROP POLICY`/`DISABLE RLS` |
| `0007_roles.sql` | `klaro_app`(비-BYPASS)·`klaro_system`(BYPASSRLS) 롤 생성 + GRANT + DEFAULT PRIVILEGES | 롤 REVOKE/DROP. 클러스터 레벨(1회) |
| `0008_dev_seed.sql` | dev user(`...0003`) + dev-org owner 멤버십 백필(`ON CONFLICT DO NOTHING`) | DELETE by 고정 id |

> **적용 순서 근거**: 테이블 신설(0004) → 컬럼/백필(0005) → 정책(0006) → 롤·권한(0007, ALL TABLES 대상이라 신규 테이블 포함) → 시드(0008). 정책을 시드보다 앞에 둬도 시드가 슈퍼유저 실행이라 무해.

### 2.2 신규 테이블 DDL 스케치 (`0004_auth.sql`)

```sql
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email          citext UNIQUE NOT NULL,
  password_hash  text,                       -- OAuth 전용 시 NULL (AUTH-04)
  oauth_provider text,                        -- 'github' | 'google'
  oauth_sub      text,
  name           text,                        -- D-12
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);
-- D-12: OAuth 정체성 유일성 (비-OAuth 계정은 제외)
CREATE UNIQUE INDEX uq_users_oauth ON users(oauth_provider, oauth_sub)
  WHERE oauth_provider IS NOT NULL;

CREATE TABLE memberships (                    -- DATA-02
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('owner','admin','member','viewer')),  -- RBAC-01
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, user_id)
);
CREATE INDEX idx_memberships_user ON memberships(user_id);  -- 내 org 목록
CREATE INDEX idx_memberships_org  ON memberships(org_id);

CREATE TABLE api_keys (                       -- DATA-03
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  key_hash     text NOT NULL,                 -- sha256(hex). 원문 미저장(AUTH-05)
  name         text NOT NULL,
  role         text NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member','viewer')),  -- D-7
  last_used_at timestamptz,
  expires_at   timestamptz,
  revoked_at   timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_api_keys_hash ON api_keys(key_hash);  -- 인증 조회 경로
```

> `users`는 전역(테넌트 비귀속) → **RLS 비대상**. `memberships`/`api_keys`는 `org_id` 보유 → RLS 대상.

### 2.3 org_id 보강 + 백필 (`0005_org_id_backfill.sql`)

대상 테이블별 백필 출처(D-9: 전부 비정규화 컬럼):

| 테이블 | org_id 출처(백필 조인) |
|--------|------------------------|
| `verified_domains` | `projects` via `project_id` |
| `load_tests` | `projects` via `project_id` |
| `load_test_results` | `load_tests` via `load_test_id` |
| `scans` | `projects` via `project_id` |
| `scan_findings` | `scans` via `scan_id` |
| `apm_agents` | `projects` via `project_id` |
| `apm_spans` | `projects` via `project_id` |
| `apm_logs` | `projects` via `project_id` |
| `reports` | `projects` via `project_id` |
| `report_shares` | `reports` via `report_id` |

패턴(각 테이블 반복):

```sql
ALTER TABLE verified_domains ADD COLUMN org_id uuid;
UPDATE verified_domains vd SET org_id = p.org_id
  FROM projects p WHERE p.id = vd.project_id;
ALTER TABLE verified_domains ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE verified_domains ADD CONSTRAINT fk_vd_org
  FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX idx_vd_org ON verified_domains(org_id);
-- 자식(조인) 예:
ALTER TABLE load_test_results ADD COLUMN org_id uuid;
UPDATE load_test_results r SET org_id = lt.org_id
  FROM load_tests lt WHERE lt.id = r.load_test_id;
-- ...NOT NULL / FK / INDEX 동일
```

> `organizations`·`projects`는 기존 스코프 키 보유(`id`·`org_id`)라 컬럼 추가 불필요. `memberships`/`api_keys`는 0004에서 이미 `org_id` 보유.

### 2.4 RLS 정책 (`0006_rls.sql`) — TENANT-02

정책 대상: `organizations`, `projects`, `memberships`, `api_keys`, `verified_domains`, `load_tests`, `load_test_results`, `scans`, `scan_findings`, `apm_agents`, `apm_spans`, `apm_logs`, `reports`, `report_shares`.

```sql
-- org_id 보유 테이블 공통 패턴
ALTER TABLE load_tests ENABLE ROW LEVEL SECURITY;
ALTER TABLE load_tests FORCE ROW LEVEL SECURITY;          -- 소유자도 우회 금지
CREATE POLICY org_isolation ON load_tests
  USING      (org_id = current_setting('app.current_org', true)::uuid)
  WITH CHECK (org_id = current_setting('app.current_org', true)::uuid);

-- organizations: 스코프 키가 id
ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE organizations FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON organizations
  USING      (id = current_setting('app.current_org', true)::uuid)
  WITH CHECK (id = current_setting('app.current_org', true)::uuid);
```

- `current_setting('app.current_org', true)`의 2번째 인자 `true` = **미설정 시 예외 대신 NULL 반환** → `org_id = NULL`은 false → **세션 미설정 상태 SELECT 0건**(TENANT-02 수용 기준, 정보 누출 방지).
- `WITH CHECK` 동일 조건 → 다른 org로의 INSERT/UPDATE 차단.
- `organizations` INSERT(org 생성)는 아직 org 스코프가 없는 시점이므로 **`klaro_system` 풀로 수행**(D-10). 생성 직후 owner 멤버십도 system 풀로 원자 삽입.

### 2.5 DB 롤 (`0007_roles.sql`) — RLS 실효화의 핵심

> **중요**: PostgreSQL 슈퍼유저는 RLS를 **항상 우회**한다. 현재 앱은 `POSTGRES_USER=klaro`(슈퍼유저)로 접속하므로 정책을 켜도 무력화된다. 따라서 앱은 **비-슈퍼유저 롤 `klaro_app`**로 접속해야 RLS가 실효한다.

```sql
DO $$ BEGIN
  CREATE ROLE klaro_app    LOGIN PASSWORD 'klaro_app';               -- NOSUPERUSER, NO BYPASSRLS
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
  CREATE ROLE klaro_system LOGIN PASSWORD 'klaro_system' BYPASSRLS;  -- 부트스트랩 전용
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

GRANT USAGE ON SCHEMA public TO klaro_app, klaro_system;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO klaro_app, klaro_system;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO klaro_app, klaro_system;
```

- `klaro_app`: **BYPASSRLS 없음** → 모든 org 스코프 tx는 `app.current_org`에 종속. 앱 기본 접속 롤.
- `klaro_system`: **BYPASSRLS** → 인증 전/공유/ingest 부트스트랩(D-10) 전용. 최소 사용.
- 비밀번호는 `.env`(SOPS/age)로 주입, docker-compose에서 `DATABASE_URL`/`SYSTEM_DATABASE_URL` 구성.

### 2.6 dev 시드 (`0008_dev_seed.sql`) — TENANT-04

```sql
INSERT INTO users (id, email, password_hash, name) VALUES
  ('00000000-0000-0000-0000-000000000003', 'dev@klaro.local',
   crypt('devpassword', gen_salt('bf', 12)), 'Dev User')   -- pgcrypto bcrypt
  ON CONFLICT (id) DO NOTHING;
INSERT INTO memberships (org_id, user_id, role) VALUES
  ('00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-000000000003', 'owner')
  ON CONFLICT (org_id, user_id) DO NOTHING;
```

> 기존 `0001`의 dev org(`...0001`)/project(`...0002`) 시드는 그대로 두고, user+owner 멤버십만 백필. dev-token(D-1)은 이 user/org로 매핑.

### 2.7 문서 갱신 대상 (M-1 해소 — backend-builder 또는 후속 doc 패스가 반영)

`docs/klaro/02-data-model.md`를 실제 스키마와 일치시킨다(링크 그래프 정합):
- §2.3~2.6 각 테이블 정의에 **`org_id uuid FK→organizations`** 컬럼 행 추가(`verified_domains`, `load_tests`, `load_test_results`, `scans`, `scan_findings`, `apm_agents`, `apm_spans`, `apm_logs`, `reports`, `report_shares`).
- §2.1 `users`에 `name`/`updated_at`, `api_keys`에 `role` 행 추가.
- §3에 "RLS는 `klaro_app` 롤 + `set_config('app.current_org')` 트랜잭션 스코프로 강제하며, `klaro_system`(BYPASSRLS)은 인증 전/공유/ingest 부트스트랩 전용"이라는 운영 규칙 문장 추가.

---

## 3. 모듈 경계 (Go 패키지 구조)

```
services/load-test/internal/
  auth/                     # 신규 — 인증 원자
    password.go             #   bcrypt 해시/검증
    jwt.go                  #   Access 토큰 발급/검증(HS256), Claims{sub,exp,iat}
    refresh.go              #   Redis 저장/회전/재사용감지/폐기 (D-2)
    oauth.go                #   provider 설정 + dev mock 콜백 (D-3, D-4 링크)
    apikey.go               #   키 생성(원문 1회)/sha256 해시/검증
  rbac/                     # 신규 — 인가
    role.go                 #   Role 타입, 서열 rank(owner>admin>member>viewer)
    matrix.go               #   RequireRole(min) 판정, viewer=읽기전용(D-5)
    invariant.go            #   마지막 owner 강등/제거 방지(D-6) → ErrLastOwner
  tenancy/                  # 신규 — org 컨텍스트 + 세션 스코프 실행자
    context.go              #   gin.Context 키 상수, Principal{UserID,OrgID,Role,AuthType}
    executor.go             #   Tx(c) 접근자, RunInOrg(ctx,orgID,fn) (D-11)
  api/
    middleware.go           # 개조 — authStub 제거, 4단 체인 신설
    router.go               # 개조 — 그룹 재구성 + 신규 auth/org/member 라우트
    auth_handlers.go        # 신규 — signup/login/refresh/logout/oauth
    org_handlers.go         # 신규 — orgs CRUD + members + api-keys
    project_handlers.go     # 신규 — projects CRUD (기존 인라인 없음 → 신설)
    (loadtests|domains|scans|apm|reports).go  # 개조 — projectID(c)→org tx 전파
  store/
    store.go                # 개조 — 2풀(app/sys) + Querier 주입 + RunInOrg
    auth_store.go           # 신규 — users/memberships/api_keys CRUD (sys 풀 경유 다수)
    (apm|scans|reports)_store.go  # 개조 — Querier 파라미터화 + org_id 삽입
```

**책임 경계 원칙**: `auth`는 크리덴셜↔토큰 변환만(DB 무지, store 주입). `rbac`는 순수 판정 로직(DB 무지). `tenancy`는 org 스코프 실행자(SQL 방언 격리). `api`는 HTTP↔도메인 변환. `store`는 SQL만. 순환 의존 없음(`api → auth/rbac/tenancy/store`, 나머지는 서로 독립).

---

## 4. 미들웨어 체인 설계 (4단)

라우트 그룹별로 필요한 단계만 조합한다. 각 단계는 `gin.Context`에 키를 주입/소비한다.

```
[public]        (없음)                                    — /auth/*, /shared/:slug, /healthz
[apm-ingest]    authenticateIngest → tenancyTx            — /projects/:id/apm/ingest
[authenticated] authenticate → resolveOrg → authorize(min) → tenancyTx
```

### 4.1 단계별 명세

| 단계 | 입력 | 처리 | 출력(ctx 키) | 에러 |
|------|------|------|--------------|------|
| **authenticate** | `Authorization: Bearer <t>` | JWT면 서명·만료 검증 → sub. API Key면(접두 `klaro_`) sha256→`api_keys` 조회(sys 풀), expires/revoked 확인, `last_used_at` 갱신. dev-token(D-1)이면 dev user. | `principal.user_id`, `principal.auth_type`(jwt\|apikey\|dev), API Key는 `principal.org_id`+`principal.role` 선주입 | 없음/무효/만료 → **401 UNAUTHENTICATED** |
| **resolveOrg** | 경로 `:orgId` 또는 헤더 `X-Org-Id`; API Key는 선주입 org 사용 | JWT 유저: `org_id` 확정 후 `memberships(user_id,org_id)` 조회(sys 풀, 스코프 확정 전이므로 BYPASSRLS 필수). API Key: 경로 `:orgId` 있으면 선주입 org와 일치 검증. | `principal.org_id`, `principal.role` | 멤버십 없음/org 없음 → **404 NOT_FOUND**(D-8, 은닉). `X-Org-Id`·`:orgId` 부재(리소스 라우트) → **400 VALIDATION_ERROR** |
| **authorize(min)** | `principal.role`, 라우트 최소 역할 | `rbac.Rank(role) >= rank(min)`. viewer는 쓰기 라우트 자동 거부(D-5). | (통과 시 무변경) | 역할 미달 → **403 FORBIDDEN** |
| **tenancyTx** | `principal.org_id` | app 풀 `Begin()` → `SELECT set_config('app.current_org',$1,true)` → `c.Set(tx)` → `c.Next()`. defer: `c.IsAborted()` 또는 status≥400 또는 `len(c.Errors)>0` → `Rollback`, else `Commit`. (D-11) | `ctx["tx"]` = `pgx.Tx`(org 스코프) | 커밋 실패 → 500 INTERNAL |

### 4.2 authorize 최소 역할 매핑 (RBAC-02 → 라우트)

`router.go`에서 라우트별 `authorize(min)` 부착:

| 라우트 | min |
|--------|-----|
| `POST /orgs` | user(=인증만, authorize 생략) |
| `GET /orgs`, `GET /orgs/:orgId/members`, `GET/POST/GET/PATCH /projects*`, 리소스 GET/생성(load-tests/scans/domains/apm/reports) | member |
| `POST /orgs/:orgId/members`, `PATCH/DELETE .../members/:userId`, `GET/POST/DELETE .../api-keys*`, `DELETE /projects/:id` | admin |
| `POST /orgs/:orgId/subscription`(참고, 미구현) | owner |

> `viewer`(D-5)는 member 미만이므로 위 member 라우트의 쓰기(POST/PATCH/DELETE)에서 거부되도록, member 라우트를 **읽기(GET)=viewer 허용 / 쓰기=member 이상**으로 분리 부착한다. 즉 GET 계열은 `authorize(viewer)`, 변경 계열은 `authorize(member)`.

### 4.3 apm-ingest 특례 (D-10)
`authenticateIngest`: `X-Ingest-Token`을 sys 풀로 `apm_agents` 조회 → `project.org_id` 해석 → `principal.org_id` 주입 후 **tenancyTx**(app 풀)로 전환해 org 스코프에서 span/log write. 미들웨어 체인은 `authenticate`(JWT/키) 대신 ingest 인증만 사용.

---

## 5. API 표면 (Phase 1 신규 엔드포인트)

`03-api-spec.md`와 일치. Base `/v1`. 에러 포맷은 기존 `errors.go` 유지.

### 5.1 인증 (public)
| 메서드·경로 | 요청 | 응답 | 비고 |
|-------------|------|------|------|
| `POST /v1/auth/signup` | `{email, password, name?}` | `201 {user_id}` | 개인 org+owner+default project 원자 생성(D-6). 중복 email → **409 CONFLICT** |
| `POST /v1/auth/login` | `{email, password}` | `200 {access_token, refresh_token, expires_in}` | 실패 → **401**(계정 은닉) |
| `POST /v1/auth/refresh` | `{refresh_token}` | `200 {access_token, refresh_token, expires_in}` | 회전. 재사용/무효 → **401**(D-2) |
| `POST /v1/auth/logout` | `{refresh_token}` | `204` | family 폐기(D-2) |
| `GET /v1/auth/oauth/:provider` | (redirect) | `302` → provider 또는 mock | `provider∈{github,google}`(D-3) |
| `GET /v1/auth/oauth/:provider/callback` | `?code=` 또는 `?mock_email=&mock_sub=` | `200 {access_token, refresh_token, expires_in}` | 병합 정책 D-4 |

### 5.2 조직·멤버·키 (authenticated)
| 메서드·경로 | min | 요청/응답 |
|-------------|-----|-----------|
| `GET /v1/orgs` | member | `{data:[{id,name,role}]}` (내 멤버십 기준; sys 풀로 조회) |
| `POST /v1/orgs` | user | `{name}` → `201 {id}` (+owner 멤버십, sys 풀) |
| `GET /v1/orgs/:orgId/members` | member | `{data:[{user_id,email,role}]}` |
| `POST /v1/orgs/:orgId/members` | admin | `{email, role}` → `201`(기존 user 즉시 멤버십; 미가입 → 404, M-2) |
| `PATCH /v1/orgs/:orgId/members/:userId` | admin | `{role}` → `200`(마지막 owner 강등 → 409, D-6) |
| `DELETE /v1/orgs/:orgId/members/:userId` | admin | `204`(마지막 owner 제거 → 409) |
| `GET /v1/orgs/:orgId/api-keys` | admin | `{data:[{id,name,role,last_used_at,...}]}` (원문·해시 미노출) |
| `POST /v1/orgs/:orgId/api-keys` | admin | `{name, role?, expires_at?}` → `201 {id, key}`(원문 1회 노출, AUTH-05) |
| `DELETE /v1/orgs/:orgId/api-keys/:id` | admin | `204`(`revoked_at` set) |

### 5.3 프로젝트 (authenticated)
| 메서드·경로 | min | 비고 |
|-------------|-----|------|
| `GET /v1/projects` | member(GET=viewer 허용) | 활성 org RLS로 자동 스코프 |
| `POST /v1/projects` | member | `{name}` → `201 {id}` |
| `GET /v1/projects/:id` | viewer | RLS 0건 → 404 |
| `PATCH /v1/projects/:id` | member | |
| `DELETE /v1/projects/:id` | admin | |

> 기존 도메인/부하/스캔/APM/리포트 라우트(§router.go)는 **경로 유지**하되 인증 그룹이 4단 체인으로 교체되고 `X-Org-Id` 필수가 된다(리소스 단독 라우트 `/load-tests/:id` 등). 권한은 GET=viewer, 변경=member 부착.

---

## 6. store 계층 org 컨텍스트 전파

### 6.1 2풀 구조 + Querier 주입

```go
// store.go
type Querier interface {          // *pgxpool.Pool 과 pgx.Tx 둘 다 구현
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
    app *pgxpool.Pool   // klaro_app  : RLS 강제
    sys *pgxpool.Pool   // klaro_system: BYPASSRLS(부트스트랩 전용)
}

func New(ctx context.Context, appDSN, sysDSN string) (*Store, error) { ... }

// 워커/비-HTTP 경로용 org 스코프 실행 (D-11)
func (s *Store) RunInOrg(ctx context.Context, orgID string, fn func(pgx.Tx) error) error {
    tx, err := s.app.Begin(ctx); if err != nil { return err }
    if _, err = tx.Exec(ctx, `SELECT set_config('app.current_org',$1,true)`, orgID); err != nil {
        tx.Rollback(ctx); return err
    }
    if err = fn(tx); err != nil { tx.Rollback(ctx); return err }
    return tx.Commit(ctx)
}

// 부트스트랩(BYPASSRLS) 접근자
func (s *Store) Sys() Querier { return s.sys }
```

### 6.2 기존 메서드 시그니처 개조 패턴

`s.pool.QueryRow(...)` 직접 호출 → **`q Querier` 파라미터 주입**으로 전환. `project_id` 필터는 유지하되(추가 방어), 크로스테넌트 차단의 1차 방어선은 RLS가 담당.

```go
// AS-IS
func (s *Store) GetLoadTest(ctx context.Context, id string) (*model.LoadTest, error) {
    err := s.pool.QueryRow(ctx, `... WHERE id=$1`, id)...
}
// TO-BE
func (s *Store) GetLoadTest(ctx context.Context, q Querier, id string) (*model.LoadTest, error) {
    err := q.QueryRow(ctx, `... WHERE id=$1`, id)...   // RLS가 org 밖 행을 0건 처리 → ErrNotFound
}
```

- **INSERT는 `org_id` 명시**: `CreateLoadTest`/`SaveResult`/`CreateDomain`/`CreateScan`/`SaveFindings`/`InsertSpans`/`InsertLogs`/`CreateReport`/`CreateShare` 등은 `org_id` 컬럼을 INSERT 목록에 추가하고, 값은 principal.org_id(핸들러) 또는 job.OrgID(워커)에서 받는다. (RLS `WITH CHECK`가 불일치 INSERT를 거부하므로 값 정확성 강제됨.)
- **모든 §3의 34개 store 메서드**가 `q Querier`를 받도록 변경(기계적). 자식 INSERT는 부모 org_id를 인자로 추가.
- **핸들러**: `d.Store.GetLoadTest(c, tenancy.Tx(c), id)` 형태로 org 스코프 tx 주입. `projectID(c)` 헬퍼는 `tenancy.OrgID(c)`/`tenancy.ProjectID(c)`로 대체.

### 6.3 부트스트랩 store (sys 풀)
`auth_store.go`의 `GetUserByEmail`, `GetMembership(userID,orgID)`, `ListOrgsByUser`, `CreateOrgWithOwner`(org+membership+default project 원자), `GetShareBySlug`, ingest용 `ResolveAgentOrg`는 `s.sys`(BYPASSRLS)로 실행. 그 외 org 스코프 조회는 전부 app tx.

### 6.4 워커 경로 (RLS 관통)

`worker.Deps.process`는 gin 밖이므로 `RunInOrg`로 감싼다. `model.Job`에 **`OrgID string`** 필드 추가(`createLoadTest`가 principal.org_id로 채움). 각 DB write를 짧은 org tx로:

```go
// 예: 상태 갱신
_ = d.Store.RunInOrg(ctx, job.OrgID, func(tx pgx.Tx) error {
    return d.Store.UpdateStatus(ctx, tx, id, model.StatusRunning, nil)
})
```

> 스트리밍 메트릭 publish는 DB 미접촉(Redis)이라 무영향. 잡당 DB write는 소수(상태 전이 + 결과 저장)라 tx 오버헤드 무시 가능. `model.LoadTest`에도 `OrgID string`(json `"-"`) 추가.

### 6.5 기존 테스트 회귀 영향

| 테스트 | 영향 | 조치 |
|--------|------|------|
| `store/store_test.go`(`//go:build integration`) | `New` 시그니처 변경, 메서드에 `q` 추가 | `New(ctx, appDSN, sysDSN)` 호출로 수정. `s.RunInOrg(ctx, devOrg, func(tx){...})`로 감싸 org 스코프 검증. **신규**: 미설정 tx에서 조회 0건, 크로스 org 차단 케이스 추가 |
| `store/host_test.go` | `HostFromURL` 순수함수, 무영향 | 없음 |
| `api/errors_test.go`, `api/ws_test.go` | 미들웨어/store 무관 | 없음 |
| `model/*`, `breaker/*`, `scenario/*`, `domainverify/*`, `queue/*` | 무관 | 없음 |
| `worker/aggregate_test.go`, `scan_analysis_test.go` | 집계 로직 순수, store 미접촉이면 무영향 | store 호출 있으면 `RunInOrg` 목/스킵 |

> 대부분 유닛 테스트는 무영향. 영향은 `integration` 태그 store 테스트에 집중되며, 오히려 RLS 격리 검증 테스트를 **추가**해야 한다(TENANT-03 수용 기준).

---

## 7. 빌드 순서 (backend-builder용, 의존순)

각 단계는 이전 단계 산출에 의존한다. 단계 종료 시 `go build ./...` + 관련 테스트 그린을 게이트로 삼는다.

1. **마이그레이션(0004~0008)** — DDL·백필·RLS·롤·시드. `docker compose` 재기동 후 psql로 정책/롤 검증(`SET ROLE klaro_app; SELECT ...` 0건 확인). 산출: 스키마.
2. **`tenancy` 패키지** — `context.go`(키·Principal), `executor.go`(`RunInOrg`, `Tx(c)`). store 2풀 도입(`store.New(appDSN,sysDSN)`, `Querier`, `RunInOrg`, `Sys()`). `main.go`(api/worker) DSN 2개 배선. 산출: org 스코프 실행 인프라.
3. **`auth` 패키지** — password/jwt/refresh(Redis)/apikey/oauth(dev mock). 순수 유닛테스트 우선. 산출: 토큰·크리덴셜 로직.
4. **`rbac` 패키지** — role/matrix/invariant. 순수 유닛테스트. 산출: 인가 판정.
5. **미들웨어 교체** — `authStub`/`projectID(c)` 제거, 4단 체인(authenticate/resolveOrg/authorize/tenancyTx) + ingest 특례. `router.go` 그룹 재구성 + `authorize(min)` 부착. 산출: 인증/인가 경로.
6. **기존 store/handler org화** — §3 메서드 `Querier` 파라미터화 + INSERT `org_id` + 핸들러 `tenancy.Tx(c)` 주입. `model.Job`/`model.LoadTest`에 `OrgID`. worker `RunInOrg` 래핑. 산출: 기존 기능의 RLS 관통.
7. **신규 auth/org/member/project API** — `auth_handlers.go`/`org_handlers.go`/`project_handlers.go` + `auth_store.go`(sys 풀). signup 원자 생성(org+owner+default project). 산출: Phase 1 API 표면.
8. **테스트·시드·검증** — store RLS 격리 integration 테스트 추가, auth/rbac 유닛, e2e(signup→login→org 스코프 CRUD→크로스 org 404). dev-token→dev user/org 매핑 검증. 불변식 체크리스트(계약 §불변식) 통과 확인.

---

## 8. 불변식 준수 매핑 (계약 §불변식 체크리스트)

| 불변식 | 설계상 보장 지점 |
|--------|------------------|
| RLS 멀티테넌시(세션 미설정 0건) | 2.4 정책 `current_setting(...,true)` NULL 처리 + 2.5 `klaro_app` 비-슈퍼유저 |
| 테넌트 크로스 차단(IDOR) | 6.2 id-only 조회가 RLS org 스코프 tx에서 실행 → org 밖 0건 → 404(RBAC-04) |
| dev-token 스텁 제거 | 4.1 authenticate가 고정 project 주입 폐기, dev user/org 매핑(D-1) |
| 최소 권한 403/401 | 4.1~4.2 authorize 단계 + 매트릭스 |
| 비밀정보 비노출 | 2.2 password_hash/key_hash만 저장, 5.1/5.2 응답 미포함, 로그인 실패 은닉(401) |
| 비용 정책([COST-05]) | 1.1 전부 OSS(golang-jwt/x-crypto/x-oauth2/기존 Redis), 신규 유료 0 |
| 도메인 게이트·서킷·Ephemeral·mTLS·시계열 분리 | Phase 1 직접 대상 아님. 기존 유지, 위반 도입 없음(RLS·인증 증축만) |
