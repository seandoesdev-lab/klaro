# P2-05 QA 검증 리포트 — S2 보안 스캔 실엔진 (Phase 2)

- 작성: qa-verifier · 작성일 2026-07-20 · 대상 모듈 `services/load-test`
- 입력: `_workspace/p2_03_backend-builder_manifest.md`, `_workspace/p2_02_architect_design.md`, `_workspace/p2_01_spec-analyst_contract.md`
- 검증 환경: Windows 11 호스트, Go 1.26.0, Docker 29.6.1 / Compose v5.3.0. **네트워크 사용 가능** → 이미지 빌드/스캐너 번들 전부 실측 성공.
- 방식: 유닛/통합 테스트 실행 + 라이브 API 호출 + 워커 이미지 빌드·인컨테이너 실행 + psql 직접 대조. 정적 대조는 보조로만 사용.

## 종합 판정

| # | 항목 | 판정 | 근거 |
|---|------|------|------|
| 1 | 환경 기동(이미지 빌드·마이그레이션·healthz) | **PASS** | worker/api 이미지 빌드 exit 0, 0001~0009 적용, healthz 200, worker tmpfs 가드 통과 |
| 2 | [EPHEM-01] 소스 소멸(최우선) | **PASS** | 통합테스트 + 라이브 실증 + tmpfs 실측 + DB 무잔존. 위양성 능동 점검 완료 |
| 3 | SAST 실엔진 | **FAIL (High)** | semgrep 전체 룰(`--config /opt/semgrep-rules`, 2151개) 이 300s 안에 완료 못함 → 실 SAST 항상 timeout→failed. **재현 절차 아래 D-1** |
| 4 | DAST 게이트·헤더·dedup | **PARTIAL PASS** | SC-01 게이트(baseline·active) 403 라이브 확인, 헤더/dedup 유닛 PASS. **ZAP 실엔진 e2e 는 미수행**(설계상 수동 범위, ZAP 데몬 미기동) |
| 5 | RLS 회귀(scan_findings 신규 컬럼) | **PASS** | 크로스 org·세션 미설정 모두 scans/scan_findings 0건, FORCE RLS 실측 |
| 6 | SAST 소스 XOR | **PASS** | 둘 다/둘 다없음 → 400 라이브 확인 |
| 7 | API shape | **PASS** | 매니페스트 §2 엔드포인트 전부 라이브 동작(업로드 201, 스캔 202, 검증 400/403) |
| 8 | S1 회귀(k6 보존) | **PASS** | worker 이미지 내 `k6 v2.1.0` 정상 실행, `K6_PATH=k6` 유지 |

> **결함 1건(High): 항목 3 SAST semgrep 타임아웃.** Ephemeral 위반은 없음. 상세·재현은 아래 「결함」.

---

## 항목별 상세

### 1. 환경 기동 — PASS
- `docker compose build worker` **exit 0**. python:3.12-slim 베이스에 semgrep 1.170.0(pip), osv-scanner 1.9.1(정적 바이너리), git 2.47.3, semgrep-rules(4283 파일 clone) 번들. k6 는 멀티스테이지 COPY 로 보존.
- 인컨테이너 실측:
  - `semgrep --version` → `1.170.0`
  - `osv-scanner --version` → `1.9.1`
  - `k6 version` → `k6 v2.1.0 (…, go1.26.4, linux/amd64)` ← **S1 회귀 없음**
  - `git --version` → `2.47.3`
  - `/opt/semgrep-rules` yaml 규칙 수 **2151**, `SEMGREP_RULES_DIR=/opt/semgrep-rules OSV_BIN=osv-scanner SEMGREP_BIN=semgrep K6_PATH=k6`
