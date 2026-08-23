-- 0010 organizations.plan_code [OBS-02 쿼터 / BILL-03]
--
-- 쿼터 스냅샷은 org의 플랜 한도(obs_max_hosts, obs_max_ingest_gb_month)를 읽어야 하는데
-- 공유 스키마의 organizations에는 플랜 연결이 없다(services/load-test/migrations/0001_init.sql).
-- 0000이 plans를 부트스트랩했으므로 연결 컬럼도 같은 방식(멱등 ALTER)으로 덧붙인다.
--
-- 한도는 차단 임계가 아니라 과금 기준선이다(§7-1 사용자 확정: 전 플랜 overage 과금).

ALTER TABLE organizations
  ADD COLUMN IF NOT EXISTS plan_code text NOT NULL DEFAULT 'free';

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'organizations_plan_code_fkey') THEN
    ALTER TABLE organizations
      ADD CONSTRAINT organizations_plan_code_fkey FOREIGN KEY (plan_code) REFERENCES plans(code);
  END IF;
END
$$;

COMMENT ON COLUMN organizations.plan_code IS
  'billing plan for this org; observability quota baselines live on plans.obs_*.';
