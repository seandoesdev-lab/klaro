# klaro Phase 2 — 아키텍처·구현 설계: S2 보안 스캔 실엔진

**작성** architect · **작성일** 2026-07-20 · **상태** 빌더 착수용(확정)
**입력** `_workspace/p2_01_spec-analyst_contract.md`, `_workspace/02_architect_design.md`, `_workspace/03_backend-builder_manifest.md`, `docs/klaro/01~04`, `services/load-test/**`(실측)
**확정 스택(입력)** Go · 공용 DB+RLS(Phase 1 완료) · 큐=Redis · Docker Compose · 스캐너 전부 OSS(Semgrep/osv-scanner/OWASP ZAP)
**대상 모듈** 단일 Go 모듈 `services/load-test`(`github.com/klaro/load-test`)에 `internal/scanner` 패키지를 신설하고 `scanworker`를 개조한다. 별도 서비스 분리 없음.

> backend-builder는 이 문서만으로 착수할 수 있다. 결정 #1~#11 전부 확정, 되물을 항목 없음.
> **Phase 1 패턴 그대로 재사용**: `store.RunInOrg(orgID, fn)` org tx 관통(`store.go:91`), `Querier` 주입(`scans_store.go`의 모든 메서드는 이미 `q Querier` 파라미터화됨), 워커 org tx 관통(`scanworker.go:54-58`). Phase 2는 이 위에 스캐너 실엔진만 얹는다. RLS/tenancy 구조는 손대지 않는다.

---

## 0. 설계 요약 (한 문단)

`stubSAST`(고정 2건 placeholder)와 헤더 전용 `runDAST`를 **실엔진 파이프라인**으로 대체한다. SAST는 소스(공개 repo clone 또는 사용자 아카이브 업로드)를 **잡별 tmpfs 작업 디렉터리**에 확보한 뒤 Semgrep(코드)+osv-scanner(의존성)를 `context.WithTimeout` 하에 실행하고, 종료 시 `defer os.RemoveAll`로 소스를 소멸시킨다([EPHEM-01]). DAST는 기존 순수 헤더 분석(`AnalyzeHeaders`)에 **OWASP ZAP daemon**(별도 Compose 서비스, HTTP API)의 baseline/active 스캔을 통합하며, SC-01 검증 도메인 게이트는 active를 포함해 그대로 유지한다. 결과는 `scan_findings`에 **`evidence jsonb` + `cwe/confidence/package/package_version` 소수 컬럼**을 더한 확장 스키마(마이그레이션 0009)로 정규화 저장하되 org_id+RLS는 유지한다. 스캐너 출력→finding 정규화·severity 매핑·finding_hash 산출은 `internal/scanner`가 담당하고, `scanworker`는 오케스트레이션(상태 전이·소스 라이프사이클·재스캔 triage 승계)만 맡는다. 신규 유료 의존 0([COST-05]).

---

## 1. 결정표 (#1~#11 전부 확정)

