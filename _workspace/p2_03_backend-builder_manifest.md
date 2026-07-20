# P2-03 backend-builder 매니페스트 — S2 보안 스캔 실엔진 (Phase 2)

- 작성: backend-builder · 작성일 2026-07-20 · 상태: 구현 완료(GitHub App 연동은 명시적 후속)
- 입력: `_workspace/p2_02_architect_design.md`(단일 진실 공급원), `_workspace/p2_01_spec-analyst_contract.md`
- 대상 모듈: `services/load-test`(`github.com/klaro/load-test`) — `internal/scanner` 신설 + `scanworker` 개조
- 빌드 순서: 설계 §7 (1~8) 그대로 수행, 각 단계 종료 시 `go build/vet` 그린 게이트 통과.

---

## 1. 추가/수정 파일

### 신규
| 파일 | 내용 |
|------|------|
| `migrations/0009_scan_engine.sql` | scans/scan_findings 컬럼 확장(§3) |
| `internal/scanner/scanner.go` | 인터페이스(SAST/DAST), `Source`, `Mode`, 타임아웃 상수 |
| `internal/scanner/source.go` | Ephemeral 소스 확보 `Acquire`(clone/extract) + zip/tar 추출(zip-slip 방어·크기 상한) |
| `internal/scanner/tmpfs_linux.go` | `AssertTmpfs`(unix.Statfs, TMPFS_MAGIC) — linux 빌드 전용 |
| `internal/scanner/tmpfs_other.go` | `AssertTmpfs` no-op — 비-linux(개발 호스트) 빌드 |
| `internal/scanner/normalize.go` | 스니펫 정규화·repo상대경로·ptr 헬퍼 |
| `internal/scanner/mapping.go` | severity 매핑(Semgrep/CVSS/osv/ZAP) |
| `internal/scanner/headers.go` | `AnalyzeHeaders` 이관 + rule_id `dast.header.*` 개명 + evidence |
| `internal/scanner/semgrep.go` | Semgrep exec + `parseSemgrep`(JSON→finding) |
| `internal/scanner/osv.go` | osv-scanner exec + `parseOSV` |
| `internal/scanner/zap.go` | ZAP REST 클라이언트(spider→ascan?→alerts) + `parseZAPAlerts` |
| `internal/scanner/dedup.go` | `DedupDAST`(§6.5 ZAP 우선 억제) |
| `internal/scanner/parse_test.go` | 스캐너 JSON 픽스처 파싱·dedup·hash 안정성 유닛(바이너리·네트워크 불필요) |
| `internal/scanner/source_test.go` | 업로드 추출·cleanup·zip-slip 거부·미존재 staging 유닛 |
| `internal/worker/scan_integration_test.go` | `//go:build integration`: Ephemeral cleanup / NoSourceInDB / 재스캔 triage |

### 수정
| 파일 | 변경 |
|------|------|
| `internal/model/scan.go` | `ScanFinding` 증거 필드(cwe/confidence/package/package_version/evidence), `Scan`에 source_type/source_ref/mode, `ScanSource`, `ScanJob.Source/Mode`, `FindingHashParts`, `ComputeScore` 개편(open만·버킷상한) |
| `internal/model/scan_test.go` | ComputeScore 버킷상한/ignored·fixed 제외 케이스, `FindingHashParts` 테스트 |
| `internal/store/scans_store.go` | CreateScan/SaveFindings/GetScan/ListFindings 신규 컬럼, `GetLatestCompletedScanFindings` 신설, `jsonbArg` 헬퍼 |
| `internal/worker/scanworker.go` | **전면 개조**: stubSAST/헤더코드 제거, `runSAST`(Acquire+cleanup, Semgrep∥osv), `runDAST`(header+ZAP dedup), 세마포어, zapMu, `applyTriage`, panic 안전망. `ScanDeps`는 포인터 수신(mutex 포함) |
| `internal/worker/scan_analysis_test.go` | **삭제**(헤더 테스트는 scanner로 이관, stubSAST 삭제) |
| `internal/api/scans.go` | createScan SAST XOR 검증·DAST mode, `uploadScanSource` 핸들러, staging 검증 헬퍼 |
| `internal/api/router.go` | `POST /projects/:id/scans/source` 라우트(min=member) |
| `cmd/worker/main.go` | 스캐너/디렉터리/동시성 주입 + 기동 `AssertTmpfs`(SCAN_WORK_TMPFS_ASSERT=0 우회) + ZAP_ADDR 미설정 시 header-only |
| `Dockerfile.worker` | 베이스 grafana/k6→python:3.12-slim, Semgrep(pip)/osv-scanner(정적)/git/semgrep-rules 번들, k6 멀티스테이지 COPY 보존 |
| `docker-compose.yml` | `zap` 서비스, worker `/scan-work` tmpfs·`scan_src` 공유 tmpfs 볼륨·ZAP_ADDR·동시성·리소스 limits, api scan_src 마운트 |
| `docs/klaro/02-data-model.md` §2.4 | 신규 컬럼 행 + 0009 주석 |
| `docs/klaro/03-api-spec.md` §3.3/§4 | 업로드 라우트·SAST 소스 입력·DAST mode·findings 응답, 웹훅 후속 표기 |

