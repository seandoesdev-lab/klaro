-- Phase 1: dev 시드 정합 (TENANT-04, D-1)
-- 기존 0001 의 dev org(...0001)/project(...0002) 시드는 유지하고, 소유 user + owner 멤버십만 백필.
-- dev-token(D-1)은 이 user/org 로 매핑된다.
-- password_hash 는 pgcrypto bcrypt(cost 12) → Go x/crypto/bcrypt 와 호환($2a$).

INSERT INTO users (id, email, password_hash, name) VALUES
  ('00000000-0000-0000-0000-000000000003', 'dev@klaro.local',
   crypt('devpassword', gen_salt('bf', 12)), 'Dev User')
  ON CONFLICT (id) DO NOTHING;

INSERT INTO memberships (org_id, user_id, role) VALUES
  ('00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-000000000003', 'owner')
  ON CONFLICT (org_id, user_id) DO NOTHING;