| # | 항목 | 결정 | 근거(한 줄) | 기각 대안 |
|---|------|------|-------------|-----------|
| **#1** | SAST 소스 경로 | **(a) 사용자 아카이브(tar/zip) 업로드 + (b) 공개 repo URL clone**. GitHub App PR 체크아웃·PR 코멘트 게시·`POST /webhooks/github`는 **범위 밖**(후속 GitHub 연동 페이즈). SAST 요청은 `repo_url` XOR `upload_token` 필수([SAST-03] 400). | `github_installations`는 Phase 1 제외(nullable). 두 경로면 GitHub 종속 없이 실엔진 검증 가능. | PR 트리거 포함 → GitHub App 인증/웹훅 서명/설치 흐름 전체 선행 필요, Phase 2 폭증. |
| **#2** | Ephemeral tmpfs | worker 컨테이너에 **tmpfs 마운트 `/scan-work`(size 2g)** + Go `os.MkdirTemp("/scan-work","job-<id>-")` 잡별 디렉터리, `defer os.RemoveAll`. 업로드 아카이브는 **api·worker 공유 tmpfs 볼륨 `scan_src`(RAM)** 경유. 소스 본문 컬럼 부재 + 기동 시 tmpfs assert. | 명명 볼륨/이미지 레이어/호스트 바인드에 소스 미기록. 공유 tmpfs 볼륨으로 api→worker 아카이브 전달도 RAM 유지. | Redis에 아카이브 blob → Redis는 기본 디스크 영속(RDB/AOF), 소스 디스크 잔존 위험. dind → 무겁고 Compose 제약. |
| **#3** | 스캐너 실행 | Semgrep(pip)·osv-scanner(정적 바이너리)·git은 **worker 이미지 번들**. ZAP은 **별도 Compose 서비스 `zap`(`zaproxy/zap-stable` headless daemon)**, 워커가 **ZAP REST API(HTTP)** 로 구동. | docker-in-docker 회피. ZAP는 무겁고 상주형이라 서비스 분리가 자연스러움. 전부 OSS·로컬 ₩0([COST-05]). | 워커에 ZAP 번들 → 이미지 비대·상주 데몬 관리 복잡. dind → 권한/보안/무비용 제약. |
| **#4** | 리소스·타임아웃 | 스캐너별 `context.WithTimeout`: **clone 120s / Semgrep 300s / osv 120s / ZAP baseline 600s / ZAP active 900s**. 워커 **세마포어 `SCAN_MAX_CONCURRENCY=2`**, ZAP은 단일 데몬이라 **mutex 직렬화**. compose `deploy.resources.limits`(worker cpus 2·mem 4g, zap mem 2g), tmpfs size 2g. | 무한 점유·플랫폼 폭주 방지([SC-07]). ZAP 세션 충돌 방지. | 무제한 → 워커 점유·OOM. 전역 동시성 1 → 처리량 저하. |
| **#5** | 증거 스키마 | `scan_findings`에 **`evidence jsonb`(스캐너 원시 근거)** + 공통 소수 컬럼 **`cwe text`,`confidence text`,`package text`,`package_version text`**. 신규 마이그레이션 **0009**. org_id+RLS 유지(D-9). | jsonb는 스캐너별 이질 증거를 유연 수용, 공통 조회·필터 필드만 컬럼화. `ALTER ADD COLUMN nullable`로 하위호환. | 전부 개별 컬럼 → 스캐너마다 스키마 변경 폭증. 전부 jsonb → 인덱싱·조회 불편. |
| **#6** | severity 매핑 | 매핑 테이블 고정(§6.3). **ComputeScore 개편**: open finding만 집계 + **severity 버킷별 상한**(critical 40/high 30/medium 20/low 8/info 2, 합계 상한 100). | 실엔진 finding 급증 시 단순 가산은 하한 0 고착 → 버킷 상한으로 gradation 확보(계약 모호#score 완화). ignored/fixed 제외로 triage 반영. | 현행 무제한 가산 유지 → 0 고착. 로그감쇠 등 → 과설계. |
| **#7** | finding_hash | **SAST(Semgrep)** = `sha256(rule_id \| repo상대 file_path \| 정규화 스니펫)` — 라인번호 배제로 라인 시프트에 견고. **osv** = `sha256(rule_id \| package \| version)`. **DAST(ZAP)** = `sha256(rule_id \| url \| param)`. 헤더분석 = 현행 `FindingHash(rule_id, location)` 유지. | 스니펫 내용 해시가 라인 이동에 안정. osv/DAST는 자연 키. | 라인번호 포함 → 무해한 편집에도 triage 유실. |
| **#8** | 재스캔 fixed 판정 | **워커가 스캔 완료 시** 이전 최신 completed 스캔(같은 project+type)의 finding을 조회, (a) 이번에도 있는 hash 중 이전 `ignored`는 상태·사유 승계, (b) 이전엔 있었으나 이번엔 없는 hash는 **이번 스캔에 `fixed` 행으로 삽입**. 판정 위치: `scanner` 집계 후 `scanworker` 내 org tx. | 조회 시 계산은 매 조회 diff 비용·불변 결과 저장 부재. 워커 1회 판정이 감사·표시에 명확. | 조회 시 계산 → 표시 일관성·성능 저하. |
| **#9** | DAST active | 요청 `{type:"dast", target_url, mode?:"baseline"\|"active"}`, **기본 baseline**. active는 **SC-01 검증 도메인 게이트 하에서만**(기존 게이트가 모든 DAST에 적용되므로 자동 충족) + 타임아웃 900s. | baseline=수동/안전 기본, active=명시 옵트인. 게이트 약화 없음. | active 기본 → 미검증/과부하 위험. active 무노출 → 요구([DAST-01]) 미충족. |
| **#10** | 중복 정책 | rule_id 네임스페이스 **`dast.header.*`(헤더분석) vs `zap.*`(ZAP)**. 동일 이슈(주로 보안 헤더 누락) 중복 시 **ZAP 우선** — ZAP가 해당 헤더 토픽을 보고하면 대응 `dast.header.*` finding 억제(§6.5 overlap 표). | 헤더 분석은 오프라인·결정적 baseline, ZAP는 근거(요청/응답) 풍부 → ZAP 우선. | 무중복 정책 → 이중 보고로 score 왜곡·UI 혼란. |
| **#11** | 상태머신 | **축약 유지**: `pending→running→completed/failed`(`scan.go:36-44` 불변). 부하 잡 확장머신 미도입. org tx 관통·불법 전이 거부(`ErrIllegalTransition`) 유지. | 스캔은 단계 노출 요구가 약하고 현행 enum·DDL·UI가 축약 기준. 확장은 마이그레이션·UI 파급. | 확장머신 도입 → enum/DDL/프런트 파급, 편익 낮음. |

---

## 2. Ephemeral 소스 파이프라인 ([EPHEM-01], 가장 중요)

### 2.1 시퀀스 (성공 경로)