---

## 2. 신규/변경 엔드포인트 (qa 참조 — 요청/응답 shape)

### 신규: `POST /projects/:id/scans/source` (min=member)
- Content-Type: `multipart/form-data`, field `file` = 아카이브(`.tar`/`.tar.gz`/`.tgz`/`.zip`, ≤200MB)
- 201: `{ "upload_token": "<32hex>", "expires_in": 3600 }`
- 400 VALIDATION_ERROR: file 필드 없음 / 미지원 확장자 / 업로드 초과·중단
- 아카이브 바이트는 공유 tmpfs(`$SCAN_SRC_DIR`)에만 기록, DB/디스크 미기록([EPHEM-01]).

### 변경: `POST /projects/:id/scans` (min=member)
요청:
```jsonc
// DAST
{ "type": "dast", "target_url": "https://staging.example.com", "mode": "baseline|active" } // mode 생략 시 baseline
// SAST — repo_url XOR upload_token 필수
{ "type": "sast", "repo_url": "https://github.com/acme/app", "ref": "main" }
{ "type": "sast", "upload_token": "<token>" }
```
응답:
- 202: `{ "id": "<uuid>", "status": "pending" }`
- 400 VALIDATION_ERROR: type 오류 / DAST target_url 누락·무효 / **SAST 소스 XOR 위반(둘 다 또는 둘 다 없음)** / upload_token staging 부재
- 403 DOMAIN_NOT_VERIFIED: DAST(baseline·active 공통) 대상 도메인 미검증([SC-01])

### 변경: `GET /scans/:id/findings` (min=viewer)
- 200: `{ "data": [ ScanFinding... ] }` — 신규 필드 `cwe`,`confidence`,`package`,`package_version`,`evidence`(jsonb) 는 omitempty. rule_id 네임스페이스: `sast.semgrep.*` / `osv.*` / `zap.*` / `dast.header.*`.

### 유지(무변경): `GET /projects/:id/scans`, `GET /scans/:id`, `PATCH /scans/:id/findings/:findingId`

---

## 3. 마이그레이션 0009 요약
- `scans`: `+source_type text`(CHECK repo|upload), `+source_ref text`, `+mode text`(CHECK baseline|active)
- `scan_findings`: `+evidence jsonb`, `+cwe text`, `+confidence text`, `+package text`, `+package_version text`
- 전부 `ADD COLUMN IF NOT EXISTS`(nullable), CHECK 는 `pg_constraint` 가드 DO 블록 → 재실행 안전.
- org_id/RLS: 0005/0006 의 `org_isolation` FORCE RLS 는 ADD COLUMN 영향 없음 → 확장 컬럼 자동 보호. 소스 본문 컬럼 신설 없음([EPHEM-01]).

---

## 4. 실행 / 검증 방법

### compose 재기동(0009 적용 — 스키마 변경이므로 `down -v` 필수)
```bash
cd services/load-test
docker compose down -v && docker compose up -d --build   # zap·worker(스캐너 번들)·api 포함
# 컬럼 확인
docker exec load-test-postgres-1 psql -U klaro -d klaro -c "\d scans"
docker exec load-test-postgres-1 psql -U klaro -d klaro -c "\d scan_findings"
```

### SAST(업로드) 스캔
```bash
# 1) 소스 업로드 → upload_token
curl -s -H "Authorization: Bearer dev" -H "X-Org-Id: <org>" \
  -F "file=@app.zip" http://localhost:8080/projects/<pid>/scans/source
# 2) 스캔 생성
curl -s -H "Authorization: Bearer dev" -H "X-Org-Id: <org>" -H "Content-Type: application/json" \
  -d '{"type":"sast","upload_token":"<token>"}' http://localhost:8080/projects/<pid>/scans
# 3) findings 조회
curl -s -H "Authorization: Bearer dev" -H "X-Org-Id: <org>" http://localhost:8080/scans/<id>/findings
```
SAST(repo): `{"type":"sast","repo_url":"https://github.com/OWASP/NodeGoat","ref":"master"}`
DAST(baseline/active): `{"type":"dast","target_url":"https://<verified-domain>/","mode":"active"}`

