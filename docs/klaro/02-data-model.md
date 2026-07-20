# klaro — 데이터 모델 (Data Model / ERD 초안)

**버전** v1.0.0-dev · **작성일** 2026-07-16 · **상태** 초안

관련 문서: [기술 설계서](./01-technical-design.md) · [API 스펙](./03-api-spec.md) · [비용 모델](./04-cost-model.md) · [UI/UX](./05-ui-ux-design.md)

> RDB(PostgreSQL) 스키마 중심. 시계열 메트릭/트레이스/로그는 별도 저장소(VictoriaMetrics/Tempo/Loki)에 저장하며 여기서는 참조 키만 관리.

---

## 1. ERD 개요

```
organizations 1──∞ users(멤버십)        organizations 1──∞ projects
       │ 1                                        │ 1
       ├──∞ subscriptions ──1 plans               ├──∞ verified_domains ──1∞ endpoints(API 카탈로그)
       ├──∞ usage_records                         ├──∞ load_tests ──1∞ load_test_results
       ├──∞ api_keys                              ├──∞ scans ──1∞ scan_findings
       └──∞ audit_logs                            ├──∞ reports ──0..1 report_shares
                                                  └──∞ apm_agents
users ∞──∞ organizations  (via memberships, role)
github_installations 1──∞ projects
```

---

> **사이트 = `verified_domains`**. 각 사이트는 여러 API(`endpoints`)를 카탈로그로 보유하고, 부하 테스트는 이 중 일부를 **가중치와 함께 선택**해 하나의 시나리오로 동시 실행한다(load_tests ↔ endpoints N:M, `scenario.apis[]`).

## 2. 테이블 정의

### 2.1 테넌시 & 인증

**organizations** — 최상위 테넌트
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| name | text | |
| plan_id | uuid FK→plans | 현재 플랜 |
| created_at / updated_at | timestamptz | |

**users**
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| email | citext UNIQUE | |
| password_hash | text nullable | OAuth 전용 시 null |
| oauth_provider / oauth_sub | text | GitHub/Google. 부분 UNIQUE `(oauth_provider, oauth_sub) WHERE oauth_provider IS NOT NULL`(Phase 1, D-12) |
| name | text nullable | 표시 이름(Phase 1, D-12) |
| created_at / updated_at | timestamptz | (Phase 1: users 는 전역·RLS 비대상) |

**memberships** — 유저↔조직 N:M + 역할
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK→organizations | |
| user_id | uuid FK→users | |
| role | enum(owner, admin, member, viewer) | |
| — | UNIQUE(org_id, user_id) | |

**api_keys** — CLI/CI 연동용
| 컬럼 | 타입 |
|------|------|
| id | uuid PK |
| org_id | uuid FK |
| key_hash | text |
| name | text |
| role | enum(owner, admin, member, viewer) default member | Phase 1(D-7). API Key 권한 등급 |
| last_used_at / expires_at / revoked_at | timestamptz nullable |

**github_installations** — GitHub App 연동
| 컬럼 | 타입 |
|------|------|
| id | uuid PK |
| org_id | uuid FK |
| installation_id | bigint |
| account_login | text |
| created_at | timestamptz |

### 2.2 프로젝트 & 도메인

**projects**
| 컬럼 | 타입 |
|------|------|
| id | uuid PK |
| org_id | uuid FK |
| name | text |
| repo_url | text nullable |
| github_installation_id | uuid FK nullable |
| created_at | timestamptz |

**verified_domains** — 테스트 대상 **사이트**. DDoS 악용 차단 게이트([SC-01])
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK→organizations | Phase 1: RLS 비정규화 컬럼(D-9). `org_isolation` 정책 대상 |
| project_id | uuid FK→projects | |
| domain | text | |
| method | enum(dns_txt, file) | |
| token | text | 검증 토큰 |
| status | enum(pending, verified, failed) | |
| verified_at | timestamptz nullable | |
| — | UNIQUE(project_id, domain) | |

**endpoints** — 사이트(도메인)별 테스트 대상 **API 카탈로그** ([CAT-01]). 한 번 등록해 여러 부하 테스트·스캔에서 재사용
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| domain_id | uuid FK→verified_domains | 소속 사이트(검증된 도메인) |
| name | text | 사람이 읽는 이름(예: "로그인") |
| method | enum(GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS) | HTTP 메서드 |
| path | text | 예: `/api/v1/login` |
| query | jsonb nullable | 쿼리 파라미터 템플릿 |
| headers | jsonb nullable | 요청 헤더(토큰 등) |
| body_template | jsonb nullable | 요청 바디 템플릿 |
| expected_status | int nullable | 정상 응답 코드(검증용) |
| default_weight | int nullable | 부하 테스트 기본 트래픽 비중 |
| tags | text[] nullable | 분류(예: read/write, public) |
| created_at / updated_at | timestamptz | |
| — | UNIQUE(domain_id, method, path) | 사이트 내 API 중복 방지 |

