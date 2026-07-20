# P2-01 요구사항 계약 — S2 보안 스캔 실엔진 (Phase 2)

- 작성: spec-analyst · 대상 스킬: `klaro-spec-contract`
- 단일 진실 공급원: `docs/klaro/01-technical-design.md §2.3, §4`, `docs/klaro/02-data-model.md §2.4, §3`, `docs/klaro/03-api-spec.md §3.3`, `docs/klaro/04-cost-model.md`
- 확정 스택(입력): Go · 공용 DB+RLS · 큐=Redis(현행) · Docker Compose · 스캐너 전부 OSS(Semgrep/osv-scanner/OWASP ZAP, [COST-05] 무비용)
- 선행 상태: Phase 1 RLS 완료. 모든 org 스코프 write 는 `store.RunInOrg(job.OrgID, ...)` 로 `app.current_org` 세션변수 관통(`scanworker.go:54-58`). Phase 2 는 이 위에서 동작해야 한다.

> 이 계약은 WHAT + 수용 기준만 담는다. HOW(실행 방식·격리 구현·이미지 구성)는 `architect` 결정 사항으로 §결정 필요 목록에 분리했다.

---

## 범위

S2 를 스텁/수동 수준에서 설계 문서(`§2.3`) 수준의 실엔진으로 승격한다.

1. **SAST 실엔진** — Semgrep(코드 정적분석) + osv-scanner(의존성 취약점). 소스 체크아웃 필요.
2. **Ephemeral 소스 처리(불변식)** — 소스코드는 RAM(tmpfs)에만, 디스크/DB 저장 절대 금지, 잡 종료 시 소멸.
3. **DAST 능동 스캔** — OWASP ZAP headless(baseline/active). 기존 수동 헤더 분석과 통합. SC-01 도메인 검증 게이트 유지.
4. **결과 모델 확장** — finding 의 rule_id/severity/증거/재현정보, 스캔 score, 상태머신, 재스캔 triage 보존(finding_hash).

범위 밖(명시): S3/S4, PR 코멘트 게시(`§2.3` "결과를 PR 코멘트로 게시")는 GitHub App 연동에 종속 → §결정 필요. NATS 승격, 프로덕션 K8s 격리.

---

## 요구사항 계약

### [SAST-01] Semgrep 정적분석 실행
- 설명: 체크아웃된 소스 트리에 Semgrep 을 실행하여 코드 취약점을 탐지하고, 결과를 정규화된 `scan_findings` 로 저장한다. 현재의 `stubSAST`(고정 2건 placeholder)를 대체한다.
- 관련 ID: [SC-04](오탐 보존과 연계), SAST-01
- 수용 기준:
  - `type=sast` 스캔이 `stubSAST` 대신 실제 Semgrep 출력에서 파생된 finding 을 저장한다(`[STUB]` 접두 title 이 결과에 없다).
  - 각 finding 에 Semgrep 규칙 식별자(`rule_id`), `severity`, `file_path`, `line`, 코드 증거(스니펫/메시지)가 채워진다.
  - Semgrep 이 0건을 반환하면 스캔은 findings 0건으로 `completed`, score=100(무결점 상한).
  - Semgrep 실행 실패(비정상 종료/타임아웃)는 스캔을 `failed` 로 전이하고 소스는 소멸([EPHEM-*]).
- 데이터 모델: `scan_findings(rule_id, severity, title, file_path, line, finding_hash, status)` + 증거 컬럼 확장([SC-05]).
- API: `POST /projects/:id/scans {type:"sast", ...}` → 202 `{id,status}`, `GET /scans/:id/findings`.

### [SAST-02] osv-scanner 의존성 취약점 스캔
- 설명: 체크아웃된 소스의 의존성 매니페스트/락파일(예: go.mod/package-lock/requirements 등 osv-scanner 지원 포맷)을 스캔하여 알려진 취약점(OSV/CVE)을 finding 으로 저장한다.
- 관련 ID: SAST-02, [SC-04]
- 수용 기준:
  - `type=sast` 스캔이 Semgrep(코드) 결과와 osv-scanner(의존성) 결과를 **하나의 스캔** 아래 통합 저장한다.
  - osv finding 은 취약 패키지·버전·권고 ID(OSV/CVE)를 증거로 포함하고, `rule_id` 로 osv 출처를 식별할 수 있다(예: `osv.<advisory>` 네임스페이스).
  - 락파일이 없거나 지원 포맷이 없으면 osv 파트는 0건이며 스캔을 실패시키지 않는다.
