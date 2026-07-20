# klaro 부하 테스트 서비스 (MVP)

도메인 검증 → 부하 테스트 생성 → 로컬 k6 워커 실행 → WebSocket 실시간 메트릭 → 결과 요약까지의
end-to-end 수직 슬라이스. 설계: `docs/superpowers/specs/2026-07-19-load-test-service-design.md`.

## 구성
- `api` — Control Plane(REST/WS), Go/Gin
- `worker` — 잡 소비 + k6 실행 + 집계, Go
- `postgres` — 메타데이터, `redis` — 잡 큐 + 메트릭 pub/sub

## 로컬 실행
```bash
cd services/load-test
docker compose up --build -d
curl -s localhost:8080/healthz   # {"status":"ok"}
```

> **마이그레이션 갱신 시 주의**: postgres는 명명 볼륨(`klaro_pgdata`)에 데이터를 유지하므로, `migrations/*.sql`을 추가·수정하면 `docker-entrypoint-initdb.d`는 **데이터 디렉토리가 비어야만** 재실행된다. 변경분을 반영하려면 `docker compose down -v` 후 재기동하라(익명 볼륨 잔존으로 인한 위양성 방지).

## 수동 e2e 스모크
1. 스텁 대상 서버 기동(정상): `ADDR=:9000 go run ./testdata/target`
2. 대상 호스트를 검증 도메인으로 시드(개발 편의):
   ```sql
   INSERT INTO verified_domains (org_id, project_id, domain, method, token, status, verified_at)
   VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002',
           'host.docker.internal','file','x','verified', now());
   ```
3. 부하 테스트 생성(dev-token → dev user/org; `X-Org-Id`는 dev-org 기본값 생략 가능):
   ```bash
   curl -s -X POST localhost:8080/projects/00000000-0000-0000-0000-000000000002/load-tests \
     -H 'Authorization: Bearer dev' -H 'Content-Type: application/json' \
     -d '{"target_url":"http://host.docker.internal:9000","scenario":{"vu":5,"duration_sec":5,"ramp_up_sec":1,"steps":[{"method":"GET","path":"/"}],"thresholds":{"error_rate":0.5}}}'
   ```
4. 상태가 `completed`가 될 때까지 폴링:
   `curl localhost:8080/load-tests/<id> -H 'Authorization: Bearer dev'`
5. 결과: `curl localhost:8080/load-tests/<id>/results -H 'Authorization: Bearer dev'`
6. 실시간 스트림: `websocat ws://localhost:8080/load-tests/<id>/stream`

## 서킷브레이커 확인
대상 서버를 `MODE=fail ADDR=:9000 go run ./testdata/target`로 기동한 뒤 위 절차 반복 →
상태가 `aborted`, `aborted_reason`에 `error_rate > 0.80` 기록.

## 테스트
```bash
go test ./...                                   # 단위 테스트
go test -tags integration ./...                 # 통합(환경변수 TEST_DATABASE_URL / TEST_REDIS_ADDR 필요)
```

## 인증 / 멀티테넌시 (Phase 1)
- **RLS 강제**: 앱은 `klaro_app`(NOBYPASSRLS) 롤로 접속. 요청당 tx + `set_config('app.current_org')`로 org 스코프. `klaro_system`(BYPASSRLS)은 인증 전/공유/ingest 부트스트랩 전용.
- **인증**: JWT Access(15분) + Redis 회전 Refresh(14일), API Key(`klaro_` 접두), dev-token(`APP_ENV=dev` 한정, dev user/org 매핑).
- **DSN 2개**: `DATABASE_URL`(klaro_app) / `SYSTEM_DATABASE_URL`(klaro_system).
- 리소스 API(`/projects/:id/...`, `/load-tests/:id` 등)는 `Authorization: Bearer <token>` + `X-Org-Id: <uuid>` 필요.
- 마이그레이션 0004~0008이 users/memberships/api_keys·org_id 백필·RLS·롤·dev 시드를 구성한다.
- 통합 테스트: `TEST_DATABASE_URL`(klaro_app), `TEST_SYSTEM_DATABASE_URL`(klaro_system), `TEST_REDIS_ADDR` 설정 후 `go test -tags integration ./...`.

## 알려진 한계 (MVP 의도)
- 워커 잡 소비는 at-most-once(크래시 시 유실). NATS JetStream 승격 시 해결.
- 에러 판정은 status 태그 4xx/5xx 근사.
- 초대 pending(`invitations`)·SAML은 Phase 1 범위 밖.
