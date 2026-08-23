-- 0003 observability_usage_rollups [OBS-08/BILL-03] — 수집량 롤업(설계 §3 / HOW-6)
-- 과금 미터 원천. 시계열 원본이 아니라 기간 집계 메타이므로 RDB 보관이 맞다.

CREATE TABLE observability_usage_rollups (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  period_start   timestamptz NOT NULL,
  period_end     timestamptz NOT NULL,
  signal         text NOT NULL CHECK (signal IN ('metrics','traces','logs')),
  ingested_bytes bigint NOT NULL DEFAULT 0,
  series_count   bigint,
  host_count_max int,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, period_start, signal)
);
CREATE INDEX idx_observability_usage_org_period ON observability_usage_rollups(org_id, period_start);

ALTER TABLE observability_usage_rollups ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability_usage_rollups FORCE ROW LEVEL SECURITY;
CREATE POLICY observability_usage_rollups_isolation ON observability_usage_rollups
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON observability_usage_rollups TO klaro_obs_app;