### 테스트
```bash
# 유닛(바이너리·네트워크·DB 불필요)
go build ./... && go vet ./... && go test ./...
# integration(Postgres 필요, 스캐너 바이너리 불필요 — nil/fake 스캐너)
docker compose up -d postgres redis
TEST_DATABASE_URL="postgres://klaro_app:klaro_app@localhost:5432/klaro?sslmode=disable" \
TEST_SYSTEM_DATABASE_URL="postgres://klaro_system:klaro_system@localhost:5432/klaro?sslmode=disable" \
SCAN_WORK_TMPFS_ASSERT=0 go test -tags integration ./internal/worker/ ./internal/store/ -v
```

### 검증 결과(본 세션 실측)
- `go build ./...` : **그린**
- `go vet ./...` : **그린**(linux 크로스컴파일 `GOOS=linux go build ./internal/scanner/` 도 그린 — x/sys/unix 확인)
- `go test ./...`(유닛) : **전 패키지 ok** (scanner 파싱/dedup/hash, model ComputeScore 버킷상한, source 추출/zip-slip 등)
- integration : `TestEphemeralCleanup` / `TestNoSourceInDB` / `TestRescanTriage` **PASS**, 기존 store RLS 테스트 회귀 없음
- 0009 initdb 적용 확인: scans(source_type/source_ref/mode) + CHECK 2건, scan_findings(evidence/cwe/confidence/package/package_version) 존재 확인
- `docker compose config -q` : 유효

---

## 5. Ephemeral([EPHEM-01]) 검증 근거
- **tmpfs 전용**: worker `/scan-work` = compose `tmpfs:`(RAM), 업로드 staging `scan_src` = `driver_opts type=tmpfs`(RAM). 어느 경로도 명명 볼륨/디스크/이미지 레이어에 소스 미기록.
- **소멸 보장**: `Acquire` 직후 `defer cleanup()` 등록 → 정상/에러/타임아웃/ctx취소/panic 전 경로에서 `os.RemoveAll(dir)` 실행. 업로드 staging 은 추출 즉시 삭제 + cleanup 재삭제(idempotent). `processScan` 최상단 `recover` 로 panic 시에도 status=failed + cleanup 도달.
- **기동 가드**: `cmd/worker/main.go` 가 `scanner.AssertTmpfs(SCAN_WORK_DIR)` 로 tmpfs 아니면 `log.Fatal`. 비-tmpfs(로컬 go test)는 `SCAN_WORK_TMPFS_ASSERT=0` 우회.
- **스키마 부재**: scans/scan_findings 에 소스 본문 컬럼 없음. `evidence` 는 스니펫 ≤10줄(`SnippetMaxLines`)만. `source_ref` 는 repo URL/파일명 포인터.
- **테스트 증거**: `TestEphemeralCleanup`(성공·강제실패 후 `/scan-work` 잡 디렉터리 0개), `TestNoSourceInDB`(소스 콘텐츠 canary 를 scans·scan_findings 전 text/jsonb 컬럼에서 검색 → 0건).