- 마이그레이션 0001~0009 initdb 적용 확인. `\d scans` → source_type/source_ref/mode + CHECK 2건(chk_scans_source_type, chk_scans_mode). `\d scan_findings` → evidence(jsonb)/cwe/confidence/package/package_version.
- 전체 컬럼 덤프로 **소스 본문 컬럼 부재** 확인(scans 는 메타데이터 3컬럼, scan_findings 는 증거 5컬럼만).
- api/worker 컨테이너 기동, healthz 200. worker 로그 `worker started` → **AssertTmpfs 가드 통과**(log.Fatal 없음).

### 2. [EPHEM-01] 소스 소멸 — PASS (최우선, 위양성 능동 점검 포함)
증거를 4중으로 확보:

1. **통합 테스트(klaro_app 비-슈퍼유저 DSN)** — `go test -tags integration ./internal/worker/`:
   - `TestEphemeralCleanup` PASS (성공·강제실패 후 `/scan-work` 잡 디렉터리 0개)
   - `TestNoSourceInDB` PASS (canary 문자열이 scans/scan_findings 전 text·jsonb 컬럼에서 0건)
   - DSN 은 `postgres://klaro_app:...`(rolbypassrls=**f**) 사용 확인.
2. **라이브 실증** — compose 스택에서 취약 코드(subprocess shell=True / eval) 업로드→SAST 실행. 스캔 종료(failed) 후:
   - `docker exec worker ls -A /scan-work` → **빈 값**, `ls -A /scan-src` → **빈 값** (실패 경로에서도 cleanup + staging 소비 확인)
   - `SELECT count(*) FROM scan_findings WHERE …~'subprocess|shell=True|eval\('` → **0**
   - scans.source_ref 는 업로드 토큰(`df2a5589…`)·repo URL 만 저장, 소스 본문 없음.
3. **tmpfs 실측(위양성 방어)** — `stat -f -c %T /scan-work` 를 **실제 실행 컨테이너**에서 → `tmpfs`. compose config 상 `/scan-work` 는 `tmpfs:size=2g,mode=1777`, `scan_src` 볼륨은 `driver_opts type=tmpfs device=tmpfs`. 두 소스 경로 모두 RAM.
4. **위양성 결론** — 통합테스트는 `t.TempDir()`(비-tmpfs)에서 돌지만, 그 테스트는 tmpfs 잔존을 주장하지 않고 **cleanup(RemoveAll) + DB 무잔존만** 검증한다(정직). 실제 tmpfs 잔존 강제는 (a) compose tmpfs 마운트, (b) 워커 기동 `AssertTmpfs`(linux) 두 층이 담당하며, **둘 다 라이브로 통과**(워커 비-fatal + `stat -f`=tmpfs). 즉 "tmpfs assert 우회로 실제 tmpfs 아님에도 통과" 하는 위양성은 없음.

`AssertTmpfs`(tmpfs_linux.go)는 `GOOS=linux go build ./internal/scanner/` 크로스컴파일 그린으로 컴파일 검증, 런타임에서 실제 tmpfs 판정 통과.

### 3. SAST 실엔진 — **FAIL (High)** → 결함 D-1
- 스캐너 코드/파싱은 정상: `parseSemgrep`/`parseOSV` 유닛 PASS, `sast.semgrep.*`/`osv.*` 네임스페이스, hash 라인시프트 안정성 PASS, `[STUB]`/`stubSAST` 코드 전량 제거 확인(grep 0건).
- **그러나** 라이브 SAST(업로드·repo clone 양 경로) 3건 모두 `failed`. 워커 로그: `semgrep timed out: context deadline exceeded`.
- 근본 원인 재현(결함 D-1): `--config /opt/semgrep-rules`(2151 규칙, ~30개 언어)가 300s(SemgrepTimeoutSec) 내 완료 불가. 상세 아래.
- 결과적으로 [SAST-01](실 finding 저장)·[SAST-02](osv 통합)·"0건→completed score=100" 수용 기준이 **런타임에서 미충족**. (osv 는 semgrep 타임아웃으로 병렬 결과가 버려지므로 동반 실패.)