```
[SAST]
1. (업로드 경로) 클라이언트 → POST /projects/:id/scans/source (multipart tar/zip)
     API: 크기·MIME 검증 → scan_src(공유 tmpfs)에 /scan-src/<token>.tar 기록 (RAM)
        → {upload_token, expires_in} 반환. 아카이브 바이트는 DB/디스크 미기록.
2. 클라이언트 → POST /projects/:id/scans {type:"sast", upload_token|repo_url, ref?}
     API: 소스 입력 검증(XOR) → scans INSERT(source_type, source_ref 메타데이터만)
        → ScanJob{...Source} enqueue
3. worker.processScan:
   a. setStatus(running)  [RunInOrg org tx]
   b. dir, cleanup := scanner.Acquire(ctx, "/scan-work", src)   // MkdirTemp + clone/extract
      defer cleanup()                                            // = os.RemoveAll(dir) (+ 업로드 staging 삭제)
   c. semgrep.Scan(ctx300s, dir)  ∥  osv.Scan(ctx120s, dir)      // 병렬, 각자 타임아웃
   d. findings = merge(semgrep, osv)  → 정규화·hash·severity 매핑
   e. triage 승계/fixed 판정(#8)  [이전 스캔 조회, org tx]
   f. SaveFindings + setStatus(completed, score)  [org tx]
   g. (defer) cleanup() 실행 → 작업 디렉터리·staging 소멸

[DAST]
   b'. header = AnalyzeHeaders(resp.Header, url)      // 순수, 네트워크 1회
   c'. zap.Scan(ctxMode, url, mode)                    // ZAP REST: spider→(passive|active)→alerts
   d'. findings = dedup(header, zap)  (§6.5, ZAP 우선)
   (소스 없음 → tmpfs 미사용)
```

### 2.2 실패/타임아웃/중단 각 경로의 소멸 보장

`Acquire`가 성공적으로 dir을 만든 **직후** `defer cleanup()`를 건다. 이후 어떤 경로로 함수가 종료되어도(정상 return / 스캐너 에러 / `context` 타임아웃 / panic recover / worker ctx 취소) `os.RemoveAll(dir)`가 실행된다.

| 경로 | 소멸 보장 지점 |
|------|----------------|
| 정상 완료 | 함수 return 직전 `defer cleanup()` |
| clone/extract 실패 | `Acquire` 내부에서 부분 생성분 즉시 `RemoveAll` 후 err 반환(dir 미반환 시 자기정리) |
| Semgrep/osv 실패·타임아웃 | `defer cleanup()` + `setStatus(failed)` |
| ZAP 실패·타임아웃 | (소스 없음) — 해당 없음, DAST는 tmpfs 미사용 |
| worker ctx 취소(SIGTERM) | processScan의 `ctx` 파생 취소 → 스캐너 종료 → `defer cleanup()` 도달 |
| panic | processScan 최상단 `defer func(){ recover(); cleanup은 이미 등록 }` — cleanup은 panic 시에도 실행 |

> 업로드 staging(`/scan-src/<token>`)은 worker가 extract 성공 후 즉시 삭제하고, cleanup에서도 재삭제(idempotent). 미소비 staging은 api 측 TTL 스위퍼(파일 mtime > expires 삭제)로 회수.

### 2.3 코드·스키마 레벨 강제·검증

1. **스키마 레벨**: `scans`/`scan_findings` 어디에도 소스 본문 컬럼이 없다. Phase 2가 추가하는 컬럼은 메타데이터(`source_type`,`source_ref`=repo URL 또는 업로드 파일명)와 finding 증거(경로·스니펫 조각)뿐. `evidence jsonb`에는 매칭 라인 스니펫만 저장하며 전체 파일 사본 금지(정규화 단계에서 스니펫 라인 수 상한 `SNIPPET_MAX_LINES=10`).
2. **tmpfs assert(기동 시)**: worker `main`에서 `scanner.AssertTmpfs(SCAN_WORK_DIR)` 호출 — `unix.Statfs`로 `f_type == TMPFS_MAGIC(0x01021994)` 확인, 아니면 `log.Fatal`. `SCAN_WORK_DIR`이 tmpfs가 아닌 환경(오구성/윈도우 로컬 `go test`)은 `SCAN_WORK_TMPFS_ASSERT=0`로 우회(테스트 전용).
3. **잡별 격리**: `os.MkdirTemp`로 잡마다 고유 디렉터리 → 잡 간 소스 혼입 불가.
4. **검증 테스트(integration)**: `TestEphemeralCleanup` — 스캔(성공/강제실패) 후 `/scan-work` 하위 잡 디렉터리 0개 확인. `TestNoSourceInDB` — 스캔 후 `scans`/`scan_findings` 어떤 text 컬럼에도 소스 원문 토큰(주입한 canary 문자열)이 없음 확인.

---

## 3. 데이터 모델·마이그레이션 (0009)

### 3.1 `migrations/0009_scan_engine.sql`

