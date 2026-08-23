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
       ├──∞ audit_logs                            ├──∞ reports ──0..1 report_shares
       │                                          └──∞ apm_agents (MVP 스냅샷)
       └──∞ observability_keys ──∞ observability_hosts   (상시 관측, org 단위)
       └──∞ alert_rules ──∞ alert_events · dashboards · observability_usage_rollups
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
| oauth_provider / oauth_sub | text | GitHub/Google |
| created_at | timestamptz | |

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
| scan_id | uuid FK→scans | |
| rule_id | text | Semgrep/ZAP 룰 |
| severity | enum(critical, high, medium, low, info) | |
| title | text | |
| file_path / line | text / int nullable | SAST |
| finding_hash | text | 재스캔 매칭용(오탐 상태 유지) |
| status | enum(open, ignored, fixed) | |
| ignore_reason | text nullable | [SC-04] |
| created_at | timestamptz | |

### 2.5 APM / 상시 관측 (S3)

> **2026-08-23**: S3가 독립 상시 관측 제품(Datadog 유사)으로 승격됨에 따라, 아래 `apm_agents`(project 단위 `ingest_token`)는 현재 **MVP 스냅샷 스코프**다(리포트용 project-scoped 수집·조회, `services/load-test/` migration 0003). 그 아래 **[상시 관측 스키마]**가 org 단위 상시 플랫폼(`services/observability/` migration 0001~0007)을 정의하며, 두 경로는 **공존**한다(대체 아님 — 리포트 스냅샷 경계 O10).

**apm_agents** (MVP 스냅샷)
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| project_id | uuid FK | |
| language | enum(nodejs, springboot, fastapi) | |
| ingest_token | text | mTLS 클라이언트 식별 |
| last_seen_at | timestamptz | |
| created_at | timestamptz | |

> MVP는 편의상 `apm_spans`/`apm_logs`를 Postgres에 저장(migration 0003). 상시 플랫폼은 이 데이터를 **VictoriaMetrics/Tempo/Loki로 이행**해 "시계열은 RDB 밖" 불변식을 복원한다. Postgres에는 참조 키·메타만.

**[상시 관측 스키마] (2026-08-23 확정 — 상세 `_workspace/05_architect_observability-design.md`)**

모든 테이블 org-scoped + FORCE RLS(`app.current_org`). 시계열 원본은 VM(AccountID=org)/Tempo·Loki(X-Scope-OrgID=org)에 저장.

**observability_keys** [OBS-02] — org 단위 수집 API 키(project `ingest_token` 승격)
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK | |
| name | text | UNIQUE(org_id, name) |
| key_prefix | text | 표시용(시크릿 아님) |
| key_hash | text | sha256(secret), 시크릿은 1회 노출 후 미저장. UNIQUE |
| scope_label | jsonb nullable | {service?, env?} 태깅(계층 키 트리 아님) |
| status | enum(active, revoked) | |
| created_by / last_used_at / revoked_at / created_at | | |

**observability_hosts** [OBS-02 호스트수 쿼터]
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK | |
| key_id | uuid FK→observability_keys nullable | |
| host_ident | text | 정규화 service.instance.id / hostname. UNIQUE(org_id, host_ident) |
| service / env | text nullable | |
| first_seen_at / last_seen_at | timestamptz | 활성 호스트 카운트(윈도 내 last_seen) |

**observability_usage_rollups** [OBS-08/BILL-03 과금·수집량]
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK | |
| period_start / period_end | timestamptz | UNIQUE(org_id, period_start, signal) |
| signal | enum(metrics, traces, logs) | |
| ingested_bytes | bigint | |
| series_count / host_count_max | bigint / int | |

**alert_rules** [OBS-06]
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK | |
| name | text | UNIQUE(org_id, name) |
| signal | enum(metric, log, trace) | MVP는 metric만 활성 |
| query | text | MetricsQL 식(또는 구조화 렌더 결과) |
| comparator | enum(gt, gte, lt, lte) | |
| threshold | numeric | |
| for_duration_sec | int | 지속(평가 창) |
| severity | enum(info, warning, critical) | |
| channels | jsonb | [{type:email/slack, target}] |
| enabled | bool | |
| created_by / created_at / updated_at | | |

**alert_events** [OBS-07]
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK | |
| rule_id | uuid FK→alert_rules | |
| state | enum(firing, resolved) | |
| value | numeric nullable | |
| labels / notified_channels | jsonb | |
| started_at / resolved_at | timestamptz | INDEX(org_id, started_at) |

**dashboards** [OBS-09]
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK | |
| name | text | UNIQUE(org_id, name) |
| description | text nullable | |
| spec | jsonb | 패널 배열(쿼리·viz·layout) 단일 JSONB |
| created_by / created_at / updated_at | | |

### 2.6 리포트 (S4)

**reports**
| 컬럼 | 타입 |
|------|------|
| id | uuid PK |
| project_id | uuid FK |
| load_test_id | uuid FK nullable |
| scan_id | uuid FK nullable |
| performance_score / security_score | int |
| ai_summary | text |
| pdf_url | text |
| status | enum(generating, ready, failed) |
| created_at | timestamptz |

