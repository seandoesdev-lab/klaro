---
name: klaro-dashboard-design
description: klaro 대시보드 화면을 기존 디자인 시스템으로 구현하는 방법. 부하 테스트 생성 폼·실시간 WS 대시보드·결과 리포트 카드·도메인 검증 상태 등 klaro 프런트엔드 화면을 만들거나 수정할 때 반드시 사용. 디자인 토큰, 컴포넌트, 차트 빌더, WS 바인딩, 상태 디자인 규칙을 담는다.
---

# klaro 대시보드 디자인

klaro는 이미 확정된 디자인 시스템이 있다. **새로 디자인하지 말고, 원본에서 이식**한다.

## 디자인 원본 (반드시 먼저 읽기)
- `docs/klaro/prototype.html` — 실동작 프로토타입. 아래 라인 범위를 그대로 이식:
  - **디자인 토큰** `:root` light/dark/`[data-theme]` (약 1~60행): 색·간격·타이포·그림자·라디우스·`--mono`/`--sans`.
  - **컴포넌트 CSS** (약 61~365행): topbar, sidebar, `.card`, `.kpi`, `.badge`, `.dot`, `.btn`, gauge/meter, `.cb-banner`(서킷브레이커), `.toast`, 폼/슬라이더, 반응형.
  - **차트 빌더 JS** (약 529~606행): `lineChartSVG`(다중 라인+임계선+area), `gaugeSVG`, `sparkline`, `stackbarHTML`.
- `docs/klaro/05-ui-ux-design.md` — 화면별 레이아웃·UX 포인트·상태 디자인·색 시맨틱.
- `docs/klaro/03-api-spec.md` — API 요청/응답 계약.

## 설계 원칙 (명세 §1)
1. **한 눈에**: 핵심 결론(판정·KPI)을 스크롤 없이 상단에.
2. **결론 → 근거**: 판정 먼저, 그래프·수치는 아래.
3. **상태를 색으로**: 정상=good(초록)/경고=warn(앰버)/위험=crit(레드)/중립=muted. **색만으로 판단 금지 — 아이콘·라벨 병기**(색맹 대응).
4. **불안 해소**: 서킷브레이커·도메인 검증을 "안전 약속"으로 명시.

## 색 시맨틱 (전 화면 일관)
| 의미 | CSS 변수 | 아이콘 |
|------|----------|--------|
| 정상/통과 | `--good` | ● / ✓ |
| 경고/주의 | `--warn` | ⚠ |
| 위험/실패 | `--crit` | ✕ / 🔴 |
| 중립/정보 | `--muted` | ○ |
지연 시리즈: P50 `--s-p50`, P95 `--s-p95`, P99 `--s-p99`.

## 부하 테스트 대시보드 필수 구성
1. **새 부하 테스트 폼** (명세 §4.2): 대상 URL(검증 상태 배지) + 간단 모드(VU·지속시간 슬라이더, `input[type=range]`) + 실시간 사용량 미리보기 + `.safety-note`(에러율 80% 초과 자동 중단). 미검증 도메인이면 시작 버튼 `disabled` + 인라인 안내(명세 §7).
2. **실시간 대시보드** (명세 §4.3): 상단 `.live-chip`(맥동) + 경과/총 시간. **KPI 타일 4개**(현재 VU·RPS·P95 지연·에러율) 각 상태색·`.kpi::before` 컬러바. **지연 라인차트**(`lineChartSVG`, 임계선 점선). 서킷브레이커 `.cb-banner`(정상=good, 발동 시 `.is-crit` + 토스트). 슬라이딩 윈도로 최근 N포인트 유지.
3. **결과/판정 카드** (명세 §4.4): 완료 시 요약(RPS·P50/95/99·에러율·병목 엔드포인트) + 판정 배지(점수 기준 `verdictOf`: ≥85 배포가능/≥70 조건부/그 외 위험). 프로토타입 `report-hero`·`gauge`·`meter` 재사용.

## 실 API 바인딩 (같은 오리진 :8080)
인증: 모든 REST·WS 호출에 `Authorization: Bearer <dev-token>`(기본 `dev`).

| 동작 | 호출 |
|------|------|
| 도메인 목록 | `GET /projects/{pid}/domains` |
| 테스트 생성 | `POST /projects/{pid}/load-tests` body `{target_url, scenario:{vu,duration_sec,ramp_up_sec,steps:[{method,path}],thresholds:{error_rate,http_req_duration_p95_ms}}}` |
| 상태 폴링 | `GET /load-tests/{id}` → `status` |
| 실시간 | `WS /load-tests/{id}/stream` → `{ts,rps,latency_p95_ms,error_rate,active_vu}` / abort 시 `{event:"aborted",reason}` |
| 결과 | `GET /load-tests/{id}/results` → `{rps_avg,latency_p50,latency_p95,latency_p99,error_rate,max_vu_before_degradation,bottleneck_endpoint}` |

> WS는 브라우저 `WebSocket`이 헤더를 못 붙이므로, 스트림은 인증 없이 열되(개발 스텁) id 기반 구독으로 처리하거나, 토큰을 쿼리(`?token=`)로 전달하는 방식을 API와 맞춘다. 실제 라우트(`internal/api/router.go`·`ws.go`)를 읽어 현 계약을 확인하고 그에 맞춘다.

고정 개발 프로젝트 id: `00000000-0000-0000-0000-000000000002`.

## 같은 오리진 서빙 (CORS 회피)
`internal/api/router.go`에서 `//go:embed web/*` 로 `web/index.html`을 바이너리에 포함하고 `GET /`(및 정적 자산)에서 서빙한다. distroless 이미지에서도 동작한다. 이러면 REST·WS가 동일 오리진이라 CORS 설정이 불필요하다.

## 상태 디자인 (명세 §7)
빈 상태(첫 테스트 CTA)·로딩(스켈레톤)·스트리밍(LIVE 맥동)·스트림 끊김(그래프 회색 + "응답 없음")·서킷브레이커(안전 종료 배너 + 부분 결과)·도메인 미검증(버튼 비활성 + 안내).

## 접근성 (명세 §8)
색+아이콘+라벨 병기, 실시간 값 `aria-live="polite"`, 대비 4.5:1, 포커스 링(`:focus-visible`), 차트에 `role="img"`+`aria-label`, `prefers-reduced-motion` 시 애니메이션 정지.

## 검증
로컬 Go 미설치 → `golang:1.25` Docker로 `go build ./...`. 그 뒤 `docker compose up`으로 브라우저 e2e(대시보드 로드 → 검증 도메인 대상 테스트 → 실시간 그래프 → 결과 표시).
