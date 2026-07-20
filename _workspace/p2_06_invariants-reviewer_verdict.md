# P2-06 불변식·비용 리뷰 판정 — S2 보안 스캔 실엔진 (Phase 2)

## 재감사(2차) 판정 — 2026-07-20 · 종합: 배포 가능 (DEPLOY)

이전 차단 사유 C-1(critical)·H-1(high) **전부 해소**. M-1·M-2·D-1도 해소(프로덕션 배선 확인, 테스트 전용 no-op 아님). 신규 critical/high 없음. 잔여 low 1건(R-1)은 비차단.

| ID | 이전 sev | 상태 | 근거(file:line) |
|----|----------|------|------------------|
| C-1 | critical | **해소** | `scanner/validate.go:22-45` ValidateRepoURL(https-only+host필수+선행`-`거부+SSRF게이트), `source.go:67-98` gitClone `-c protocol.ext/file/ftp/ftps.allow=never`+`--` 구분자+`GIT_TERMINAL_PROMPT=0`/`GIT_CONFIG_NOSYSTEM=1`/`GIT_CONFIG_GLOBAL=/dev/null`+클론 전 재검증, `api/scans.go:100-110` 큐 적재 전 검증(400) |
| H-1 | high | **해소** | `validate.go:73-99` assertHostNotInternal+isBlockedIP: IP리터럴+DNS 해석 양쪽, loopback/RFC1918+ULA/link-local(169.254)/unspecified 차단, 다중 A레코드 전부 검사 |
| D-1 | high(QA) | **해소** | `semgrep.go:80-115` detectConfigs 언어 감지→해당 언어 룰만 `--config`, `:184-191` normalizeCheckID, `scanner.go:63` timeout 900s, 언어 미감지→0건 |
| M-1 | medium | **해소** | `osv.go:66-88` exit코드 구분(0/1 파싱, 128→0건, 그외 non-zero/timeout/미기동→에러 표면화), Offline+LocalDBPath, Dockerfile 오프라인 DB |
| M-2 | medium | **해소** | `queue.SrcTokenStore`+redis 구현, `cmd/api/main.go:57` 배선, `scans.go:91-95` 업로드 토큰 org 불일치→404, 업로드 시 PutSrcToken(org,1h) |

**C-1 우회 잔여 집중 검증(전부 차단 확인)**: `--upload-pack`류 인자주입(선행`-`거부+`--` 구분자), ValidateRef 재주입(선행`-`·제어문자·특수문자 거부), 위험 스킴(ext/file/git/ssh/ftp), IPv4-mapped IPv6, 8·16·10진 IP 표기, 멀티 A레코드 스머글링.

**R-1 (low, 비차단)**: DNS 리바인딩 TOCTOU — 검증 시점과 clone 시점의 DNS 해석 차이. 그러나 transport 비활성(ext/file/ftp) + `--` 가드 + 이중 검증으로 성공해도 "블라인드 https-only GET"에 그쳐 실질 완화. 권고: 스캔 워커에 egress 제한 네트워크. (정보성 2건: CGNAT 100.64/10 범위, 핸들러 동기 DNS.)

---

<!-- 이하 1차(차단) 판정 원문 보존 -->

## 1차 판정 — 종합: 차단 (BLOCK)

critical 1건(SAST `repo_url` git 인자·전송 주입 → RCE/SSRF)이 존재해 병합 불가.

| severity | 건수 |
|----------|------|
| critical | 1 (C-1) |
| high | 1 (H-1) |
| medium | 2 (M-1, M-2) |
| low | 3 (L-1, L-2, L-3) |

---

## CRITICAL

### C-1. SAST `repo_url` → `git clone` 인자·전송(transport) 주입 (RCE / SSRF / 로컬파일 노출)
- 근거: `internal/api/scans.go:79-98` — SAST 분기가 `repo_url`을 **scheme/host 검증 없이** 수용(DAST의 `target_url`은 `store.HostFromURL`를 거치는 것과 대조). `internal/scanner/source.go:69-73` — 이 값을 `git clone`에 **위치 인자로 전달, `--` 가드 없음, transport 제한 없음**.
- 위험: git `ext::sh -c ...` 원격코드실행(기본 `protocol.ext.allow=user`가 직접 clone에서 허용), `--upload-pack`류 인자 주입, `file://`/클라우드 메타데이터 SSRF. 엔드포인트는 `min=member`만 요구 → **저권한 테넌트 멤버가 공유 워커에서 RCE** → 진행 중인 타 테넌트 tmpfs 소스 열람 또는 DB 자격증명 탈취로 RLS 우회 → **[EPHEM-01]·RLS 동시 붕괴**.
- 수정 방향(차단 해제 필수):
  1. `repo_url`을 `url.Parse`로 검증, **scheme 화이트리스트 `https`만**(http/git/ssh/ext/file 거부), host 필수, 정규화.
  2. `git clone` 호출에 `--` 구분자 + `-c protocol.ext.allow=never -c protocol.file.allow=never` 강제, `GIT_TERMINAL_PROMPT=0`, `--depth 1 --single-branch`. `ref`도 인자 주입 방지(`--` 뒤 배치·검증).
  3. (심층방어) 사설/링크로컬/메타데이터 IP(RFC1918, 169.254.0.0/16, ::1 등) 대상 clone 차단(SSRF 게이트).

