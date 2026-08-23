-- 0009 키 로테이션 grace + Collector authz 해석 함수 [OBS-02 / 설계 HOW-4 · §4.1 · §4.4]
--
-- 로테이션은 "신규 키 발급 + grace 윈도(구·신 병존) 후 구키 revoke"다(HOW-4).
-- grace_until은 구키가 언제까지 수집을 통과할지를 나타낸다. 즉시 폐기(DELETE)는 grace 없이
-- status='revoked'로 바뀌므로 곧바로 거부된다.

ALTER TABLE observability_keys
  ADD COLUMN IF NOT EXISTS grace_until     timestamptz,
  ADD COLUMN IF NOT EXISTS rotated_from_id uuid REFERENCES observability_keys(id) ON DELETE SET NULL;

COMMENT ON COLUMN observability_keys.grace_until IS
  'rotation grace deadline: an active key stops being accepted for ingest after this instant. NULL = no deadline.';
COMMENT ON COLUMN observability_keys.rotated_from_id IS
  'the key this one replaced, for audit trails across a rotation chain.';

-- grace가 걸린 키만(대부분 0~소수) 인덱싱한다.
CREATE INDEX IF NOT EXISTS idx_observability_keys_grace
  ON observability_keys(grace_until) WHERE grace_until IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Collector authz 빠른 경로: key_hash → org 해석
--
-- 문제: 수집 인증은 org를 알아내기 위한 조회다. 그런데 observability_keys는 FORCE RLS이고
-- 정책은 app.current_org를 요구한다 — 아직 org를 모르는 상태에서는 조회 자체가 불가능하다.
-- (닭과 달걀. 설계 §3의 "UNIQUE(key_hash) — 전역 조회"가 가리키는 지점.)
--
-- 해결: RLS를 우회하는 최소 권한 창구 하나만 만든다. NOLOGIN·BYPASSRLS 롤이 소유한
-- SECURITY DEFINER 함수로, 정확히 일치하는 key_hash 한 건의 org/상태만 돌려준다.
-- 이 롤로는 로그인할 수 없고, 함수는 목록/검색/열거를 제공하지 않으며, 호출자는 이미
-- 시크릿을 알고 있어야 한다. 앱 롤에 BYPASSRLS를 주는 것보다 노출면이 훨씬 좁다.
-- ---------------------------------------------------------------------------
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'klaro_obs_authz') THEN
    CREATE ROLE klaro_obs_authz NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS;
  END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO klaro_obs_authz;
GRANT SELECT ON observability_keys TO klaro_obs_authz;

-- 출력 컬럼명에 r_ 접두사를 쓰는 이유: RETURNS TABLE의 출력 파라미터는 본문 스코프에 들어와
-- 같은 이름의 테이블 컬럼과 충돌한다(ambiguous column).
CREATE OR REPLACE FUNCTION obs_resolve_ingest_key(p_key_hash text)
RETURNS TABLE (
  r_org_id      uuid,
  r_key_id      uuid,
  r_status      text,
  r_grace_until timestamptz,
  r_revoked_at  timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT k.org_id, k.id, k.status, k.grace_until, k.revoked_at
  FROM public.observability_keys k
  WHERE k.key_hash = p_key_hash
$$;

ALTER FUNCTION obs_resolve_ingest_key(text) OWNER TO klaro_obs_authz;
REVOKE ALL ON FUNCTION obs_resolve_ingest_key(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION obs_resolve_ingest_key(text) TO klaro_obs_app;