기존 러너는 `migrations/*.sql` 사전순 적용(0001…0009). 슈퍼유저(`klaro`) 실행이라 RLS 우회. 전부 `ADD COLUMN IF NOT EXISTS`(nullable)로 재실행·하위호환 안전.

```sql
-- scans: 소스 참조(메타데이터만) + DAST 모드
ALTER TABLE scans ADD COLUMN IF NOT EXISTS source_type text;   -- 'repo' | 'upload' (sast 전용)
ALTER TABLE scans ADD COLUMN IF NOT EXISTS source_ref  text;   -- repo URL 또는 업로드 파일명(원문 아님)
ALTER TABLE scans ADD COLUMN IF NOT EXISTS mode        text;   -- dast: 'baseline' | 'active'
-- 방어적 CHECK(느슨): 값이 있으면 허용 집합
ALTER TABLE scans ADD CONSTRAINT chk_scans_source_type
  CHECK (source_type IS NULL OR source_type IN ('repo','upload'));
ALTER TABLE scans ADD CONSTRAINT chk_scans_mode
  CHECK (mode IS NULL OR mode IN ('baseline','active'));

-- scan_findings: 증거 확장 (jsonb + 공통 소수 컬럼)
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS evidence        jsonb;
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS cwe             text;
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS confidence      text;   -- high|medium|low
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS package         text;   -- osv
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS package_version text;   -- osv
```

- **org_id/RLS**: `scan_findings`·`scans`는 Phase 1(0005/0006)에서 이미 `org_id` + `org_isolation` FORCE RLS 보유. `ALTER TABLE ... ADD COLUMN`은 정책에 영향 없음 → 확장 컬럼도 자동으로 org 스코프 보호.
- **하위호환**: 신규 컬럼 전부 nullable → 기존 finding JSON 계약(`model/scan.go:82-94`)과 호환. 모델 필드는 `omitempty`.
- **소멸 원칙**: `source_ref`는 repo URL/파일명 같은 포인터·라벨만. 소스 본문·아카이브 바이트 저장 컬럼은 신설하지 않는다([EPHEM-01]).

### 3.2 문서 갱신 대상 (링크 그래프 정합)

- `docs/klaro/02-data-model.md §2.4`: `scans`에 `source_type`/`source_ref`/`mode` 행, `scan_findings`에 `evidence`/`cwe`/`confidence`/`package`/`package_version` 행 추가.
- `docs/klaro/03-api-spec.md §3.3`: SAST 요청을 `pr_number` 예시에서 `repo_url`/`upload_token` 필수로 갱신, DAST `mode` 추가, `POST /projects/:id/scans/source` 신설, findings 응답 증거 필드. `§4 웹훅`의 `POST /webhooks/github`는 "후속 GitHub 연동 페이즈" 주석 표기(Phase 2 미구현).

---

## 4. Go 패키지·모듈 설계

### 4.1 신규 패키지 `internal/scanner`

```
internal/scanner/
  scanner.go     # 인터페이스, Options(타임아웃), 공통 타입
  source.go      # Ephemeral 소스 확보: Acquire(clone/extract) + AssertTmpfs + cleanup
  semgrep.go     # Semgrep 실행(exec) + JSON 파싱 → []model.ScanFinding
  osv.go         # osv-scanner 실행 + JSON 파싱 → findings
  zap.go         # ZAP REST 클라이언트(spider→scan→alerts) → findings
  headers.go     # AnalyzeHeaders 이관(순수 함수) + rule_id 네임스페이스 dast.header.*
  mapping.go     # severity 매핑 테이블(#6) + ComputeScore(개편)
  normalize.go   # 스니펫 정규화, FindingHashParts, repo상대경로화
  *_test.go      # 각 스캐너 JSON 픽스처 파싱 유닛테스트(네트워크·바이너리 불필요)
```

**인터페이스**(작은 경계):

```go
type Mode string // "baseline" | "active"

type SASTScanner interface {
    Name() string
    Scan(ctx context.Context, srcDir string) ([]model.ScanFinding, error)
}
type DASTScanner interface {
    Name() string
    Scan(ctx context.Context, targetURL string, mode Mode) ([]model.ScanFinding, error)
}

type Source struct {
    Type    string // "repo" | "upload"
    RepoURL string
    Ref     string // 선택 branch/commit, 기본 default HEAD
    Token   string // upload staging 토큰
}

// Acquire: /scan-work 하위 잡별 tmpfs 디렉터리에 소스 확보. cleanup은 항상 호출자 defer.
func Acquire(ctx context.Context, workRoot, srcStagingRoot string, s Source) (dir string, cleanup func(), err error)
func AssertTmpfs(path string) error
```

- Semgrep·osv는 `SASTScanner`, ZAP은 `DASTScanner` 구현. 헤더 분석은 `headers.go`의 순수 함수로 DAST 집계에 항상 포함.
- 스캐너는 **DB 무지**(정규화된 `[]model.ScanFinding`만 반환). store/tenancy와 분리 → 순환 의존 없음(`scanworker → scanner`, `scanner → model`만).

