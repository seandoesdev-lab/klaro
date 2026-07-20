-- S2 Phase 2: Security scan real-engine schema extensions (per p2 design §3.1).
-- All additive/nullable → backward compatible, re-runnable. Superuser (klaro) runs
-- initdb so RLS is bypassed here; org_id/RLS policies on scans/scan_findings
-- (0005/0006) are unaffected by ADD COLUMN and keep protecting the new columns.

-- scans: source reference (metadata only, never source body) + DAST mode.
ALTER TABLE scans ADD COLUMN IF NOT EXISTS source_type text;   -- 'repo' | 'upload' (sast only)
ALTER TABLE scans ADD COLUMN IF NOT EXISTS source_ref  text;   -- repo URL or uploaded filename (a pointer, not source content)
ALTER TABLE scans ADD COLUMN IF NOT EXISTS mode        text;   -- dast: 'baseline' | 'active'

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_scans_source_type') THEN
    ALTER TABLE scans ADD CONSTRAINT chk_scans_source_type
      CHECK (source_type IS NULL OR source_type IN ('repo','upload'));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_scans_mode') THEN
    ALTER TABLE scans ADD CONSTRAINT chk_scans_mode
      CHECK (mode IS NULL OR mode IN ('baseline','active'));
  END IF;
END$$;

-- scan_findings: evidence (jsonb) + a few common columns for filtering/joining.
-- [EPHEM-01] NOTE: no source-body column is ever added; `evidence` carries only a
-- capped code snippet (<=10 lines) / scanner metadata, never a full file copy.
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS evidence        jsonb;
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS cwe             text;
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS confidence      text;   -- high|medium|low
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS package         text;   -- osv
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS package_version text;   -- osv