- 데이터 모델: `scan_findings`(위와 동일 + 증거 컬럼).
- API: 동일(SAST 스캔에 포함).

### [SAST-03] 소스 체크아웃(입력 소스 확보)
- 설명: SAST 스캔이 분석할 소스 트리를 잡 시작 시 확보한다. 확보 경로/방식(사용자 제공 아카이브 vs 공개 저장소 clone vs GitHub App PR 체크아웃)은 미확정 → §결정 필요.
- 관련 ID: SAST-03, [EPHEM-01]
- 수용 기준:
  - SAST 스캔 생성 요청은 분석 대상 소스를 식별할 입력(현행 `pr_number` 또는 대체 입력)을 요구하며, 입력이 없으면 `400 VALIDATION_ERROR` (현행은 SAST 입력 검증이 없다 — 격차 참조).
  - 체크아웃된 소스는 tmpfs 경로에만 존재하고 잡 종료 시 소멸([EPHEM-01]).
- 데이터 모델: `scans(pr_number)` (현행) + 소스 참조 확장 여부는 §결정 필요.
- API: `POST /projects/:id/scans {type:"sast", pr_number:42}` (`03-api-spec.md:194-197`). GitHub 웹훅 트리거 `POST /webhooks/github`(`03-api-spec.md:288`)는 §결정 필요.

### [EPHEM-01] Ephemeral 소스 처리(불변식, MUST)
- 설명: 스캔 대상 소스코드는 RAM(tmpfs)에만 존재하고, 디스크·DB·오브젝트 스토리지에 절대 기록되지 않으며, 잡 종료(성공/실패/중단) 시 완전히 소멸한다. Docker Compose 로컬 환경에서 이 불변식을 실현한다(워커 컨테이너 tmpfs 마운트 또는 잡별 tmpfs — 방식은 §결정 필요).
- 관련 ID: [EPHEM-01] (설계 `§2.3`, `§4` "소스코드 처리 = Ephemeral(RAM), 디스크 미저장, 잡 종료 시 소멸", `02-data-model.md:289`)
- 수용 기준(검증 가능):
  - 소스 콘텐츠(파일 본문/아카이브/클론)가 `scans`·`scan_findings` 어떤 컬럼에도 저장되지 않는다(스키마·저장 코드 레벨에서 소스 본문 컬럼 부재).
  - 소스 작업 디렉터리는 tmpfs 백엔드 경로에만 위치한다(호스트/명명 볼륨/이미지 레이어가 아님).
  - 스캔 종료(completed/failed) 이후 해당 작업 디렉터리가 남지 않는다(잡별 정리 실행 확인).
  - finding 의 `file_path`/`line`/증거는 **경로·스니펫 메타데이터만** 보관하며 전체 소스 사본을 구성하지 않는다.
- 데이터 모델: `scans`, `scan_findings` (소스 본문 컬럼 없음 유지).
- API: 없음(런타임 불변식).

### [DAST-01] OWASP ZAP 능동 스캔
- 설명: 검증된 대상 URL 에 대해 OWASP ZAP headless 로 baseline/active 스캔을 실행하여 취약점을 finding 으로 저장한다.
- 관련 ID: DAST-01, [SC-01]
- 수용 기준:
  - `type=dast` 스캔이 ZAP 결과에서 파생된 finding 을 저장하며, ZAP 규칙 식별자(`rule_id`)·`severity`·증거(요청/응답 근거·URL·파라미터)를 포함한다.
  - baseline(수동적, 안전) vs active(능동 공격 페이로드) 모드 선택이 가능해야 한다(active 는 검증 도메인 게이트 하에서만). 모드 기본값·노출 방식은 §결정 필요.
  - ZAP 실행 실패/타임아웃은 스캔을 `failed` 로 전이한다.
- 데이터 모델: `scan_findings`(증거 컬럼 포함).
- API: `POST /projects/:id/scans {type:"dast", target_url}` (`03-api-spec.md:193`).

### [DAST-02] 수동 헤더 분석 통합/유지
- 설명: 기존 `AnalyzeHeaders`(HSTS/CSP/X-Frame-Options 등 순수 함수, `scanworker.go:114-172`)의 결과를 DAST 스캔 결과에 계속 포함한다(ZAP 결과와 통합).
- 관련 ID: DAST-02
- 수용 기준:
  - DAST 스캔 결과에 헤더 자세(posture) finding 이 ZAP finding 과 함께 하나의 스캔 아래 존재한다.
  - 헤더 분석은 순수 함수 특성(동일 입력→동일 finding_hash)을 유지한다(회귀 없음).
  - ZAP 와 헤더 분석이 동일 이슈를 이중 보고하지 않도록 rule_id 네임스페이스로 구분한다(중복 정책은 §결정 필요).