### 4. DAST — PARTIAL PASS
- **[SC-01] 도메인 게이트 라이브 확인**: 미검증 도메인 대상
  - `{"type":"dast","target_url":"https://unverified.example.com/"}` → **403 DOMAIN_NOT_VERIFIED**
  - `{"…","mode":"active"}` → **403 DOMAIN_NOT_VERIFIED** (active 도 동일 게이트, 약화 없음)
  - target_url 누락 → 400 VALIDATION_ERROR. 게이트는 mode 분기 이전에 위치(정적·런타임 일치).
- 헤더 분석 `dast.header.*` 네임스페이스·해시 안정성 유닛 PASS. `DedupDAST`(ZAP 우선 억제) 유닛 PASS.
- **미수행**: ZAP 데몬 실엔진 e2e(spider→ascan→alerts). 설계 §7·매니페스트 §7 에서 "자동 integration 은 파싱 유닛+nil/fake, ZAP 바이너리 e2e 는 수동 범위"로 명시. zap 서비스는 본 검증에서 기동하지 않음(대형 이미지, SAST 우선). `zap.*` 파싱은 `TestParseZAPAlerts` 로만 커버. → **런타임 미검증 잔여**로 명시.

### 5. RLS 회귀 — PASS
- `go test -tags integration ./internal/store/`: `TestRLSCrossOrgBlocked`, `TestRLSSessionUnsetReturnsZero` PASS.
- 직접 대조(klaro_app 풀): 오답 org(`1111…`) SET 후 `SELECT count(*)` → scans 0 / scan_findings 0. 세션 미설정 → scans 0 / scan_findings 0.
- `pg_class`: scans·scan_findings 모두 relrowsecurity=t, relforcerowsecurity=t. 0009 의 ADD COLUMN 은 정책 무영향 → 신규 컬럼 자동 org 스코프 보호.
- 잔여: scan_findings **전용** 크로스-org 통합테스트 케이스는 없음(메커니즘은 load_tests 로 증명 + FORCE RLS 구조 실측). 기능상 보호는 위 직접 대조로 확인됨.

### 6. SAST 소스 XOR — PASS
- 둘 다 지정 → 400 `"sast requires exactly one of repo_url or upload_token"`
- 둘 다 없음 → 400 (동일 메시지)
- upload_token staging 부재 → 400 `"upload_token not found or expired"`
- repo_url 단독 → 202, upload_token 단독(유효) → 202.

### 7. API shape — PASS (매니페스트 §2 대조)
- `POST /projects/:id/scans/source`: multipart `file` → **201** `{upload_token,expires_in:3600}`. staging 에 `<token>.zip` 만 기록. 미지원 확장자 → 400, file 필드 없음 → 400.
- `POST /projects/:id/scans`: 202 `{id,status:"pending"}`, 검증 400/403 위와 일치.
- `GET /scans/:id`, `GET /scans/:id/findings`, `GET /projects/:id/scans` 라우트·권한(min=member/viewer) router.go 와 일치.
- 신규 응답 필드(cwe/confidence/package/package_version/evidence) omitempty 로 모델 반영.

### 8. S1 회귀 — PASS
- 베이스 grafana/k6→python:3.12-slim 전환 후에도 worker 이미지에 `k6 v2.1.0` 정상. `K6_PATH=k6` 유지. 부하 워커 코드 무변경.
- (도메인 검증 시드→부하 스모크 전 과정은 미실행; k6 바이너리 가용성·부하 워커 코드 무변경으로 회귀 위험 낮음으로 판단.)

---

## 결함

