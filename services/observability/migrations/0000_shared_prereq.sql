-- 0000 선행 의존성(공유 스키마) — 설계 §3 "전제: orgs·projects·plans·users·audit_logs는 선행 의존성으로 존재 가정".
--
-- 상시 관측 서비스가 독립 DB(로컬 개발)로 뜰 때를 위한 멱등 부트스트랩이다.
-- 공유 DB(S1과 동일 인스턴스)에 적용하면 IF NOT EXISTS로 전부 no-op이 된다.
-- 여기에는 관측 도메인 테이블을 만들지 않는다 — 0001~0007이 설계 §3 그대로 담당한다.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- S1(services/load-test/migrations/0001_init.sql)의 실제 테이블명은 `organizations`다.
-- 설계 문서의 `orgs` 표기 대신 실제 코드 기준으로 정합화한다(설계 §0 원칙).
CREATE TABLE IF NOT EXISTS organizations (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email      text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- plans: 0007이 obs_* 보존/쿼터 컬럼을 ALTER로 덧붙인다(02-data-model.md §2.7).
CREATE TABLE IF NOT EXISTS plans (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code               text NOT NULL UNIQUE CHECK (code IN ('free','pro','enterprise')),
  name               text NOT NULL,
  max_vu             int  NOT NULL DEFAULT 50,
  max_duration_sec   int,
  monthly_test_limit int,
  scan_features      jsonb NOT NULL DEFAULT '{}',
  apm_retention_days int  NOT NULL DEFAULT 1,   -- MVP 스냅샷 호환(후속 deprecate)
  price_cents        int  NOT NULL DEFAULT 0
);

-- audit_logs: 관측 키 lifecycle·룰 변경 기록처(02-data-model.md §2.8, 설계 §3 말미).
CREATE TABLE IF NOT EXISTS audit_logs (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  action        text NOT NULL,
  resource_type text,
  resource_id   uuid,
  metadata      jsonb NOT NULL DEFAULT '{}',
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_logs_org ON audit_logs(org_id, created_at DESC);

-- audit_logs도 org 스코프이므로 동일 RLS 규약을 적용한다.
ALTER TABLE audit_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_logs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS audit_logs_isolation ON audit_logs;
CREATE POLICY audit_logs_isolation ON audit_logs
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);

-- 애플리케이션 롤: FORCE RLS는 superuser / BYPASSRLS 롤에는 적용되지 않는다.
-- 불변식("RLS 크로스테넌트 차단")이 실제로 성립하려면 앱이 반드시 이 non-superuser 롤로 접속해야 한다.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'klaro_obs_app') THEN
    -- 개발용 자격증명. 프로덕션은 시크릿 매니저에서 주입한 비밀번호로 별도 생성한다.
    CREATE ROLE klaro_obs_app LOGIN PASSWORD 'klaro_obs_app' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
  END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO klaro_obs_app;
GRANT SELECT ON organizations, users, plans TO klaro_obs_app;
GRANT SELECT, INSERT ON audit_logs TO klaro_obs_app;
