-- 0001 observability_keys [OBS-02] — org 단위 수집 API 키(설계 §3 / HOW-4)
-- 시크릿은 저장하지 않는다: sha256(secret)만 보관하고 평문은 발급 응답에서 1회만 노출한다.

CREATE TABLE observability_keys (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name         text NOT NULL,
  key_prefix   text NOT NULL,                 -- 표시용(예: obsk_ab12), 시크릿 아님
  key_hash     text NOT NULL,                 -- sha256(secret)
  scope_label  jsonb,                         -- {service?, env?} 라벨 태깅(계층 키 트리 아님)
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
  created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  last_used_at timestamptz,
  revoked_at   timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name),
  UNIQUE (key_hash)                           -- Collector authz 빠른 경로(전역 조회)
);
CREATE INDEX idx_observability_keys_org_status ON observability_keys(org_id, status);

ALTER TABLE observability_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY observability_keys_isolation ON observability_keys
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON observability_keys TO klaro_obs_app;