---

## HIGH

### H-1. 검증되지 않은 아웃바운드 clone (SSRF, C-1과 동근)
- `repo_url` 호스트 검증 부재로 내부 서비스/메타데이터 엔드포인트로의 아웃바운드 요청 가능. C-1 수정(scheme 화이트리스트 + 사설 IP 차단)으로 함께 해소.

## MEDIUM

### M-1. osv-scanner 오프라인 미보장 + 에러의 조용한 0건 처리
- osv-scanner가 실제로 오프라인(로컬 DB) 모드로 도는지 미보장 → 런타임 외부 조회 시 [COST-05]/오프라인 원칙 위반 소지. 또한 실행 에러가 "findings 0건"으로 **조용히 흡수**되어 실패가 성공(무결점)으로 오인될 위험.
- 수정: osv-scanner 오프라인/로컬 advisory DB 사용 명시(빌드타임 번들), 실행 실패(정상 exit 코드 외)는 0건이 아니라 스캔 `failed` 또는 명시적 에러 표면화. "락파일 없음"(정상 0건)과 "실행 실패"를 구분.

### M-2. 업로드 토큰 org 미스코프
- `upload_token`이 org에 바인딩되지 않아 토큰 확보 시 타 org가 staging 소스를 스캔에 참조할 여지.
- 수정: 업로드 시 `scan-src:<token>→org_id`(Redis TTL) 기록하고, SAST 생성 시 요청 org와 일치 검증(불일치 404).

## LOW (비차단)
- **L-1** scanworker cleanup/정리 관련 경미 항목(EPHEM 불변식 자체는 통과).
- **L-2**, **L-3** 비차단(문서/견고성). 상세는 재감사 시 확인.

---

## 통과한 불변식 (코드 근거 확인)
- **[EPHEM-01]**: `Acquire` 직후 `defer cleanup()`가 성공/실패/타임아웃/취소/panic 전 경로 커버, `scans`/`scan_findings`에 소스 본문 컬럼 부재, evidence 스니펫 ≤10줄 상한 강제, tmpfs 전용 경로. **통과**.
- **[SC-01]**: 모든 DAST(baseline·active)가 `IsDomainVerified` 게이트 순서 준수. **통과**.
- **[COST-05]**: 신규 유료 SDK 0, Semgrep 번들 규칙(`$SEMGREP_RULES_DIR`, `--config auto` 미사용). (단 osv 오프라인은 M-1). **대체로 통과**.
- **RLS**: scanner findings write/read가 `RunInOrg`/`q Querier` 관통, 0009 신규 컬럼도 기존 `org_isolation` RLS로 보호, 스캐너는 store 우회 없음. **통과**.
- **회귀**: Dockerfile 베이스 변경에도 k6(S1) 멀티스테이지 COPY 보존, 헤더 rule_id 개명 영향 문서화. **통과**.
- **업로드 경로 traversal**: zip-slip/path traversal 방어 존재. **통과**(단 clone 경로가 C-1).

---

## 재감사 조건 (backend-builder 처리 대상)
1. **C-1** (필수, 차단 해제): repo_url scheme 화이트리스트(https만)+host 검증, git clone `--`+`protocol.ext/file.allow=never`+`GIT_TERMINAL_PROMPT=0`+`--depth 1 --single-branch`, 사설/메타데이터 IP 차단.
2. **H-1**: C-1과 함께 SSRF 게이트로 해소.
3. **M-1**: osv-scanner 오프라인 보장 + 실행 실패를 0건으로 흡수하지 말 것.
4. (권장) **M-2**: 업로드 토큰 org 스코프.

C-1/H-1/M-1 수정 후 재감사 시 해당 경로 재확인 + 그린 테스트로 판정 갱신 예정.