> **APM 스냅샷 소비 [OBS-10]**: 리포트는 상시 관측 데이터를 리포트 시점 구간 스냅샷으로 **조회·소비**할 뿐 소유하지 않는다. 관측 데이터의 수명·보존은 리포트와 무관.

**report_shares**
| 컬럼 | 타입 |
|------|------|
| id | uuid PK |
| report_id | uuid FK |
| slug | text UNIQUE |
| password_hash | text nullable |
| expires_at / revoked_at | timestamptz nullable |
| created_at | timestamptz |

### 2.7 과금 (Billing)

**plans**
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| code | enum(free, pro, enterprise) | |
| name | text | |
| max_vu | int | |
| max_duration_sec | int nullable | |
| monthly_test_limit | int nullable | |
| scan_features | jsonb | |
| apm_retention_days | int | **MVP 스냅샷 호환** — 상시는 아래 신호별 컬럼으로 승격(후속 deprecate) |
| obs_metrics_retention_days | int | 상시 메트릭 보존(예: Free 1d / Pro 14d / Ent 90d) |
| obs_traces_retention_days | int | 상시 트레이스 보존(비용 고려 더 짧게 가능) |
| obs_logs_retention_days | int | 상시 로그 보존 |
| obs_max_hosts | int nullable | 관측 호스트수 한도(NULL=무제한/Enterprise) |
| obs_max_ingest_gb_month | numeric nullable | 월 수집량 한도(secondary guard, 초과 시 차단 아님 — overage 과금) |
| obs_metrics_rollup_retention_days | int nullable | 다운샘플링 롤업(5m/1h) 시리즈 보존일(예: Ent 365d). raw는 `obs_metrics_retention_days` |
| price_cents | int | |

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

> **관측 과금 미터(2026-08-23 신설)**: 상시 관측은 VU-Minutes와 **병행**하는 별도 미터(월 활성 호스트수 primary + 수집 GB overage secondary)로 계량한다. 발행은 `klaro.usage.emitted`에 `meter`(vu_minutes / observability_hosts / observability_ingest_gb) 판별자로 구분하며, 계산·청구는 Billing 서비스 책임(상세 [04-cost-model.md §6](./04-cost-model.md)). 계량 원천은 `observability_usage_rollups`.

### 2.8 감사

**audit_logs** — SOC2 대비
| 컬럼 | 타입 | 비고 |
|------|------|------|
| id | uuid PK | |
| org_id | uuid FK | |
| actor_user_id | uuid FK nullable | |
| action | text | 예: load_test.create, member.role_change, **obs.key.issue/revoke/rotate, obs.rule.create** |
| resource_type / resource_id | text / uuid | |
| metadata | jsonb | ip, user_agent 등 |
| created_at | timestamptz | |

---

## 3. 데이터 격리 & 보존 규칙

- **RLS**: `org_id`를 가진 모든 테이블에 Row-Level Security 정책. 세션 `SET app.current_org = <uuid>`. 상시 관측 신규 테이블(observability_keys/hosts/usage_rollups, alert_rules/events, dashboards) 전부 포함.
- **보존 자동화([BILL-03])**: 플랜별 APM 보존일(Free 24h / Pro 14d / Enterprise 90d) 경과 데이터는 TSDB/Loki 리텐션 정책 + 배치 삭제 잡으로 제거.
  - **상시 관측(2026-08-23)**: 신호별 개별 보존(`plans.obs_*_retention_days`). 각 백엔드 native 리텐션은 최대 플랜 기준 전역 설정하고, 그보다 짧은 org/플랜 보존은 **CP `retentionjob`(cron)이 백엔드 delete API로 집행**.
  - **다운샘플링(2026-08-23 확정, 메트릭 한정)**: OSS VictoriaMetrics는 enterprise 다운샘플링 기능이 없어, **vmalert recording rule**로 5m/1h 집계 롤업 시리즈를 별도 생성해 `obs_metrics_rollup_retention_days`(장기, 예: 365d)로 보존한다. raw 보존(`obs_metrics_retention_days`, 짧음) 초과 구간 조회는 Explorer가 롤업 시리즈로 자동 폴백하며 응답에 `resolution`(raw/5m/1h) 필드로 표시한다. **트레이스·로그는 다운샘플링 대상이 아니다**(OSS 생태계에 표준 개념 부재) — 리텐션(보존·삭제)만 적용. 상세 `_workspace/05_architect_observability-design.md` HOW-10.
  - **쿼터 초과 동작(2026-08-23 확정)**: 호스트수·수집 GB 모두 **전 플랜 공통으로 차단하지 않고 overage 과금**(VU-Minutes와 동일 정책, [BILL-02]식이 아니라 병행 정책으로 통일).
- **테넌트 격리(상시)**: 저장소 계층에서 org로 키잉(VM AccountID / Tempo·Loki X-Scope-OrgID). CP만 테넌트 키 설정.
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
- `observability_keys(key_hash)` UNIQUE — Collector authz 빠른 조회.
- `observability_hosts(org_id, last_seen_at)` — 활성 호스트 카운트(쿼터).
- `alert_events(org_id, started_at)`, `alert_events(org_id, rule_id, state)` — 알림 이력.