### 4.2 스캐너 실행 상세

| 스캐너 | 실행 커맨드(요지) | 파싱 |
|--------|-------------------|------|
| Semgrep | `semgrep --config $SEMGREP_RULES_DIR --json --metrics=off --timeout <s> <dir>` (규칙셋은 이미지 번들, 런타임 무네트워크·무비용) | `results[].{check_id,path,start.line,extra.{message,severity,lines,metadata.cwe}}` |
| osv-scanner | `osv-scanner --format json --recursive <dir>` | `results[].packages[].{package.{name,version}, vulnerabilities[].{id,summary,severity[].score(CVSS)}}` |
| ZAP | REST: `spider/action/scan` → poll `spider/view/status` → (active면 `ascan/action/scan` poll) → `core/view/alerts?baseurl=` | alerts[]: `{alertRef/pluginId,name,risk,confidence,url,param,evidence,cweid,solution,reference}` |

- **Semgrep 규칙 오프라인**: 이미지 빌드 시 `returntocorp/semgrep-rules`(OSS)를 `/opt/semgrep-rules`에 clone → `SEMGREP_RULES_DIR=/opt/semgrep-rules`. `--config auto`(레지스트리/계정 필요) 금지([COST-05] 오프라인).
- **락파일 없음**: osv-scanner가 지원 매니페스트 미발견 → exit 0/128 처리, 0건으로 취급(스캔 실패 아님, [SAST-02] 수용 기준).
- **finding 0건**: SAST 0건 → `completed`, score=100([SAST-01]).

### 4.3 결과 정규화 매핑 (스캐너 출력 → scan_findings)

| 필드 | Semgrep | osv | ZAP | 헤더분석 |
|------|---------|-----|-----|----------|
| `rule_id` | `sast.semgrep.<check_id>` | `osv.<advisory-id>` | `zap.<pluginId>` | `dast.header.<topic>` |
| `severity` | ERROR→high, WARNING→medium, INFO→low (metadata.security-severity 있으면 우선) | CVSS→§6.3 | High→high…Info→info | 현행 유지 |
| `title` | `extra.message` 요약 | `<pkg> <ver>: <summary>` | alert name | 현행 문구 |
| `file_path` | repo 상대경로(work dir prefix 제거) | 매니페스트 상대경로 | (nil) | (nil) |
| `line` | `start.line`(표시용, hash 제외) | (nil) | (nil) | (nil) |
| `cwe` | `metadata.cwe` | (nil) | `cweid` | (nil) |
| `confidence` | metadata.confidence | (nil) | ZAP confidence | (nil) |
| `package`/`package_version` | (nil) | pkg name/version | (nil) | (nil) |
| `evidence`(jsonb) | `{lines(≤10), message, references}` | `{advisory, cvss, summary}` | `{url, param, evidence, solution, reference}` | `{location}` |
| `finding_hash` | §6.4 | §6.4 | §6.4 | 현행 |

### 4.4 model 변경 (`internal/model/scan.go`)

```go
// ScanFinding 확장 (전부 omitempty → 하위호환)
type ScanFinding struct {
    ... 기존 필드 ...
    CWE            *string          `json:"cwe,omitempty"`
    Confidence     *string          `json:"confidence,omitempty"`
    Package        *string          `json:"package,omitempty"`
    PackageVersion *string          `json:"package_version,omitempty"`
    Evidence       json.RawMessage  `json:"evidence,omitempty"` // jsonb
}

// ScanJob 확장: 소스 참조 + DAST 모드
type ScanJob struct {
    ScanID    string
    OrgID     string
    ProjectID string
    Type      ScanType
    TargetURL string
    Mode      string     `json:"mode,omitempty"`   // dast
    Source    ScanSource `json:"source,omitempty"` // sast
}
type ScanSource struct {
    Type    string `json:"type,omitempty"` // repo|upload
    RepoURL string `json:"repo_url,omitempty"`
    Ref     string `json:"ref,omitempty"`
    Token   string `json:"token,omitempty"`
}

// finding_hash 유연화: 기존 FindingHash(2인자) 유지 + 가변 헬퍼 추가
func FindingHashParts(parts ...string) string { /* sha256(strings.Join(parts,"|")) */ }
```

`Scan` 구조체에 `SourceType/SourceRef/Mode *string` 추가(응답·조회용, `omitempty`).

### 4.5 store 변경 (`internal/store/scans_store.go`)

- `CreateScan`: INSERT 목록에 `source_type, source_ref, mode` 추가(Phase 1 org_id 세션 주입 패턴 유지).
- `SaveFindings`: INSERT 목록에 `evidence, cwe, confidence, package, package_version` 추가.
- `ListFindings`/`GetScan`: SELECT에 신규 컬럼 추가.
- **신규** `GetLatestCompletedScanFindings(ctx, q, projectID string, typ model.ScanType, beforeScanID string) ([]model.ScanFinding, error)` — #8 재스캔 diff용. 같은 project+type의 직전 `completed` 스캔 findings 반환(RLS org tx 내 실행).

