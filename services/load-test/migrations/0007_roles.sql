-- Phase 1: DB 롤 (RLS 실효화의 핵심, 아키텍처 설계 §2.5)
-- 슈퍼유저(klaro)는 RLS를 항상 우회하므로, 앱은 비-슈퍼유저 klaro_app 롤로 접속해야 RLS가 실효한다.
--   klaro_app    : LOGIN, NOSUPERUSER, NOBYPASSRLS → 모든 org 스코프 tx는 app.current_org 종속 (기본 접속 롤)
--   klaro_system : LOGIN, BYPASSRLS → 인증 전/공유/ingest 부트스트랩 전용 (최소 사용)
-- 비밀번호는 개발 편의상 고정. 프로덕션은 .env(SOPS/age)로 주입.

DO $$ BEGIN
  CREATE ROLE klaro_app LOGIN PASSWORD 'klaro_app' NOSUPERUSER NOBYPASSRLS;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
  CREATE ROLE klaro_system LOGIN PASSWORD 'klaro_system' NOSUPERUSER BYPASSRLS;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

GRANT USAGE ON SCHEMA public TO klaro_app, klaro_system;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO klaro_app, klaro_system;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO klaro_app, klaro_system;

-- 향후 생성 테이블/시퀀스에도 동일 권한 자동 부여 (klaro 소유 객체 기준)
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO klaro_app, klaro_system;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO klaro_app, klaro_system;