### 2.3 부하 테스트 (S1)

**load_tests**
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK→organizations | Phase 1: RLS 비정규화 컬럼(D-9) |
| project_id | uuid FK | |
| domain_id | uuid FK→verified_domains | 테스트 대상 **사이트**(검증 필수) |
| target_url | text | domain 기준 베이스 URL(스킴+호스트). endpoints의 path와 결합 |
| scenario | jsonb | mode(weighted/journey), vu, duration, ramp-up, thresholds, **apis:[{endpoint_id, weight}]**(가중치 혼합 트래픽) |
| vu | int | 최대 가상 사용자(가중치로 API별 분배) |
| duration_sec | int | |
| status | enum(pending, validating, queued, provisioning, running, aggregating, completed, failed, aborted, rejected) | |
| region | text nullable | M3 멀티리전 |
| aborted_reason | text nullable | 서킷 브레이커 사유 |
| started_at / finished_at | timestamptz | |
| created_by | uuid FK→users | |

**load_test_results** — 요약 결과(시계열 원본은 TSDB). **API별 1행 + 테스트 전체 집계 1행**으로 저장
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK→organizations | Phase 1: RLS 비정규화 컬럼(D-9), load_tests 로부터 백필 |
| load_test_id | uuid FK | |
| endpoint_id | uuid FK→endpoints nullable | API별 결과 행. **NULL = 테스트 전체 집계** |
| rps_avg | numeric | throughput |
| latency_p50 / p95 / p99 | numeric(ms) | |
| error_rate | numeric | |
| max_vu_before_degradation | int | AI 한계점 도출 근거 |
| bottleneck_endpoint | text nullable | |
| metrics_ref | text | TSDB 쿼리 키 |
| created_at | timestamptz | |

### 2.4 보안 스캔 (S2)

**scans**
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK→organizations | Phase 1: RLS 비정규화 컬럼(D-9) |
| project_id | uuid FK | |
| type | enum(sast, dast) | |
| trigger | enum(manual, pr, schedule) | |
| target_url | text nullable | DAST 대상(검증 도메인) |
| pr_number | int nullable | SAST(PR) |
| status | enum(pending, running, completed, failed) | |
| score | int nullable | 보안 점수 |
| started_at / finished_at | timestamptz | |

**scan_findings**
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK→organizations | Phase 1: RLS 비정규화 컬럼(D-9), scans 로부터 백필 |
| scan_id | uuid FK→scans | |
| rule_id | text | Semgrep/ZAP 룰 |
| severity | enum(critical, high, medium, low, info) | |
| title | text | |
| file_path / line | text / int nullable | SAST |
| finding_hash | text | 재스캔 매칭용(오탐 상태 유지) |
| status | enum(open, ignored, fixed) | |
| ignore_reason | text nullable | [SC-04] |
| created_at | timestamptz | |

### 2.5 APM (S3)

**apm_agents**
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK→organizations | Phase 1: RLS 비정규화 컬럼(D-9) |
| project_id | uuid FK | |
| language | enum(nodejs, springboot, fastapi) | |
| ingest_token | text | mTLS 클라이언트 식별 |
| last_seen_at | timestamptz | |
| created_at | timestamptz | |

> 실측 메트릭/트레이스/로그는 각각 VictoriaMetrics / Tempo / Loki에 저장. `load_test_results.metrics_ref`, span/trace id로 상관.
>
> **MVP 임시 저장(Phase 1 현행)**: `apm_spans`·`apm_logs` 테이블을 PostgreSQL에 두어 span/log를 직접 보관한다(TSDB 미도입). 두 테이블 모두 Phase 1에서 `org_id uuid FK→organizations`(D-9)와 `org_isolation` RLS 정책을 가진다.

### 2.6 리포트 (S4)

**reports**
| 컬럼 | 타입 |
|------|------|
| id | uuid PK |
| org_id | uuid FK→organizations (Phase 1: RLS 비정규화 컬럼, D-9) |
| project_id | uuid FK |
| load_test_id | uuid FK nullable |
| scan_id | uuid FK nullable |
| performance_score / security_score | int |
| ai_summary | text |
| pdf_url | text |
| status | enum(generating, ready, failed) |
| created_at | timestamptz |

