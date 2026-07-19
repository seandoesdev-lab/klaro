-- S2 Security Scan: scans + scan_findings (per 02-data-model §2.4)
CREATE TABLE scans (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid NOT NULL REFERENCES projects(id),
  type        text NOT NULL CHECK (type IN ('sast','dast')),
  trigger     text NOT NULL CHECK (trigger IN ('manual','pr','schedule')),
  target_url  text,
  pr_number   int,
  status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed')),
  score       int,
  started_at  timestamptz,
  finished_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_scans_project ON scans(project_id, created_at);

CREATE TABLE scan_findings (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  scan_id       uuid NOT NULL REFERENCES scans(id),
  rule_id       text NOT NULL,
  severity      text NOT NULL CHECK (severity IN ('critical','high','medium','low','info')),
  title         text NOT NULL,
  file_path     text,
  line          int,
  finding_hash  text NOT NULL,
  status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open','ignored','fixed')),
  ignore_reason text,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_scan_findings_scan_status ON scan_findings(scan_id, status);
CREATE INDEX idx_scan_findings_hash ON scan_findings(finding_hash);
