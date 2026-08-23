-- 0007 plans 확장 [OBS-08/BILL-03] — 신호별 보존 + 관측 쿼터(설계 §3 / HOW-5·HOW-6·HOW-10)
-- 기존 apm_retention_days는 MVP 스냅샷 호환 위해 존치(후속 deprecate).
-- plans는 org 스코프가 아닌 전역 카탈로그이므로 RLS 대상이 아니다.

ALTER TABLE plans
  ADD COLUMN IF NOT EXISTS obs_metrics_retention_days        int NOT NULL DEFAULT 1,  -- Free 1d
  ADD COLUMN IF NOT EXISTS obs_traces_retention_days         int NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS obs_logs_retention_days           int NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS obs_max_hosts                     int,      -- NULL=무제한(Enterprise)
  ADD COLUMN IF NOT EXISTS obs_max_ingest_gb_month           numeric,  -- secondary guard
  -- 다운샘플링 롤업(5m/1h) 시리즈 보존일(HOW-10). raw는 obs_metrics_retention_days.
  ADD COLUMN IF NOT EXISTS obs_metrics_rollup_retention_days int;

-- 쿼터 초과는 차단이 아니라 overage 과금이다(§7-1 사용자 확정). 한도 컬럼은 과금 기준선일 뿐
-- 수집 거부 조건이 아니며, Collector authz는 status=revoked 키만 거부한다.
COMMENT ON COLUMN plans.obs_max_hosts IS
  'observability host quota baseline (primary meter). Not an ingest block - overage billing.';
COMMENT ON COLUMN plans.obs_max_ingest_gb_month IS
  'monthly ingest GB guard (secondary meter). Not an ingest block - overage billing.';

-- 플랜 시드(멱등). 계약의 Free 24h / Pro 14d / Enterprise 90d는 메트릭 기준,
-- 트레이스·로그는 비용 고려해 더 짧게 잡는다(설계 §3 주석).
INSERT INTO plans (code, name, max_vu, apm_retention_days, price_cents,
                   obs_metrics_retention_days, obs_traces_retention_days, obs_logs_retention_days,
                   obs_max_hosts, obs_max_ingest_gb_month, obs_metrics_rollup_retention_days)
VALUES
  ('free',       'Free',        50,  1,     0,  1,  1,  1,    5,   10, NULL),
  ('pro',        'Pro',        500, 14,  9900, 14,  7, 14,   50,  500,   90),
  ('enterprise', 'Enterprise', 5000, 90, 99900, 90, 30, 30, NULL, NULL,  365)
ON CONFLICT (code) DO NOTHING;
