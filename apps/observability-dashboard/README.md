# klaro 상시 관측 대시보드

klaro **상시 관측 플랫폼(S3)** 의 프런트엔드다. `services/observability`(obsplane)의
REST/WebSocket 표면을 소비해 메트릭·트레이스·로그를 탐색하고, 라이브 KPI를 보고,
알림 룰과 대시보드를 관리한다.

배포 적합성 리포트(S1/S2/S4) 대시보드와는 **별도 앱**이다. 05-ui-ux-design.md §3의
2026-08-23 노트대로 상시 관측은 프로젝트 하위가 아니라 최상위 내비게이션이며,
수집 단위가 프로젝트가 아니라 org 관측 키이기 때문이다.

## 실행

```bash
npm install
cp .env.example .env.local     # 값 조정
npm run dev                    # http://localhost:3100
```

기본값은 `NEXT_PUBLIC_OBS_MOCK=1`(mock 모드)이다. obsplane 없이도 모든 화면이
실제 응답 shape 그대로 동작한다. 실제 백엔드에 붙이려면 `.env.local`에서
`NEXT_PUBLIC_OBS_MOCK=0`으로 두고 `NEXT_PUBLIC_OBS_API_BASE` · `NEXT_PUBLIC_OBS_ORG_ID` ·
`NEXT_PUBLIC_OBS_TOKEN`을 채운다.

## 화면

| 경로 | 화면 | 소비 API | 요구사항 |
|------|------|----------|----------|
| `/live` | 라이브 대시보드 (KPI ≤2초 갱신) | WS `/obs/live`, `/obs/quota` | OBS-01, APM-02 |
| `/metrics` | 메트릭 Explorer (라인차트 + 해상도 배지) | `/obs/metrics/query` | OBS-03 |
| `/traces` | 트레이스 목록 | `/obs/traces` | OBS-04 |
| `/traces/[traceId]` | 스팬 워터폴 | `/obs/traces/:id` | OBS-04, APM-03 |
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

API 클라이언트(`src/lib/api/client.ts`)가 모든 요청에 `Authorization: Bearer <token>`을
붙인다. obsplane은 현재 개발용 단일 토큰(`tenancy.DevTokenAuthenticator`)을 받고 JWT로
하드닝 중이지만, 클라이언트 입장에서는 동일하다.

**알려진 간극 — WebSocket 자격 증명**: 브라우저 WebSocket API는 핸드셰이크에
`Authorization` 헤더를 붙일 수 없다. obsplane의 `getLive`는 헤더만 읽으므로, mock을 끈
상태에서 라이브 스트림을 붙이려면 서버가 다음 중 하나를 받아들여야 한다:

- `?access_token=<토큰>` 쿼리 파라미터 (`NEXT_PUBLIC_OBS_WS_AUTH_MODE=query`, 기본값)
- `Sec-WebSocket-Protocol: klaro-bearer,<토큰>` 서브프로토콜
  (`...=subprotocol`; 서버가 선택한 서브프로토콜을 **되돌려줘야** 브라우저가 연결을 유지한다)

셋째 선택지는 인증을 종단하는 리버스 프록시다. 어느 쪽으로 정할지는 백엔드 하드닝
작업의 결정 사항이라 클라이언트는 세 모드를 모두 설정으로 열어두었다.

## 디자인

`docs/klaro/prototype.html`의 디자인 토큰과 앱 셸(상단바 + 사이드바, 라이트/다크)을
그대로 복제한다. 토큰이 두 곳에 존재하므로 한쪽을 바꾸면 `src/app/globals.css`도 함께
갱신해야 한다(CLAUDE.md 문서 지도). 상태색은 05 §5의 초록/앰버/레드 시맨틱을 따르고,
색만으로 판단하지 않도록 아이콘·라벨을 병기한다.

## 검증

```bash
npm run typecheck   # tsc --noEmit
npm run build       # next build
```

헤드리스 스모크는 `next start` 후 시스템 Chrome + puppeteer-core로 돌렸다: 6개 라우트의
앱 셸·본문 텍스트, 차트 SVG 렌더, 워터폴 스팬, 테마 토글, 빈 상태, 알림 룰 생성·토글,
대시보드 패널 추가·저장. 콘솔 오류 0건.

## 비용

새로 추가한 런타임 의존성은 `next` · `react` · `recharts`뿐이다. 유료 외부 서비스 호출은
없다([COST-05]).
