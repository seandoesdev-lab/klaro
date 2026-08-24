# klaro 상시 관측 대시보드

klaro **상시 관측 플랫폼(S3)** 의 프런트엔드다. `services/observability`(obsplane)의
REST/WebSocket 표면을 소비해 메트릭·트레이스·로그를 탐색하고, 라이브 KPI를 보고,
알림 룰과 대시보드를 관리한다.

배포 적합성 리포트(S1/S2/S4) 대시보드와는 **별도 앱**이다. 05-ui-ux-design.md §3의
2026-08-23 노트대로 상시 관측은 프로젝트 하위가 아니라 최상위 내비게이션이며,
수집 단위가 프로젝트가 아니라 org 관측 키이기 때문이다.

## 실행

### mock 모드 (백엔드 없이)

```bash
npm install
cp .env.example .env.local     # NEXT_PUBLIC_OBS_MOCK=1 그대로
npm run dev                    # http://localhost:3100
```

obsplane 없이도 모든 화면이 실제 응답 shape 그대로 동작한다. 기본값이 mock인 이유는
백엔드가 항상 옆에서 돌고 있지는 않고, 빈 화면이 명백히 가짜인 데이터보다 나쁜
기본값이기 때문이다.

### 실데이터 (로컬 풀스택)

```bash
# 저장소 루트에서 — compose 기동 + 시드 + 주입 + REST/WS 확인 + next build
./scripts/e2e-fullstack.sh

# .env.local은 위 스크립트가 써준다(토큰·org·MOCK=0). 그 다음:
cd apps/observability-dashboard && npm run dev     # http://localhost:3100/live
```

시드만 다시 하려면 `services/observability/deploy/scripts/seed-dev.sh`다 — org 행을
넣고, HS256 JWT를 발급하고, 수집 키를 발급하고, 이 앱의 `.env.local`을 다시 쓴다.
자세한 절차와 포트 표는 `services/observability/README.md`의 "로컬 풀스택 실행"에 있다.

세 변수만 알면 충분하다: `NEXT_PUBLIC_OBS_API_BASE`(기본 `http://localhost:8090`) ·
`NEXT_PUBLIC_OBS_ORG_ID` · `NEXT_PUBLIC_OBS_TOKEN`. 토큰의 `org_id` 클레임과
`NEXT_PUBLIC_OBS_ORG_ID`가 **같아야** 한다 — 다르면 REST는 403, 라이브 소켓은 4403이고
브라우저에서는 둘 다 서버 장애처럼 보인다.

## 화면

| 경로 | 화면 | 소비 API | 요구사항 |
|------|------|----------|----------|
| `/infrastructure` | 인프라 (호스트 테이블 + visx 육각 hostmap + 호스트 상세 + 업타임 SLO) | `/obs/hosts`, `/obs/hosts/:id/metrics`, `/obs/slo/uptime` | OBS-01 |
| `/live` | 라이브 대시보드 (KPI ≤2초 갱신) | WS `/obs/live`, `/obs/quota` | OBS-01, APM-02 |
| `/metrics` | 메트릭 Explorer (라인차트 + 해상도 배지) | `/obs/metrics/query` | OBS-03 |
| `/traces` | 트레이스 목록 | `/obs/traces` | OBS-04 |
| `/traces/[traceId]` | 트레이스 연계 분석(플레임그래프/워터폴 + 스팬별 로그·메트릭 탭) | `/obs/traces/:id/correlated` | OBS-04, APM-03 |
| `/logs` | 로그 Explorer | `/obs/logs` | OBS-05 |
| `/alerts` | 알림 룰 CRUD + 이벤트 목록 | `/obs/alert-rules`, `/obs/alert-events` | OBS-06, OBS-07 |
| `/dashboards`, `/dashboards/[dashId]` | 대시보드 목록·조회·최소 편집 | `/obs/dashboards` | OBS-09 |

## 설계 제약이 코드에 나타나는 지점

- **원시 질의를 보내지 않는다.** obsplane은 MetricsQL/TraceQL/LogQL을 받지 않고
  구조화된 파라미터만 받는다(조직 라벨을 서버가 강제 주입하기 위해서다,
  `internal/explorer` 패키지 문서). 그래서 UI가 구조화 폼인 것은 단순화가 아니라 **계약**이다.
  응답의 `query` 필드(서버가 생성한 질의)를 화면에 읽기 전용으로 노출해 주입이 확인 가능하게 했다.
- **해상도를 숨기지 않는다.** 넓은 구간은 롤업으로 답한다. `resolution`(raw/5m/1h)과
  `clamped`를 배지로 표시하지 않으면 1시간 버킷의 평평한 선이 "안정적"으로 오독된다.
- **끊긴 스트림은 끊긴 것처럼 보인다.** WS가 열려 있지 않으면 타일을 흐리게 하고 배너로
  알린다(05 §7 스트림 끊김). 마지막 값을 현재 값처럼 남겨두지 않는다.