- 데이터 모델: `scan_findings`.
- API: 동일.

### [SC-01] 도메인 소유권 검증 게이트(불변식, MUST)
- 설명: DAST(및 active) 잡은 `verified_domains` 에 검증된 도메인에만 생성 허용. 미검증이면 거부.
- 관련 ID: [SC-01] (`01-technical-design.md:95-98`)
- 수용 기준:
  - 미검증 도메인 대상 DAST 생성 → `403 DOMAIN_NOT_VERIFIED` (현행 유지, `scans.go:46-54`).
  - `target_url` 누락/무효 → `400 VALIDATION_ERROR` (현행 유지, `scans.go:37-45`).
  - Phase 2 에서 게이트 로직이 약화되지 않는다(active 스캔에도 동일 게이트 적용).
- 데이터 모델: `verified_domains`(Phase 1), `store.IsDomainVerified`.
- API: `POST /projects/:id/scans`.

### [SC-05] 결과 모델 확장(증거·재현정보)
- 설명: `scan_findings` 를 rule_id/severity 를 넘어 증거(evidence)·재현정보(location/request/param/cwe/confidence 등)까지 담도록 확장하여 findings 가 실엔진 출력(Semgrep/osv/ZAP)을 충실히 표현하게 한다.
- 관련 ID: SC-05 (신설), `02-data-model.md:171-184`
- 수용 기준:
  - 각 finding 이 스캐너별 증거 필드를 담는다(SAST: 코드 스니펫/메시지, osv: 패키지·버전·권고 ID, DAST: URL·파라미터·요청/응답 근거).
  - 확장 컬럼은 `org_id` + RLS 정책 하에 저장된다(비정규화 D-9 유지, `02-data-model.md:287`).
  - 기존 finding JSON 계약(`model/scan.go:82-94`)과 하위호환(신규 필드는 optional/omitempty).
  - 구체 컬럼 형태(개별 컬럼 vs jsonb `evidence`)는 §결정 필요.
- 데이터 모델: `scan_findings` 신규 마이그레이션(0002 이후 번호).
- API: `GET /scans/:id/findings` 응답에 신규 필드 포함.

### [SC-04] 재스캔 triage 보존(finding_hash)
- 설명: 재스캔 시 finding 을 `finding_hash` 로 매칭해 이전 triage 상태(`ignored`/`fixed` + 사유)를 유지한다.
- 관련 ID: [SC-04] (`01-technical-design.md:107`, `02-data-model.md:181,299`)
- 수용 기준:
  - 동일 프로젝트를 재스캔했을 때, 이전에 `ignored`(사유 포함) 처리된 것과 동일 `finding_hash` 의 신규 finding 은 `ignored` 상태·사유를 승계한다.
  - 이전 스캔에 있었으나 이번 스캔에 없는 finding 은 `fixed`(해소)로 판정 가능해야 한다(판정 주체/시점은 §결정 필요).
  - `finding_hash` 는 실엔진 출력에서도 안정적으로 산출된다(SAST: rule_id+파일:라인, DAST: rule_id+URL/param). 라인 시프트에 대한 안정성 정책은 §결정 필요.
- 데이터 모델: `scan_findings(finding_hash)` 인덱스(`0002_scans.sql:31`), `model.FindingHash`(`scan.go:107-110`).
- API: `PATCH /scans/:id/findings/:findingId {status, ignore_reason}` (현행, `scans.go:114-138`).

### [SC-06] 스캔 상태머신(엔진별 단계)
- 설명: 스캔 생명주기가 실엔진 파이프라인(체크아웃/실행/집계)을 표현하도록 상태머신을 검토·확장한다.
- 관련 ID: SC-06 (신설), `02-data-model.md:167`
- 수용 기준:
  - 현행 enum `pending→running→completed/failed`(`scan.go:29-44`, `0002_scans.sql:9`)은 유지하되, 잡의 org tx 관통·불법 전이 거부(`ErrIllegalTransition`, `scans_store.go:69`) 특성이 실엔진에서도 보장된다.
  - 타임아웃/리소스 초과/스캐너 실패는 모두 `failed` 로 관측 가능하게 전이한다.
  - 부하 잡의 확장 상태머신(`REJECTED/PROVISIONING` 등)을 스캔에도 도입할지는 §결정 필요(현행 스캔은 축약 머신).