## 6. 불변식 준수 근거
| 불변식 | 근거 |
|--------|------|
| [EPHEM-01] | §5 전체 |
| [SC-01] 도메인 게이트 | `createScan` DAST 분기에서 baseline·active 공통 `IsDomainVerified` 게이트 유지, 미검증 403. 약화 없음 |
| RLS 멀티테넌시 | 신규/개조 store 전부 `q Querier`, 워커 `RunInOrg(job.OrgID,…)`. `GetLatestCompletedScanFindings`·`SaveFindings` 모두 org tx 내. 확장 컬럼은 기존 FORCE RLS 자동 적용 |
| 잡 상태머신 | `pending→running→completed/failed` 축약 유지(#11), `CanScanTransition`·`ErrIllegalTransition` 불변 |
| [COST-05] 비용 게이트 | Semgrep/osv-scanner/ZAP/git 전부 OSS·빌드타임 번들·런타임 무네트워크. semgrep `--config $SEMGREP_RULES_DIR`(번들 규칙), `--config auto` 미사용. Bedrock 외 신규 유료 의존 0 |
| S1 회귀 방지 | Dockerfile 베이스 변경 후 k6 바이너리 멀티스테이지 COPY 보존, `K6_PATH=k6` 유지. 부하 워커 코드 무변경 |
| 헤더 rule_id 개명 | `dast.*`→`dast.header.*`. 기존 worker 테스트 삭제, scanner `TestAnalyzeHeadersNamespaced` 로 기대값 갱신(설계 §6.5) |

## 7. 미완료 / 후속 (명시적 제외)
- **GitHub App 연동**: PR 체크아웃·PR 코멘트 게시·`POST /webhooks/github` 서명검증 — 후속 GitHub 연동 페이즈(설계 결정 #1). Phase 2 SAST 는 repo clone/업로드만.
- **staging TTL 스위퍼**: 미소비 업로드 staging 회수(mtime>expires 삭제)는 tmpfs 볼륨 자체가 컨테이너 수명과 함께 소멸하므로 MVP 에서 별도 데몬 미구현. `expires_in=3600` 은 응답 힌트값(강제 만료 스위퍼 후속).
- **osv-scanner CVSS 벡터 파싱**: 현재 numeric score 우선, 벡터는 `database_specific.severity` 라벨 폴백, 미상→medium. 정밀 CVSS 벡터 계산은 후속.
- **ZAP 실엔진 e2e**: ZAP daemon 통합 e2e 는 compose 기동 후 수동 검증 절차로 문서화(자동 integration 테스트는 파싱 유닛 + nil/fake 로 커버, ZAP 바이너리 의존 e2e 는 별도).
- **go.mod**: `golang.org/x/sys` 는 indirect 로 유지(빌드/벳 그린). 필요 시 `go mod tidy` 로 direct 승격.

---

## 재감사 수정 내역 (C-1 / H-1 / D-1 / M-1 / M-2)

P2-06 invariants-reviewer 판정(BLOCK) + P2-05 QA(D-1)를 반영해 아래를 수정했다. 각 결함별 수정 파일·라인·검증.

### C-1 (critical) — SAST repo_url git 인자/전송 주입 (RCE/SSRF) 차단
- 신규 `internal/scanner/validate.go`: `ValidateRepoURL`(scheme=**https만**, http/git/ssh/ext/file/ftp 거부, host 필수, 선행 `-` 거부), `ValidateRef`(선행 `-`·제어문자·` ~^:?*[\` 거부), `isBlockedIP`(loopback/RFC1918+ULA/link-local 169.254·fe80/unspecified 차단 = **SSRF 게이트**, IP 리터럴+DNS 해석 양쪽).
- `internal/scanner/source.go` `gitClone`(구 69-81): `ValidateRepoURL`/`ValidateRef` 선검증 + `git -c protocol.ext.allow=never -c protocol.file.allow=never -c protocol.ftp(s).allow=never clone --depth 1 --single-branch --no-tags ... -- <url> <dir>`(**`--` 구분자로 인자 주입 차단**), `GIT_TERMINAL_PROMPT=0`·`GIT_ASKPASS=/bin/true`·`GIT_CONFIG_NOSYSTEM=1`·`GIT_CONFIG_GLOBAL=/dev/null`.
- `internal/api/scans.go` createScan SAST repo 분기: 큐 적재 **전** `scanner.ValidateRepoURL`/`ValidateRef` 호출 → 실패 시 400 VALIDATION_ERROR(빠른 거부). `scrubValidationErr` 로 안전 메시지.
- 검증: 유닛 `TestValidateRepoURLRejects`(ext::/file:///169.254/10.x/127/::1/git://ssh:// / `--upload-pack=` / 선행 `-` 전부 `ErrBlockedRepoURL`), `TestValidateRef`; 통합 `TestScanRepoURLInjectionRejected`(API 8종 → 전부 **400**) **PASS**.

### H-1 (high) — 검증 없는 아웃바운드 clone (SSRF)
- C-1의 `isBlockedIP` 게이트로 해소. 내부/메타데이터(169.254.169.254 포함) 대상 clone 은 https 라도 IP 해석 시 차단. 같은 유닛/통합 테스트로 실증.

### D-1 (high, QA) — semgrep 전체 룰(2151개) 300s 타임아웃 → SAST 상시 실패
- `internal/scanner/semgrep.go`: `detectConfigs(srcDir)` 신설 — 소스 트리 확장자 walk(`.git`/`node_modules`/`vendor` 등 skip, 파일 캡 5만)로 **사용 언어 감지 후 해당 언어 룰 서브디렉터리만** `--config` 지정(예 `/opt/semgrep-rules/python`). 언어 미감지 → 0건(에러 아님). rule_id 는 `normalizeCheckID` 로 경로 노이즈 제거(`sast.semgrep.opt.semgrep-rules.python…` → `sast.semgrep.python…`).
- `internal/scanner/scanner.go`: `SemgrepTimeoutSec` 300→**900**(헤드룸; 주 수정은 룰 축소).
- 검증: 유닛 `TestSemgrepDetectConfigs`(python/go만 선택, node_modules js 제외), `TestSemgrepDetectNoLanguage`, `TestNormalizeCheckID` **PASS**. 바이너리 통합 `TestSemgrepExecFindsPythonVuln`(`//go:build integration`) — 3줄 `subprocess(shell=True)` 파이썬 파일이 **180s 내 finding 반환 + rule_id 정규화 확인**(semgrep 바이너리 있는 환경, 없으면 skip). QA 대조군(python 337규칙=49s)과 일치.

### M-1 (medium) — osv-scanner 오프라인 미보장 + 에러의 조용한 0건 흡수
- `internal/scanner/osv.go`: `OSV{Offline, LocalDBPath}` 추가. Offline 시 `--offline` + `OSV_SCANNER_LOCAL_DB=<path>` env. **exit 코드 구분**: 0/1→파싱, 128("no package sources")→0건(정상, [SAST-02]), **그 외 non-zero → 에러 표면화(스캔 failed)**. 바이너리 미기동(non-ExitError)도 에러. stderr 캡처해 메시지 포함.
- `cmd/worker/main.go`: `OSV_OFFLINE`(기본 1)·`OSV_LOCAL_DB_PATH`(기본 `/opt/osv-db`) 주입.
- `Dockerfile.worker`: 빌드타임에 `OSV_SCANNER_LOCAL_DB=/opt/osv-db` 로 오프라인 advisory DB 다운로드(`--offline --download-offline-databases` 또는 구버전 `--experimental-*` 폴백), 런타임 `OSV_OFFLINE=1`. → 런타임 무네트워크([COST-05]).
- 주의: osv-scanner 버전별 offline 플래그 차이 대비 폴백 체인 사용. DB 미탑재 환경은 `OSV_OFFLINE=0` 로 우회 가능(코드가 에러를 삼키지 않으므로 오탐 clean 없음).

### M-2 (medium) — 업로드 토큰 org 스코프
- `internal/queue/queue.go`: `SrcTokenStore` 인터페이스(Put/GetSrcToken, TTL). `internal/queue/redis.go`: Redis 구현(`klaro:scan-src:<token>→org_id`) + `var _ SrcTokenStore`.
- `internal/api/router.go` Deps `SrcTokens` 필드(nil=dev/test 관대). `cmd/api/main.go` 에 `SrcTokens: rd` 배선.
- `internal/api/scans.go`: 업로드 성공 시 `PutSrcToken(token, org, 1h)`; createScan upload 분기에서 `uploadTokenBelongsToOrg`(요청 org≠소유 org → **404 NOT_FOUND**).
- 검증: 통합 `TestUploadTokenOrgScope`(B가 A 토큰 사용→404, A가 자기 토큰→202) **PASS**.

### 재감사 검증 결과 (본 세션 실측)
- `go build ./...` / `go vet ./...` : **그린** (`GOOS=linux go build ./...` 도 그린)
- `go test ./...`(유닛, count=1) : **전 패키지 ok** (신규 `validate_test`·`semgrep_detect_test` 포함)
- 통합(Postgres+Redis, `-tags integration`): `TestEphemeralCleanup`·`TestNoSourceInDB`·`TestRescanTriage`·`TestRLSSessionUnsetReturnsZero`·`TestRLSCrossOrgBlocked`·`TestScanRepoURLInjectionRejected`(C-1/H-1)·`TestUploadTokenOrgScope`(M-2) **전부 PASS**
- 바이너리 의존 `TestSemgrepExecFindsPythonVuln`(D-1)·ZAP e2e 는 스캐너 바이너리/데몬 있는 환경에서 `-tags integration` 실행(호스트 미탑재 시 skip).

### 비차단(L-1~L-3)
- 여력 범위에서 코드 명확성 개선(git 환경변수 격리, stderr 캡처) 반영. 그 외 문서/견고성 항목은 후속.
