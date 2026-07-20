-- Phase 1: org 스코프 자식 테이블에 org_id 비정규화 컬럼 추가 (D-9, TENANT-01)
-- 패턴: 컬럼 추가(nullable) → 부모 조인 백필 → NOT NULL → FK → 인덱스
-- 슈퍼유저(klaro) 실행이라 RLS 우회, 재실행 시 중복 컬럼 에러 방지 위해 IF NOT EXISTS 사용.

-- ── 직접 project_id 보유 테이블 (부모 = projects) ──────────────────────────────
ALTER TABLE verified_domains ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE verified_domains vd SET org_id = p.org_id
  FROM projects p WHERE p.id = vd.project_id AND vd.org_id IS NULL;
ALTER TABLE verified_domains ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE verified_domains ADD CONSTRAINT fk_vd_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_vd_org ON verified_domains(org_id);

ALTER TABLE load_tests ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE load_tests lt SET org_id = p.org_id
  FROM projects p WHERE p.id = lt.project_id AND lt.org_id IS NULL;
ALTER TABLE load_tests ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE load_tests ADD CONSTRAINT fk_lt_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_lt_org ON load_tests(org_id);

ALTER TABLE scans ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE scans s SET org_id = p.org_id
  FROM projects p WHERE p.id = s.project_id AND s.org_id IS NULL;
ALTER TABLE scans ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE scans ADD CONSTRAINT fk_scans_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_scans_org ON scans(org_id);

ALTER TABLE apm_agents ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE apm_agents a SET org_id = p.org_id
  FROM projects p WHERE p.id = a.project_id AND a.org_id IS NULL;
ALTER TABLE apm_agents ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE apm_agents ADD CONSTRAINT fk_apm_agents_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_apm_agents_org ON apm_agents(org_id);

ALTER TABLE apm_spans ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE apm_spans a SET org_id = p.org_id
  FROM projects p WHERE p.id = a.project_id AND a.org_id IS NULL;
ALTER TABLE apm_spans ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE apm_spans ADD CONSTRAINT fk_apm_spans_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_apm_spans_org ON apm_spans(org_id);

ALTER TABLE apm_logs ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE apm_logs a SET org_id = p.org_id
  FROM projects p WHERE p.id = a.project_id AND a.org_id IS NULL;
ALTER TABLE apm_logs ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE apm_logs ADD CONSTRAINT fk_apm_logs_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_apm_logs_org ON apm_logs(org_id);

ALTER TABLE reports ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE reports r SET org_id = p.org_id
  FROM projects p WHERE p.id = r.project_id AND r.org_id IS NULL;
ALTER TABLE reports ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE reports ADD CONSTRAINT fk_reports_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_reports_org ON reports(org_id);

-- ── 자식(조인) 테이블: 부모 = 상위 리소스 ────────────────────────────────────────
ALTER TABLE load_test_results ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE load_test_results r SET org_id = lt.org_id
  FROM load_tests lt WHERE lt.id = r.load_test_id AND r.org_id IS NULL;
ALTER TABLE load_test_results ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE load_test_results ADD CONSTRAINT fk_ltr_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_ltr_org ON load_test_results(org_id);

ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE scan_findings f SET org_id = s.org_id
  FROM scans s WHERE s.id = f.scan_id AND f.org_id IS NULL;
ALTER TABLE scan_findings ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE scan_findings ADD CONSTRAINT fk_sf_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_sf_org ON scan_findings(org_id);

ALTER TABLE report_shares ADD COLUMN IF NOT EXISTS org_id uuid;
UPDATE report_shares sh SET org_id = r.org_id
  FROM reports r WHERE r.id = sh.report_id AND sh.org_id IS NULL;
ALTER TABLE report_shares ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE report_shares ADD CONSTRAINT fk_rs_org FOREIGN KEY (org_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS idx_rs_org ON report_shares(org_id);