**report_shares**
| 컬럼 | 타입 |
|------|------|
| id | uuid PK |
| org_id | uuid FK→organizations (Phase 1: RLS 비정규화 컬럼, D-9; 공유 slug 조회는 klaro_system BYPASSRLS 경로) |
| report_id | uuid FK |
| slug | text UNIQUE |
| password_hash | text nullable |
| expires_at / revoked_at | timestamptz nullable |
| created_at | timestamptz |

### 2.7 과금 (Billing)

**plans**
| 컬럼 | 타입 |
|------|------|
| id | uuid PK |
| code | enum(free, pro, enterprise) |
| name | text |
| max_vu | int |
| max_duration_sec | int nullable |
| monthly_test_limit | int nullable |
| scan_features | jsonb |
| apm_retention_days | int |
| price_cents | int |

**subscriptions**
| 컬럼 | 타입 |
|------|------|
| id | uuid PK |
| org_id | uuid FK |
| plan_id | uuid FK |
| status | enum(active, past_due, canceled) |
| stripe_customer_id / stripe_subscription_id | text |
| current_period_start / end | timestamptz |

**usage_records** — VU-Minutes 계량([BILL-01])
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK | |
| load_test_id | uuid FK | |
| vu_minutes | numeric | VU × 분 |
| overage_vu_minutes | numeric | 한도 초과분([BILL-02]) |
| amount_cents | int | 초과 과금액 |
| stripe_usage_record_id | text nullable | Stripe 계량 연동 |
| created_at | timestamptz | |

### 2.8 감사

**audit_logs** — SOC2 대비
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK | |
| actor_user_id | uuid FK nullable | |
| action | text | 예: load_test.create, member.role_change |
| resource_type / resource_id | text / uuid | |
| metadata | jsonb | ip, user_agent 등 |
| created_at | timestamptz | |

---

## 3. 데이터 격리 & 보존 규칙

- **RLS**: `org_id`를 가진 모든 테이블에 Row-Level Security 정책. 세션 `SET app.current_org = <uuid>`.
  - **강제 메커니즘(Phase 1 구현)**: 앱은 `BYPASSRLS` 없는 **`klaro_app`** 롤로 접속하고, 요청마다 **트랜잭션 + `set_config('app.current_org', <uuid>, true)`**(=`SET LOCAL`, D-11)로 스코프를 건다. 정책은 `USING/WITH CHECK (org_id = current_setting('app.current_org', true)::uuid)` 이며, 2번째 인자 `true` 덕분에 **세션 미설정 시 SELECT 는 0건**(정보 누출 방지). 모든 대상 테이블은 `ENABLE` + `FORCE ROW LEVEL SECURITY`(테이블 소유자도 우회 금지).
  - **부트스트랩 예외(`klaro_system`, BYPASSRLS)**: 인증 전 users/membership 조회, 조직 생성(org 스코프 확정 전), `GET /shared/:slug` 공유 리포트, APM ingest 토큰→org 해석 등 스코프 확정 이전 경로만 전용 시스템 롤로 처리(D-10). 그 외 org 스코프 쿼리는 전부 `klaro_app` tx.
  - **org_id 비정규화(D-9)**: 자식 테이블(`load_test_results`/`scan_findings`/`report_shares`)에도 `org_id`를 직접 두어 정책을 평면 등식으로 유지한다(부모 조인 서브쿼리 회피).
- **보존 자동화([BILL-03])**: 플랜별 APM 보존일(Free 24h / Pro 14d / Enterprise 90d) 경과 데이터는 TSDB/Loki 리텐션 정책 + 배치 삭제 잡으로 제거.
- **Ephemeral 소스**: 스캔 소스코드는 DB/디스크에 절대 저장 금지(RAM 전용).

---

## 4. 주요 인덱스(초안)

- `load_tests(project_id, status, created_at)` — 대시보드 목록.
- `endpoints(domain_id)` — 사이트별 API 카탈로그 목록.
- `endpoints(domain_id, method, path)` UNIQUE — 사이트 내 API 중복 방지.
- `load_test_results(load_test_id, endpoint_id)` — API별 결과 조회.
- `scan_findings(scan_id, status)`, `scan_findings(finding_hash)` — 오탐 매칭.
- `usage_records(org_id, created_at)` — 월별 집계.
- `verified_domains(project_id, domain)` UNIQUE — 소유권 게이트 조회.
- `report_shares(slug)` UNIQUE — 공유 링크 조회.