전부 기존처럼 `q Querier` 파라미터 유지 → RLS 관통 불변.

### 4.6 scanworker 개조 (`internal/worker/scanworker.go`)

```go
func RunScan(ctx, d ScanDeps) {
    sem := make(chan struct{}, d.MaxConcurrency) // #4
    for {
        job := d.Queue.DequeueScan(ctx) ...
        sem <- struct{}{}
        go func(job){ defer func(){ <-sem }(); d.processScan(ctx, job) }(job)
    }
}

func (d ScanDeps) processScan(ctx, job) error {
    setStatus(running)                                  // RunInOrg (기존 패턴)
    defer recover→ setStatus(failed) 보강
    switch job.Type {
    case SAST:
        dir, cleanup, err := scanner.Acquire(ctx, d.WorkDir, d.SrcDir, job.Source)
        if err != nil { setStatus(failed); return err }
        defer cleanup()                                 // ★ EPHEM 소멸 보장
        sg, _ := d.Semgrep.Scan(ctx300, dir)
        ov, _ := d.OSV.Scan(ctx120, dir)
        findings = merge(sg, ov)
    case DAST:
        header := scanner.AnalyzeHeaders(...)
        d.zapMu.Lock(); zap, err := d.ZAP.Scan(ctxMode, job.TargetURL, mode); d.zapMu.Unlock()
        if err != nil { setStatus(failed); return err }
        findings = scanner.DedupDAST(header, zap)       // #10
    }
    findings = d.applyTriage(ctx, job, findings)        // #7/#8 승계·fixed
    SaveFindings(findings); setStatus(completed, ComputeScore(findings))
}
```

- `ScanDeps` 확장: `Semgrep SASTScanner`, `OSV SASTScanner`, `ZAP DASTScanner`, `WorkDir`, `SrcDir`, `MaxConcurrency`, `zapMu sync.Mutex`.
- `applyTriage`: `GetLatestCompletedScanFindings` 조회(org tx) → 이번 findings의 이전 `ignored` hash 승계, 이전에만 있던 hash를 `fixed` finding으로 추가.
- `stubSAST` 삭제. `AnalyzeHeaders`는 `scanner` 패키지로 이관(worker에서 재노출 또는 호출).

### 4.7 API 변경 (`internal/api/scans.go` + `router.go`)

**`createScan` 검증 확장**:
```go
type req struct {
    Type      string  `json:"type"`
    TargetURL string  `json:"target_url"`
    Mode      string  `json:"mode"`        // dast: baseline(기본)|active
    RepoURL   string  `json:"repo_url"`    // sast
    Ref       string  `json:"ref"`         // sast(선택)
    UploadToken string `json:"upload_token"` // sast
    PRNumber  *int    `json:"pr_number"`   // 유지(trigger 라벨만, 소스 아님)
}
```
- SAST: `repo_url` XOR `upload_token` 필수, 둘 다/둘 다없음 → `400 VALIDATION_ERROR`([SAST-03]). `upload_token`은 staging 파일 존재 검증. `source_type/source_ref` scan에 기록. Job.Source 채움.
- DAST: 기존 `target_url` 필수 + `IsDomainVerified` 게이트 유지([SC-01]). `mode` 미지정→baseline, `active`면 그대로(게이트 이미 통과). scan.mode 기록.

**신규 라우트** (`router.go`, min=member):
```
POST /projects/:id/scans/source   → d.uploadScanSource  // multipart(tar/zip), 반환 {upload_token, expires_in}
```
`uploadScanSource`: `MaxBytesReader`(예 200MB)로 크기 상한, MIME/확장자 검증(tar/zip만), 랜덤 토큰 생성 → `$SCAN_SRC_DIR/<token>.<ext>`(공유 tmpfs)에 스트림 기록. org 스코프는 토큰 소유 검증용으로 Redis에 `scan-src:<token>→org_id`(TTL) 기록(선택). DB 미기록.

**findings 응답**: `listFindings`는 확장 필드 자동 포함(모델 omitempty). `PATCH /scans/:id/findings/:findingId` 현행 유지([SC-04]).

기존 라우트(`POST/GET /projects/:id/scans`, `GET /scans/:id`, `/findings`)는 경로·권한(GET=viewer, 변경=member) 유지.

---

## 5. Docker / 인프라 변경

### 5.1 `Dockerfile.worker` (스캐너 번들)

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/worker ./cmd/worker

FROM grafana/k6:latest AS k6   # k6 바이너리 추출용