### [SC-07] 스캔 실행 리소스·타임아웃 제한
- 설명: Semgrep/osv-scanner/ZAP 실행에 시간·리소스 상한을 두어 워커가 무한 점유되지 않게 한다(플랫폼 폭주 방지).
- 관련 ID: SC-07 (신설), `01-technical-design.md:156`(보호 이중역할)
- 수용 기준:
  - 각 스캐너 실행에 타임아웃이 있고, 초과 시 프로세스가 종료되며 스캔은 `failed`, 소스는 소멸([EPHEM-01]).
  - 구체 상한값(초/CPU/메모리)과 동시 스캔 수 제한은 §결정 필요.

---

## 불변식 체크리스트 (MUST)

- [x] **Ephemeral 소스** — [EPHEM-01]. 소스는 tmpfs 전용, DB/디스크 미기록, 잡 종료 시 소멸. **가장 중요한 Phase 2 불변식.**
- [x] **도메인 소유권 검증 게이트** — [SC-01]. DAST/active 는 검증 도메인만. 현행 게이트 약화 금지.
- [x] **RLS 멀티테넌시** — 모든 scan/finding write 는 `org_id` + `RunInOrg`(`app.current_org`) 관통(`scanworker.go:54-58,82-84`). 확장 컬럼도 동일.
- [x] **잡 상태머신** — 불법 전이 거부 유지(`scans_store.go:69`).
- [x] **시계열 RDB 분리** — 해당 없음(스캔 결과는 정형 finding, RDB 정상). 소스 본문·대용량 원시 로그를 RDB 에 넣지 않는다.
- [x] **비용 게이트 [COST-05]** — Semgrep/osv-scanner/ZAP 는 전부 OSS·로컬 실행 ₩0(`04-cost-model.md:18`). Bedrock 외 신규 유료 외부 호출 추가 금지.
- [ ] **mTLS / 워커 idle=0 / 서킷 브레이커** — S2 스캔 범위 밖(부하 S1 불변식). 단, 스캔 워커 정리(잡 종료 후 소스·프로세스 회수)는 [EPHEM-01]/[SC-07]로 대응.

---

## 기존 코드 격차 (file:line)

- **SAST 스텁** — `internal/worker/scanworker.go:72-73, 216-241`: `stubSAST` 가 고정 2건 `[STUB]` finding 반환. Semgrep/osv-scanner·체크아웃 전무 → [SAST-01/02/03].
- **DAST 수동 한정** — `internal/worker/scanworker.go:94-172`: `runDAST`+`AnalyzeHeaders` 는 응답 헤더 자세만. ZAP 능동 스캔 없음 → [DAST-01]. (헤더 분석은 [DAST-02]로 유지.)
- **SAST 입력 검증 부재** — `internal/api/scans.go:57-64`: SAST 는 `pr_number` 유무로 trigger 만 나누고, 소스 식별 입력을 **요구하지 않음**. `pr_number` 없이도 생성되어 빈 소스로 스캔 가능 → [SAST-03] 수용 기준 위배 상태.
- **ScanJob 에 소스 참조 없음** — `internal/model/scan.go:96-103`, `internal/api/scans.go:71`: `ScanJob{ScanID,OrgID,ProjectID,Type,TargetURL}` 만 큐에 실림. SAST 소스(레포/PR/커밋/아카이브) 전달 필드 부재 → [SAST-03].
- **finding 증거 필드 부재** — `internal/model/scan.go:82-94`, `migrations/0002_scans.sql:17-29`: `rule_id/severity/title/file_path/line/finding_hash` 만. evidence/cwe/confidence/패키지·버전 없음 → [SC-05].
- **Ephemeral 인프라 부재** — `docker-compose.yml:46-56`: worker 서비스에 tmpfs 마운트/잡별 임시공간 정의 없음 → [EPHEM-01].
- **스캐너 바이너리 부재** — worker 이미지(`Dockerfile.worker`)에 Semgrep/osv-scanner/ZAP 미포함(추정, 확인 필요) → [SAST-*/DAST-01] 실행 전제.
- **스캔 워커는 idle 상시 goroutine** — `cmd/worker/main.go:48-51`: `go worker.RunScan(...)` 상주 루프. 잡별 정리(소스 소멸) 훅 없음 → [EPHEM-01]/[SC-07] 정리 로직 필요.
- **축약 상태머신** — `internal/model/scan.go:29-44`: 부하 잡 대비 단순. 실엔진 단계 표현 여부 → [SC-06] 검토.

