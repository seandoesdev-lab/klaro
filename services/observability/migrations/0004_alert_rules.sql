-- 0004 alert_rules [OBS-06] — 알림 룰(설계 §3 / HOW-1)
-- signal 컬럼은 log/trace까지 미리 두되 MVP는 metric만 활성(vmalert가 메트릭 전용).

CREATE TABLE alert_rules (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id           uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name             text NOT NULL,
  signal           text NOT NULL DEFAULT 'metric' CHECK (signal IN ('metric','log','trace')),
  query            text NOT NULL,             -- MetricsQL 식(또는 구조화 표현 렌더 결과)
  comparator       text NOT NULL CHECK (comparator IN ('gt','gte','lt','lte')),
  threshold        numeric NOT NULL,
  for_duration_sec int NOT NULL DEFAULT 60,   -- 지속(평가 창)
  severity         text NOT NULL DEFAULT 'warning' CHECK (severity IN ('info','warning','critical')),
  channels         jsonb NOT NULL DEFAULT '[]',  -- [{type: email|slack, target}]
  enabled          boolean NOT NULL DEFAULT true,
  created_by       uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);
CREATE INDEX idx_alert_rules_org_enabled ON alert_rules(org_id, enabled);

ALTER TABLE alert_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY alert_rules_isolation ON alert_rules
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON alert_rules TO klaro_obs_app;
