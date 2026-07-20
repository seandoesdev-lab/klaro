-- Phase 1: 인증/멀티테넌시 기반 테이블 (users / memberships / api_keys)
-- 근거: 02-data-model §2.1~2.2, 아키텍처 설계 §2.2, 계약 DATA-01~03
CREATE EXTENSION IF NOT EXISTS citext;

-- users: 전역(테넌트 비귀속) → RLS 비대상
CREATE TABLE users (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email          citext UNIQUE NOT NULL,
  password_hash  text,                       -- OAuth 전용 계정은 NULL (AUTH-04)
  oauth_provider text,                        -- 'github' | 'google'
  oauth_sub      text,
  name           text,                        -- D-12
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);
-- D-12: OAuth 정체성 유일성 (비-OAuth 계정은 인덱스에서 제외)
CREATE UNIQUE INDEX uq_users_oauth ON users(oauth_provider, oauth_sub)
  WHERE oauth_provider IS NOT NULL;

-- memberships: org_id 보유 → RLS 대상 (DATA-02)
CREATE TABLE memberships (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('owner','admin','member','viewer')),  -- RBAC-01
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, user_id)
);
CREATE INDEX idx_memberships_user ON memberships(user_id);  -- 내 org 목록 조회
CREATE INDEX idx_memberships_org  ON memberships(org_id);

-- api_keys: org_id 보유 → RLS 대상 (DATA-03)
CREATE TABLE api_keys (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  key_hash     text NOT NULL,                 -- sha256(hex). 원문 미저장 (AUTH-05)
  name         text NOT NULL,
  role         text NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member','viewer')),  -- D-7
  last_used_at timestamptz,
  expires_at   timestamptz,
  revoked_at   timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_api_keys_hash ON api_keys(key_hash);  -- 인증 조회 경로