---

## architect 결정 필요 목록

1. **SAST 소스 확보 경로** (핵심): (a) 사용자 제공 아카이브 업로드, (b) 공개 저장소 URL clone, (c) GitHub App PR 체크아웃 중 Phase 2 착수 범위. `github_installations` 는 Phase 1 제외됨(`02-data-model.md:70,88` nullable). `POST /webhooks/github`(`03-api-spec.md:288`) PR 트리거·PR 코멘트 게시(`§2.3`)를 Phase 2 에 포함할지.
2. **Ephemeral tmpfs 실현 방식** (Docker Compose): worker 컨테이너 `tmpfs:` 마운트(공유) vs 스캔 잡별 별도 임시 컨테이너/tmpfs vs Go `os.MkdirTemp` on tmpfs 마운트 경로. 잡 종료 시 소멸 보장·검증 방법.
3. **스캐너 실행 방식**: worker 이미지에 Semgrep/osv-scanner/ZAP 바이너리 번들 vs 사이드카/별도 컨테이너 exec vs docker-in-docker. 로컬 무비용·Compose 제약 하 선택.
4. **스캐너 리소스·타임아웃 상한**: 스캐너별 타임아웃(초), CPU/메모리 제한, 워커 동시 스캔 수 한도([SC-07] 구체값).
5. **결과 증거 스키마 형태**: `scan_findings` 확장 컬럼 vs jsonb `evidence` 컬럼([SC-05]). cwe/confidence/package/version 표현.
6. **severity 매핑 규칙**: Semgrep/osv/ZAP 각 심각도 → klaro `critical|high|medium|low|info` 매핑 테이블. `ComputeScore` 가중치(`scan.go:112-119`) 재검토 필요 여부.
7. **finding_hash 안정성 정책**: SAST 라인 시프트/파일 이동 시 동일 이슈 매칭 유지 전략([SC-04]).
8. **재스캔 fixed 판정 주체/시점**: 워커가 자동 fixed 처리 vs 조회 시 계산([SC-04]).
9. **DAST active 노출**: baseline/active 모드 API 파라미터 노출 여부·기본값. active 안전장치(요청량·게이트).
10. **DAST 중복 정책**: ZAP vs 헤더 분석 finding 중복 제거/우선순위([DAST-02]).
11. **스캔 상태머신 확장 여부**: 축약(pending→running→…) 유지 vs 부하 잡류 확장 상태 도입([SC-06]).

---

## 모호 / 모순

- **SAST 트리거와 소스 입력 불일치**: `03-api-spec.md:194` 는 SAST 를 `{type:"sast", pr_number:42}`(PR 전제)로 예시하지만, 현행 API(`scans.go:59-64`)는 `pr_number` 를 optional(있으면 pr, 없으면 manual)로 처리하고 소스 식별을 강제하지 않는다. 문서는 GitHub App(PR)만 SAST 소스 경로로 서술(`§2.3`)하나 `github_installations` 는 Phase 1 제외 → PR 없는 수동 SAST 의 소스 출처가 문서에 미정의. → 결정 필요 #1.
- **PR 코멘트 게시**(`01-technical-design.md:103`)는 GitHub App 연동 종속. Phase 1 에서 연동 제외되었으므로 Phase 2 범위 포함 여부가 문서만으로는 불명. → 결정 필요 #1.
- **워커 격리**(`01-technical-design.md:134` "잡별 네임스페이스/네트워크 폴리시")는 K8s 전제 서술이나 로컬은 Compose. 로컬에서의 스캔 워커 격리 등가물이 문서에 없음 → 결정 필요 #2/#3.
- **상태머신 표기 차이**: CLAUDE.md 는 부하 잡의 확장 상태머신(`PENDING→VALIDATING→…→COMPLETED`, `REJECTED`)을 불변식으로 명시하나, 스캔은 `02-data-model.md:167`·현행 코드 모두 축약 4상태. 스캔에 확장 머신 적용 의도인지 문서상 불명확 → 결정 필요 #11.
- **score 산정 규칙**: `ComputeScore`(`scan.go:121-132`)는 코드에만 존재하고 설계 문서에 명시된 점수 공식 근거가 없음(문서는 "보안 점수" 컬럼만 언급, `02-data-model.md:168`). 실엔진 finding 급증 시 score 하한 0 고착 가능 — 재검토 대상. → 결정 필요 #6.