FROM python:3.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates curl \
 && pip install --no-cache-dir semgrep==1.* \
 && curl -sSL -o /usr/local/bin/osv-scanner \
      https://github.com/google/osv-scanner/releases/latest/download/osv-scanner_linux_amd64 \
 && chmod +x /usr/local/bin/osv-scanner \
 && git clone --depth 1 https://github.com/returntocorp/semgrep-rules /opt/semgrep-rules \
 && apt-get purge -y --auto-remove \
 && rm -rf /var/lib/apt/lists/*
COPY --from=k6    /usr/bin/k6           /usr/local/bin/k6
COPY --from=build /out/worker           /usr/local/bin/worker
ENV K6_PATH=k6 SEMGREP_RULES_DIR=/opt/semgrep-rules
ENTRYPOINT ["worker"]
```
> 전부 OSS·빌드타임 취득 → 런타임 무네트워크·₩0([COST-05]). 베이스가 `grafana/k6`→`python:3.12-slim`으로 바뀌므로 k6 바이너리는 멀티스테이지 COPY로 보존(S1 부하 워커 회귀 방지).

### 5.2 `docker-compose.yml`

```yaml
  zap:
    image: zaproxy/zap-stable:latest
    command: >
      zap.sh -daemon -host 0.0.0.0 -port 8090
      -config api.disablekey=true
      -config api.addrs.addr.name=.* -config api.addrs.addr.regex=true
    deploy: { resources: { limits: { memory: 2g } } }

  api:
    # ...(기존)...
    volumes:
      - scan_src:/scan-src          # 업로드 staging(공유 tmpfs)
    environment:
      SCAN_SRC_DIR: /scan-src

  worker:
    # ...(기존)...
    tmpfs:
      - /scan-work:size=2g,mode=1777   # ★ 잡별 소스 작업공간(RAM 전용)
    volumes:
      - scan_src:/scan-src
    environment:
      SCAN_WORK_DIR: /scan-work
      SCAN_SRC_DIR: /scan-src
      ZAP_ADDR: http://zap:8090
      SCAN_MAX_CONCURRENCY: "2"
    deploy: { resources: { limits: { cpus: "2.0", memory: 4g } } }
    depends_on:
      postgres: { condition: service_healthy }
      redis: { condition: service_started }
      zap: { condition: service_started }

volumes:
  klaro_pgdata:
  scan_src:                         # RAM 백엔드 공유 볼륨(api↔worker)
    driver: local
    driver_opts: { type: tmpfs, device: tmpfs, o: "size=1g" }
```

- `worker`의 `/scan-work`는 컨테이너 전용 tmpfs, `scan_src`는 api·worker 공유 tmpfs 볼륨 → 소스는 어느 경로에서도 RAM에만 존재([EPHEM-01] #2).
- `cmd/worker/main.go`: `AssertTmpfs(SCAN_WORK_DIR)` 기동 검증 + `ScanDeps`에 스캐너/디렉터리/동시성 주입.

---

## 6. 세부 규칙표

### 6.3 severity 매핑 (#6)

| 스캐너 | 원 값 → klaro |
|--------|----------------|
| Semgrep | ERROR→high, WARNING→medium, INFO→low. metadata `security-severity`(CVSS) 있으면 CVSS 규칙 우선 |
| osv (CVSS) | ≥9.0 critical / 7.0–8.9 high / 4.0–6.9 medium / 0.1–3.9 low / 미상 → medium |
| ZAP | High→high, Medium→medium, Low→low, Informational→info |
| 헤더분석 | 현행 유지(HSTS high, CSP medium, X-Frame low, …) |

**ComputeScore 개편**: open finding만 집계, severity 버킷별 상한.
```
penalty = Σ_bucket min(cap_bucket, weight_bucket × open_count_bucket)   // cap 없이 무제한 가산 제거
score   = max(0, 100 - min(100, penalty))
cap:  critical 40, high 30, medium 20, low 8, info 2
weight(개당): critical 25, high 15, medium 8, low 3, info 1   // 기존 값 유지
```
→ 단일 critical=25 감점, critical 다수라도 버킷 상한 40 → 다른 severity와 합산해야 0 도달. finding 급증 시 하한 0 즉시 고착 완화.

### 6.4 finding_hash (#7)

| 종류 | 해시 입력 |
|------|-----------|
| Semgrep | `FindingHashParts(rule_id, repo상대 file_path, 정규화 스니펫)` |
| osv | `FindingHashParts(rule_id, package, package_version)` |
| ZAP | `FindingHashParts(rule_id, url, param)` |
| 헤더분석 | `FindingHash(rule_id, location)` (현행, 회귀 없음) |

정규화 스니펫 = `extra.lines`의 각 줄 `TrimSpace` + 내부 공백 런 단일 스페이스 치환 후 join. 라인번호 미포함 → 파일 내 코드 이동에 안정.

### 6.5 DAST 중복 정책 (#10)

ZAP alert의 header-계열 pluginId → 대응 `dast.header.*` 억제(ZAP 우선).

| ZAP pluginId | 토픽 | 억제되는 헤더분석 rule_id |
|--------------|------|---------------------------|
| 10035 | HSTS | `dast.header.missing-hsts`, `dast.header.weak-hsts` |
| 10038 | CSP | `dast.header.missing-csp` |
| 10020 | X-Frame-Options | `dast.header.missing-x-frame-options` |
| 10021 | X-Content-Type-Options | `dast.header.missing-x-content-type-options` |
| 10036 | Server 버전 노출 | `dast.header.server-version-disclosure` 등 |

`DedupDAST(header, zap)`: ZAP가 보고한 토픽 집합을 만들고, 해당 토픽의 헤더 finding을 결과에서 제외 후 병합.
> **주의(회귀)**: 헤더 rule_id를 `dast.missing-*`→`dast.header.*`로 개명하므로 finding_hash가 바뀐다. 기존 DAST 스캔 이력과 1회 재베이스라인 발생(이전 헤더 findings는 다음 재스캔에서 fixed로 판정 후 새 hash로 재생성). `worker/scan_analysis_test.go`의 rule_id 기대값도 갱신 대상.

---

## 7. 빌드 순서 (backend-builder용, 의존순)

각 단계 종료 시 `go build ./... && go vet ./...` + 관련 테스트 그린을 게이트로 삼는다.

1. **마이그레이션 `0009_scan_engine.sql`** — 컬럼 확장. `docker compose down -v && up`으로 initdb 재적용, psql로 컬럼·CHECK 확인. (org_id/RLS 불변)
2. **model 확장** — `ScanFinding` 증거 필드, `ScanJob.Source/Mode`, `ScanSource`, `FindingHashParts`, `ComputeScore` 개편, severity 매핑 상수. 유닛테스트(ComputeScore 버킷상한, hash 안정성).
3. **`internal/scanner` 패키지** — `scanner.go`/`source.go`(Acquire+AssertTmpfs)/`normalize.go`/`mapping.go`/`headers.go`(이관)/`semgrep.go`/`osv.go`/`zap.go`. 스캐너별 JSON 픽스처 파싱 유닛테스트(바이너리·네트워크 불필요).
4. **store 확장** — `CreateScan`/`SaveFindings`/`ListFindings`/`GetScan` 컬럼 추가, `GetLatestCompletedScanFindings` 신설(전부 `q Querier` 유지).
5. **scanworker 개조** — `stubSAST` 제거, 소스 파이프라인·세마포어·ZAP mutex·`applyTriage`(승계/fixed). `cmd/worker/main.go` 스캐너·디렉터리·동시성 주입 + `AssertTmpfs`.
6. **API 확장** — `createScan` SAST 소스 XOR 검증·DAST mode, `uploadScanSource` 핸들러+라우트, findings 응답 확장.
7. **Docker/compose** — `Dockerfile.worker`(Semgrep/osv/git/semgrep-rules 번들·k6 보존), `docker-compose.yml`(zap 서비스·tmpfs·공유 볼륨·리소스). `docker compose up --build`로 전체 기동.
8. **검증·문서** — integration: `TestEphemeralCleanup`(잡 디렉터리 0), `TestNoSourceInDB`(canary 부재), SAST(repo/upload)·DAST(baseline/active) e2e, 재스캔 triage 승계·fixed. `docs/klaro/02·03` 갱신(§3.2). 불변식 체크리스트 통과.

---

## 8. 불변식 준수 매핑

| 불변식 | 설계상 보장 지점 |
|--------|------------------|
| **[EPHEM-01]** Ephemeral 소스 | §2: 잡별 tmpfs `/scan-work`(#2) + 공유 tmpfs `scan_src`, `defer cleanup()`(성공/실패/타임아웃/취소/panic), 기동 `AssertTmpfs`, 소스 본문 컬럼 부재(§3.1), 검증 테스트 `TestEphemeralCleanup`/`TestNoSourceInDB` |
| **[SC-01]** 도메인 검증 게이트 | §4.7: 모든 DAST(baseline·active)가 기존 `IsDomainVerified` 게이트 통과 후에만 생성(현행 `scans.go:46-54` 유지·미약화). active도 동일 게이트(#9) |
| **RLS 멀티테넌시** | 신규/개조 store 전부 `q Querier`, 워커 `RunInOrg(job.OrgID, …)`(현행 `scanworker.go:54-58` 패턴 유지), 확장 컬럼은 `scan_findings`/`scans`의 기존 `org_isolation` FORCE RLS로 자동 보호(§3.1). staging/작업디렉터리는 잡별 격리 |
| **잡 상태머신** | `pending→running→completed/failed` 축약 유지(#11), `CanScanTransition`·`ErrIllegalTransition` 불변(`scans_store.go:69`) |
| **[COST-05]** 비용 게이트 | Semgrep/osv-scanner/ZAP/git 전부 OSS·로컬 ₩0, semgrep 규칙 빌드타임 번들(런타임 무네트워크·계정 불요), Bedrock 외 신규 유료 외부 호출 0 |
| **시계열 RDB 분리** | 스캔 결과는 정형 finding(RDB 정상). 소스 본문·대용량 원시 로그 RDB 미저장(`evidence`는 스니펫 조각 ≤10줄로 상한) |
