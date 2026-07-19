-- S3 APM + S4 Reports (per 02-data-model §2.5, §2.6)
-- MVP storage: everything in Postgres (no VictoriaMetrics/Tempo/Loki).

-- ── APM (S3) ────────────────────────────────────────────────────────────────
CREATE TABLE apm_agents (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   uuid NOT NULL REFERENCES projects(id),
  language     text NOT NULL CHECK (language IN ('nodejs','springboot','fastapi')),
  ingest_token text NOT NULL,
  last_seen_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, ingest_token)
);

CREATE TABLE apm_spans (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id     uuid NOT NULL REFERENCES projects(id),
  service        text NOT NULL,
  trace_id       text NOT NULL,
  span_id        text NOT NULL,
  parent_span_id text,
  name           text NOT NULL,
  duration_ms    numeric NOT NULL DEFAULT 0,
  status         text NOT NULL DEFAULT 'ok',
  ts             timestamptz NOT NULL DEFAULT now(),
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_apm_spans_slow ON apm_spans(project_id, duration_ms);
CREATE INDEX idx_apm_spans_trace ON apm_spans(trace_id);

CREATE TABLE apm_logs (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id),
  level      text NOT NULL DEFAULT 'info',
  message    text NOT NULL,
  ts         timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_apm_logs_project_ts ON apm_logs(project_id, ts);

-- ── Reports (S4) ─────────────────────────────────────────────────────────────
CREATE TABLE reports (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id        uuid NOT NULL REFERENCES projects(id),
  load_test_id      uuid REFERENCES load_tests(id),
  scan_id           uuid REFERENCES scans(id),
  performance_score int NOT NULL DEFAULT 0,
  security_score    int NOT NULL DEFAULT 0,
  ai_summary        text NOT NULL DEFAULT '',
  pdf_url           text,
  status            text NOT NULL DEFAULT 'generating' CHECK (status IN ('generating','ready','failed')),
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_reports_project ON reports(project_id, created_at);

CREATE TABLE report_shares (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  report_id     uuid NOT NULL REFERENCES reports(id),
  slug          text NOT NULL UNIQUE,
  password_hash text,
  expires_at    timestamptz,
  revoked_at    timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now()
);
