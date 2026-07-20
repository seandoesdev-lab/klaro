# 03 backend-builder manifest — Security Scan (S2) MVP

Service: `services/load-test/` (Go). Built alongside existing load-test (S1). All existing functionality intact; `go build ./... && go vet ./... && go test ./...` green.

## Endpoints (auth group, Bearer dev token; project fixed by auth stub)
| Method | Path | Body | Success |
|--------|------|------|---------|
| POST | `/projects/:id/scans` | `{type:"sast"\|"dast", target_url?, pr_number?}` | 202 `{id, status:"pending"}` |
| GET | `/projects/:id/scans` | — | 200 `{data:[Scan]}` |
| GET | `/scans/:id` | — | 200 `Scan` |
| GET | `/scans/:id/findings` | — | 200 `{data:[ScanFinding]}` |
| PATCH | `/scans/:id/findings/:findingId` | `{status:"open"\|"ignored"\|"fixed", ignore_reason?}` | 200 `{id, status}` |

Errors: standard `{error:{code,message,details}}`.
- DAST without/invalid `target_url` → 400 `VALIDATION_ERROR`.
- DAST unverified target domain → 403 `DOMAIN_NOT_VERIFIED` (reuses `store.HostFromURL` + `IsDomainVerified`).
- bad type / bad finding status → 400 `VALIDATION_ERROR`.
- scan/finding missing → 404 `NOT_FOUND`.
- trigger derived server-side: `pr` if `pr_number` present else `manual`.

## Shapes
`Scan`: `{id, type, trigger, target_url?, pr_number?, status, score?, started_at?, finished_at?, created_at}` (project_id hidden).
`ScanFinding`: `{id, scan_id, rule_id, severity, title, file_path?, line?, finding_hash, status, ignore_reason?, created_at}`.
- severity ∈ critical|high|medium|low|info; scan status ∈ pending|running|completed|failed; finding status ∈ open|ignored|fixed.

## Scan job flow
1. `createScan` inserts scan (`pending`), enqueues `model.ScanJob{ScanID,ProjectID,Type,TargetURL}` on Redis list **`klaro:scan-jobs`** (separate from load-test `klaro:jobs`).
2. `worker.RunScan` (wired in `cmd/worker/main.go` as `go worker.RunScan(...)` alongside `worker.Run`) dequeues via `DequeueScan` (BRPop 5s), sets `running`, runs DAST/SAST, `SaveFindings`, computes score, sets `completed` (or `failed`).
3. Scan state machine enforced in `store.UpdateScanStatus` (`model.CanScanTransition`): pending→running→completed/failed; started_at/finished_at stamped automatically.

## DAST (real, lightweight)
`worker.AnalyzeHeaders(http.Header, location)` — pure, unit-tested, no network. Worker fetches target (10s HTTP client) then analyzes:
- missing `Strict-Transport-Security` → high (`dast.missing-hsts`); present but `max-age < 15552000` (~180d) → high (`dast.weak-hsts`).
- missing `Content-Security-Policy` → medium (`dast.missing-csp`).
- missing `X-Frame-Options` → low; missing `X-Content-Type-Options` → low.
- `Server` header with version → low (`dast.server-version-disclosure`); bare software → info.
- missing `X-XSS-Protection` → info.
Each finding gets `finding_hash = sha256(rule_id + "|" + location)` (stable across re-scans → triage state preserved).

## SAST (stub — clearly placeholder, stores real rows)
`worker.stubSAST` returns 2 findings (`sast.hardcoded-secret` medium, `sast.weak-crypto` low) with file/line and `[STUB]`-prefixed titles noting full Semgrep/GitHub integration is future. No repo checkout yet (tmpfs/Semgrep/osv-scanner + PR checkout is future work per skill S2).

## Score
`model.ComputeScore`: start 100, subtract critical 25 / high 15 / medium 8 / low 3 / info 1, floor 0. Stored on scan on completion.

## Files
- Added: `migrations/0002_scans.sql`, `internal/model/scan.go`, `internal/model/scan_test.go`, `internal/store/scans_store.go`, `internal/worker/scanworker.go`, `internal/worker/scan_analysis_test.go`, `internal/api/scans.go`.
- Modified: `internal/queue/queue.go` (+`ScanQueue` iface), `internal/queue/redis.go` (+`EnqueueScan`/`DequeueScan`, key `klaro:scan-jobs`), `internal/api/router.go` (+`ScanQueue` dep, 5 routes), `cmd/api/main.go`, `cmd/worker/main.go`.

## Migration note
`migrations/0002_scans.sql` must be applied to the running Postgres (orchestrator applies it). Creates `scans`, `scan_findings` + indexes `scan_findings(scan_id,status)`, `scan_findings(finding_hash)`.

## Not done / future (out of MVP scope)
- SAST real engine (Semgrep + osv-scanner) and PR/tmpfs source checkout.
- DAST active scanning (ZAP headless) — current is passive header posture only.
- RLS policies / `SET app.current_org` (auth still a dev-token stub shared with S1; RLS is a cross-cutting item for the whole service).
- Pagination (`?limit=&cursor=`) on list endpoints (returns full `{data:[]}` like S1).