### D-1 (심각도 High / 기능): SAST semgrep 전체 룰셋 타임아웃 → 실 SAST 항상 실패
- **요약**: `internal/scanner/semgrep.go` 가 `--config $SEMGREP_RULES_DIR`(=`/opt/semgrep-rules`, 2151개 다국어 규칙)로 실행되는데, 규칙 로딩/컴파일만으로 `SemgrepTimeoutSec=300`(scanner.go)을 초과한다. 따라서 업로드·repo clone 어느 경로든 실 SAST 스캔이 항상 `failed` 로 끝나 finding 이 저장되지 않는다. [SAST-01]/[SAST-02] 수용 기준 런타임 미충족.
- **재현**(인컨테이너, 3줄짜리 파일):
  ```
  docker exec load-test-worker-1 sh -c '
    printf "import subprocess\ndef r(c):\n return subprocess.call(c, shell=True)\n" > /tmp/t/app.py
    timeout 290 semgrep --config /opt/semgrep-rules --json --metrics=off --quiet --no-git-ignore /tmp/t > /tmp/o.json'
  # 결과: exit=124(timeout), /tmp/o.json 0바이트  (290s 내 미완료)
  ```
  대조군(범위 축소 시 정상): `--config /opt/semgrep-rules/python`(337 규칙) → **49s, exit 0, results 2건**
  (`python.lang.security.audit.dangerous-subprocess-use-audit`, `…subprocess-shell-true`).
- **라이브 확인**: 업로드 SAST(scan 2bb1a8c0), repo SAST(scan 3468ad8a) 모두 워커 로그 `semgrep timed out: context deadline exceeded` → status=failed.
- **경계면 위치**: `internal/scanner/semgrep.go:35-40`(exec 인자), `internal/scanner/scanner.go:60`(SemgrepTimeoutSec=300). Dockerfile.worker:21(전체 semgrep-rules clone).
- **권장 수정(택1 이상)**:
  1. `--config` 를 전체 저장소가 아니라 **큐레이트된 소규모 팩**(예: 대상 언어 subdir 자동 선택, 또는 오프라인 번들로 고정한 `p/ci`·`p/security-audit` 상당 서브셋)으로 축소.
  2. SemgrepTimeoutSec 를 현실적 값(예: 900s+)으로 상향하되 규칙 프리컴파일/캐시 도입.
  3. 언어 감지 후 해당 언어 룰 디렉터리만 지정.
- **부수 관찰(cosmetic)**: 디렉터리 `--config` 사용 시 semgrep check_id 가 경로기반이 되어 rule_id 가 `sast.semgrep.opt.semgrep-rules.python.lang.security…` 형태로 길어짐. 네임스페이스 계약(`sast.semgrep.*`)은 유지되나 표시가 지저분. 수정안 1/3 채택 시 자연 완화.
- **상태**: OPEN. 빌더(backend-builder) 통지·수정 태스크 등록 필요(본 환경에 SendMessage/TaskCreate 도구 미제공 → 오케스트레이터가 라우팅).

---

## 검증 못한/잔여 범위 (정직 고지)
- **ZAP 실엔진 e2e**: zap 데몬 미기동. `zap.*` 파싱은 유닛만. (설계상 수동 범위로 분류됨.)
- **S1 부하 잡 e2e 스모크**: k6 바이너리 가용성만 확인, 실제 부하 잡 상태전이는 미실행.
- **scan_findings 전용 크로스-org 통합 케이스**: 직접 psql 대조로 보강했으나 Go 테스트 케이스는 부재.
- 잔존 통합테스트 데이터(fakeSAST completed 스캔 3건, findings 5건)가 DB 에 남아 있음 — `docker compose down -v` 로 초기화 가능.

## 재검증 방법(요약)
```
cd services/load-test
docker compose up -d postgres redis
TEST_DATABASE_URL="postgres://klaro_app:klaro_app@localhost:5432/klaro?sslmode=disable" \
TEST_SYSTEM_DATABASE_URL="postgres://klaro_system:klaro_system@localhost:5432/klaro?sslmode=disable" \
SCAN_WORK_TMPFS_ASSERT=0 go test -tags integration ./internal/worker/ ./internal/store/ -v
docker compose up -d --build         # worker/api/zap
# 라이브 SAST: 업로드→scan→poll (D-1 재현: 항상 semgrep timeout)
```
