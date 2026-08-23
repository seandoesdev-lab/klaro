-- 0002 observability_hosts [OBS-02 쿼터-호스트수] — 활성 호스트 레지스트리(설계 §3 / HOW-4)
-- host_ident는 SDK가 정규화한 service.instance.id(컨테이너는 pod uid 우선, 없으면 hostname+PID).

CREATE TABLE observability_hosts (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  key_id        uuid REFERENCES observability_keys(id) ON DELETE SET NULL,
  host_ident    text NOT NULL,
  service       text,
  env           text,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, host_ident)
);
-- 활성 호스트 카운트(윈도 내 last_seen)
CREATE INDEX idx_observability_hosts_org_seen ON observability_hosts(org_id, last_seen_at);

ALTER TABLE observability_hosts ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability_hosts FORCE ROW LEVEL SECURITY;
CREATE POLICY observability_hosts_isolation ON observability_hosts
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON observability_hosts TO klaro_obs_app;
