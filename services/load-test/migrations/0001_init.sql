CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE organizations (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE projects (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE verified_domains (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid NOT NULL REFERENCES projects(id),
  domain      text NOT NULL,
  method      text NOT NULL CHECK (method IN ('dns_txt','file')),
  token       text NOT NULL,
  status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','verified','failed')),
  verified_at timestamptz,
  UNIQUE (project_id, domain)
);

CREATE TABLE load_tests (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id     uuid NOT NULL REFERENCES projects(id),
  target_url     text NOT NULL,
  scenario       jsonb NOT NULL,
  vu             int NOT NULL,
  duration_sec   int NOT NULL,
  status         text NOT NULL,
  aborted_reason text,
  started_at     timestamptz,
  finished_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_load_tests_project ON load_tests(project_id, status, created_at);

CREATE TABLE load_test_results (
  id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  load_test_id               uuid NOT NULL REFERENCES load_tests(id),
  rps_avg                    numeric NOT NULL DEFAULT 0,
  latency_p50                numeric NOT NULL DEFAULT 0,
  latency_p95                numeric NOT NULL DEFAULT 0,
  latency_p99                numeric NOT NULL DEFAULT 0,
  error_rate                 numeric NOT NULL DEFAULT 0,
  max_vu_before_degradation  int NOT NULL DEFAULT 0,
  bottleneck_endpoint        text,
  metrics_ref                text,
  created_at                 timestamptz NOT NULL DEFAULT now(),
  UNIQUE (load_test_id)
);

-- 개발 스텁이 참조하는 고정 org/project 시드
INSERT INTO organizations (id, name)
  VALUES ('00000000-0000-0000-0000-000000000001', 'dev-org');
INSERT INTO projects (id, org_id, name)
  VALUES ('00000000-0000-0000-0000-000000000002',
          '00000000-0000-0000-0000-000000000001', 'dev-project');
