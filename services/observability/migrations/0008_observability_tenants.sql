-- 0008 observability_tenants [HOW-8] — org → 백엔드 native 테넌트 영속 할당(설계 §2 HOW-8 · §6 2단계)
--
-- 파운데이션의 tenants.MemoryMapper는 프로세스 메모리에 순차 배정했다. 재시작하면 번호가 다시
-- 매겨져서, 이미 VictoriaMetrics에 기록된 시리즈가 다른 org의 AccountID로 넘어갈 수 있다.
-- 그건 Postgres 밖에서 일어나는 크로스테넌트 누출이라 RLS가 잡지 못한다. Collector 라우팅
-- (설계 §6 4단계)이 실제로 VM에 쓰기 시작하기 전에 반드시 영속 테이블로 교체해야 한다.
--
-- AccountID를 uuid 해시로 만들지 않는 이유: 32bit 공간에서 해시는 충돌하고, 충돌한 두 org는
-- VM 안에서 메트릭이 합쳐진다. 시퀀스 배정은 구조적으로 충돌할 수 없다.

-- 시퀀스로 배정하면 배정 시점에 다른 org의 행을 읽을 필요가 없다 —
-- RLS 아래에서 MAX(vm_account_id)를 조회하는 (불가능한) 경로를 피한다.
-- START 1: VictoriaMetrics는 AccountID 0을 기본 계정으로 취급하므로 0은 절대 배정하지 않는다.
CREATE SEQUENCE IF NOT EXISTS observability_vm_account_id_seq START WITH 1 MINVALUE 1;

CREATE TABLE observability_tenants (
  org_id        uuid PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  -- vminsert/vmselect URL의 테넌트 경로 세그먼트(예: /insert/7/prometheus).
  vm_account_id integer NOT NULL UNIQUE DEFAULT nextval('observability_vm_account_id_seq')
                CHECK (vm_account_id > 0),
  -- Tempo·Loki의 X-Scope-OrgID. 두 백엔드는 임의 문자열을 받으므로 org uuid를 그대로 쓴다.
  scope_org_id  text NOT NULL UNIQUE,
  created_at    timestamptz NOT NULL DEFAULT now()
);

-- org_id가 PK이므로 org당 한 행이고, 배정은 한 번 이뤄진 뒤 불변이다(캐시 안전).
ALTER TABLE observability_tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability_tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY observability_tenants_isolation ON observability_tenants
  USING (org_id = current_setting('app.current_org')::uuid)
  WITH CHECK (org_id = current_setting('app.current_org')::uuid);

GRANT SELECT, INSERT ON observability_tenants TO klaro_obs_app;
-- UPDATE/DELETE는 주지 않는다: 배정이 바뀌면 이미 저장된 시리즈의 소유자가 바뀐다.
GRANT USAGE ON SEQUENCE observability_vm_account_id_seq TO klaro_obs_app;
