-- 0011 알림 룰 스펙 · 이벤트 dedup · 과금 발행 원장 [OBS-06/07, BILL-03]

-- ---------------------------------------------------------------------------
-- alert_rules.query_spec — 룰의 구조화 입력
--
-- 0004의 query 컬럼은 렌더된 MetricsQL을 담는다. 그런데 그 MetricsQL을 사용자가
-- 직접 쓰게 하면 explorer에서 막아둔 문제가 알림 경로로 되돌아온다 — raw 쿼리는
-- 호출자가 org 라벨 매처를 벗길 수 있는 쿼리다. 그래서 API는 explorer와 똑같이
-- 구조화 파라미터(metric/filter/agg/step)만 받고, 서버가 org 매처를 주입해 렌더한
-- 결과를 query에 넣는다. query_spec은 그 입력 원본이라 PATCH 때 렌더 결과를
-- 거꾸로 파싱하지 않아도 된다.
-- ---------------------------------------------------------------------------
ALTER TABLE alert_rules
  ADD COLUMN IF NOT EXISTS query_spec jsonb NOT NULL DEFAULT '{}';

COMMENT ON COLUMN alert_rules.query_spec IS
  'structured query inputs (metric, filters, agg, step); alert_rules.query holds the rendered MetricsQL.';
COMMENT ON COLUMN alert_rules.query IS
  'server-rendered MetricsQL. Never caller-supplied: a raw expression could drop the org matcher.';

-- ---------------------------------------------------------------------------
-- alert_events.fingerprint — 같은 발동을 두 번 기록하지 않기 위한 키
--
-- vmalert는 발동 중인 알림을 resendDelay 주기로 계속 재전송한다. 수신부가 멱등하지
-- 않으면 1분마다 이벤트 행이 하나씩 쌓인다. fingerprint(룰 + 라벨셋)로 "열린
-- 이벤트"를 찾아 재전송은 무시하고, 해소 시 그 행을 닫는다.
-- ---------------------------------------------------------------------------
ALTER TABLE alert_events
  ADD COLUMN IF NOT EXISTS fingerprint text NOT NULL DEFAULT '';

-- 열린 이벤트는 (룰, 라벨셋)당 하나뿐이다. 부분 유니크 인덱스라 해소된 과거
-- 이벤트는 몇 개든 남을 수 있고, 재전송만 구조적으로 막힌다.
CREATE UNIQUE INDEX IF NOT EXISTS uq_alert_events_open
  ON alert_events(org_id, rule_id, fingerprint)
  WHERE resolved_at IS NULL;

-- ---------------------------------------------------------------------------
-- observability_usage_emissions — 과금 이벤트 발행 원장 [BILL-03]
--
-- klaro.usage.emitted는 Billing이 청구에 쓰는 신호다. 같은 기간·미터를 두 번
-- 발행하면 두 번 청구된다. 발행을 여기에 기록하고 (org, period, meter) 유니크로
-- 잠그면, 재시작·중복 실행·크론 겹침이 모두 무해해진다.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS observability_usage_emissions (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  period_start timestamptz NOT NULL,
  period_end   timestamptz NOT NULL,
  meter        text NOT NULL CHECK (meter IN ('observability_hosts', 'observability_ingest_gb')),
  -- quantity는 "지금까지 발행한 누적값"이다. 발행 이벤트에는 증분을 싣는다.
  -- 증분 발행이라 재시작·중복 실행이 무해하고(원장을 다시 읽어 델타를 재계산),
  -- 기간이 끝나기를 기다리지 않아도 된다. 호스트 미터도 같은 형태다 —
  -- 최고수위(max)의 증가분을 싣기 때문에 Billing이 더하면 최종 max가 된다.
  quantity     numeric NOT NULL,
  -- 계산 근거를 남긴다. 청구 이의가 들어왔을 때 "그때 무엇을 보고 이 수치를
  -- 냈는가"를 답할 수 있어야 한다.
  computation  jsonb NOT NULL DEFAULT '{}',
  emitted_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, period_start, meter)
);
CREATE INDEX IF NOT EXISTS idx_usage_emissions_org_period
  ON observability_usage_emissions(org_id, period_start);

ALTER TABLE observability_usage_emissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability_usage_emissions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS observability_usage_emissions_isolation ON observability_usage_emissions;
CREATE POLICY observability_usage_emissions_isolation ON observability_usage_emissions
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);

-- 누적값을 갱신하므로 UPDATE가 필요하다. 행 삭제 권한은 주지 않는다:
-- 발행 원장은 청구 근거이므로 지워질 수 있어서는 안 된다.
GRANT SELECT, INSERT, UPDATE ON observability_usage_emissions TO klaro_obs_app;
