-- 0005 alert_events [OBS-07] — 발동/해소 이력(설계 §3 / HOW-1 receiver)

CREATE TABLE alert_events (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id            uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  rule_id           uuid NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
  state             text NOT NULL CHECK (state IN ('firing','resolved')),
  value             numeric,
  labels            jsonb,
  notified_channels jsonb,
  started_at        timestamptz NOT NULL DEFAULT now(),
  resolved_at       timestamptz
);
CREATE INDEX idx_alert_events_org_started ON alert_events(org_id, started_at);
CREATE INDEX idx_alert_events_org_rule_state ON alert_events(org_id, rule_id, state);

ALTER TABLE alert_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_events FORCE ROW LEVEL SECURITY;
CREATE POLICY alert_events_isolation ON alert_events
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON alert_events TO klaro_obs_app;
