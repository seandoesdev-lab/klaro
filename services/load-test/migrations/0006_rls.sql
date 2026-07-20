-- Phase 1: Row-Level Security 정책 (TENANT-02)
-- 전 org 스코프 테이블에 ENABLE + FORCE RLS + org_isolation 정책.
-- current_setting('app.current_org', true) 2번째 인자 true = 미설정 시 예외 대신 NULL 반환
--   → org_id = NULL 은 false → 세션 미설정 상태 SELECT 0건 (정보 누출 방지).
-- FORCE: 테이블 소유자(klaro)도 RLS 적용. 단 슈퍼유저/ BYPASSRLS 롤(klaro_system)은 항상 우회.

-- org_id = current_org 패턴 (자식 포함 12개 테이블)
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'projects','memberships','api_keys','verified_domains','load_tests',
    'load_test_results','scans','scan_findings','apm_agents','apm_spans',
    'apm_logs','reports','report_shares'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($f$CREATE POLICY org_isolation ON %I
      USING (org_id = current_setting('app.current_org', true)::uuid)
      WITH CHECK (org_id = current_setting('app.current_org', true)::uuid)$f$, t);
  END LOOP;
END $$;

-- organizations: 스코프 키가 id
ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE organizations FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON organizations
  USING      (id = current_setting('app.current_org', true)::uuid)
  WITH CHECK (id = current_setting('app.current_org', true)::uuid);
