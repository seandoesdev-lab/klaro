-- 0006 dashboards [OBS-09] — 자체 패널 구성(설계 §3 / HOW-3)
-- 별도 dashboard_panels 테이블을 두지 않고 spec JSONB 하나로 관리(YAGNI).
-- 기능 자체는 M2+ 이연(§7-3)이지만 스키마는 파운데이션에 포함한다.

CREATE TABLE dashboards (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name        text NOT NULL,
  description text,
  spec        jsonb NOT NULL DEFAULT '{"panels":[]}',
  created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);
CREATE INDEX idx_dashboards_org ON dashboards(org_id);

ALTER TABLE dashboards ENABLE ROW LEVEL SECURITY;
ALTER TABLE dashboards FORCE ROW LEVEL SECURITY;
CREATE POLICY dashboards_isolation ON dashboards
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON dashboards TO klaro_obs_app;