- **WS 종료 코드는 이유다.** 4403(org 불일치)·4400(알 수 없는 스트림)은 재시도해도
  성공할 수 없으므로 재연결하지 않고 오류로 표시한다.
- **패널은 각자 조회한다.** 부분 실패 시 실패한 카드만 어두워지고 나머지는 유지된다(05 §7).

## 인증

API 클라이언트(`src/lib/api/client.ts`)가 모든 REST 요청에
`Authorization: Bearer <token>`을 붙인다. obsplane은 서명된 JWT를 검증하고
(`tenancy.JWTAuthenticator`: `org_id` · `role` · `exp` 필수), 개발도 프로덕션과 같은
경로다 — 다른 건 시크릿의 출처뿐이다. 로컬 토큰은
`services/observability/deploy/scripts/dev-token.mjs`가 찍는다.

### WebSocket 자격 증명 — 서브프로토콜로 확정

브라우저 WebSocket API는 핸드셰이크에 `Authorization` 헤더를 붙일 수 없다. 결정은
**서브프로토콜**이다:

```
Sec-WebSocket-Protocol: klaro-bearer, <token>
```

`liveProtocols()`가 이 두 값을 보내고, obsplane의 `getLive`가
`tenancy.SubprotocolToken`으로 읽고, 응답에 `klaro-bearer`를 **에코**한다(RFC 6455 §4.2.2).
에코가 없으면 브라우저는 인증에 성공한 연결을 스스로 끊으므로, 에코는 장식이 아니라
연결 조건이다. 토큰은 에코되지 않는다.

**쿼리 파라미터 모드는 없앴다.** URL에 실린 토큰은 경로상 모든 프록시의 액세스 로그,
브라우저 히스토리, 페이지가 외부로 링크할 때의 Referer에 남는다. 이 토큰 하나가 org
하나이므로 짧은 코드 경로와 바꿀 만한 거래가 아니다. `NEXT_PUBLIC_OBS_WS_AUTH_MODE`에
`query`를 넣으면 콘솔 경고와 함께 `subprotocol`로 처리된다 —
조용히 무시하면 설정은 틀린 채로 화면은 동작해서 아무도 알아채지 못한다.

REST 쪽은 헤더 그대로다. 서브프로토콜 확장은 라이브 라우트 **하나에만** 열려 있다
(`tenancy.AllowWSSubprotocolCredential`); 다른 라우트는 헤더만 읽는다.

### 교차 출처

대시보드(:3100)와 obsplane(:8090)은 다른 오리진이다. obsplane은 개발 프로파일에서
`OBS_DEV_CORS_ORIGINS`로 이 오리진을 허용하고(compose 기본값에 포함), 프리플라이트를
인증 앞에서 답한다. 프로덕션 프로파일은 이 변수를 거부한다 — 그때는 동일 오리진 서빙이나
게이트웨이가 정책을 갖는다.

## 디자인

`docs/klaro/prototype.html`의 디자인 토큰과 앱 셸(상단바 + 사이드바, 라이트/다크)을
그대로 복제한다. 토큰이 두 곳에 존재하므로 한쪽을 바꾸면 `src/app/globals.css`도 함께
갱신해야 한다(CLAUDE.md 문서 지도). 상태색은 05 §5의 초록/앰버/레드 시맨틱을 따르고,
색만으로 판단하지 않도록 아이콘·라벨을 병기한다.

## 검증

```bash
npm run typecheck   # tsc --noEmit
npm run build       # next build
npm audit           # 0 vulnerabilities 유지
```

백엔드까지 관통하는 검증은 저장소 루트의 `./scripts/e2e-fullstack.sh`다 — compose 기동,
시드, 합성 메트릭 주입, 라이브 WS 프레임 수신, Explorer 조회, `next build`를 단계별
PASS/FAIL로 찍는다.

### 의존성 어드바이저리

`next`는 `15.5.23`으로 올려 크리티컬 1건 + 하이 2건을 닫았다(semver-minor). 남은
`postcss` · `sharp` 하이 2건은 npm이 제안하는 유일한 수정이 `next@16`(major)이었는데,
프레임워크 메이저 업그레이드는 이 작업의 범위가 아니다. 둘 다 **자기 major 안에서**
패치되어 있으므로 `package.json`의 `overrides`로 올렸다(`postcss@^8.5.23`,
`sharp@^0.35.0`) — `npm audit`는 0건이고 `next build`는 그대로 통과한다.

헤드리스 스모크는 `next start` 후 시스템 Chrome + puppeteer-core로 돌렸다: 6개 라우트의
앱 셸·본문 텍스트, 차트 SVG 렌더, 워터폴 스팬, 테마 토글, 빈 상태, 알림 룰 생성·토글,
대시보드 패널 추가·저장. 콘솔 오류 0건.

## 비용

새로 추가한 런타임 의존성은 `next` · `react` · `recharts`뿐이다. 유료 외부 서비스 호출은
없다([COST-05]).
