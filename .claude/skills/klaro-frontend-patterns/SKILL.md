---
name: klaro-frontend-patterns
description: klaro Next.js 대시보드(온보딩·부하 생성·실시간 대시보드·배포 적합성 리포트·보안/APM 뷰)를 05-ui-ux 설계·디자인 토큰대로 구현하는 패턴. UI 컴포넌트·차트·WebSocket 실시간 화면·리포트 화면 구현/수정 시 반드시 사용. frontend-builder 에이전트 전용.
---

# klaro-frontend-patterns — 프론트 구현 패턴

## 착수 전
`docs/klaro/05-ui-ux-design.md` + `docs/klaro/prototype.html`(시각 기준) + `_workspace/03_backend-builder_manifest.md`(실제 API shape)를 읽는다.

## 디자인 원칙 (모든 화면)
- **결론 먼저**: 판정·종합 점수·핵심 문장 1줄을 최상단. 근거·그래프는 아래.
- **점진적 노출**: 간단 모드 기본, 고급은 접힘.
- **상태를 색+아이콘+라벨**로(색만으로 판단 금지): 정상=초록●/✓, 경고=앰버⚠, 위험=레드✕, 중립=그레이○.
- **10분 첫 성공**: 온보딩 3스텝(대상 입력 → 도메인 검증 → 실행·관찰).

## 디자인 토큰
prototype.html의 CSS 변수 체계(라이트/다크 쌍)를 그대로 이식. `@media (prefers-color-scheme)` + `[data-theme]` 토글 둘 다 지원. 등폭/tabular 숫자로 수치 정렬. 4px 스페이싱, 12컬럼·최대 1280px.

## 핵심 화면 바인딩 (경계면 정합 필수)
- 부하 생성: `domain_id` + `scenario.apis[{endpoint_id, weight}]`. 사이트 선택 → API 카탈로그 다중 체크 + 가중치 슬라이더(합 100%).
- 실시간 대시보드: WS `/load-tests/:id/stream`, 필드 `rps·latency_p95_ms·error_rate·active_vu`, 갱신 ≤2초. 임계선 점선 오버레이.
- 리포트: `/reports/:id`의 `performance_score·security_score·ai_summary`; 결과 `per_api[]`로 API별 분해·병목 API 강조.
- 보안 findings: `PATCH /scans/:id/findings/:id` + `finding_hash` 기준 무시 상태 유지.

## 상태 디자인 (빠짐없이)
Empty(첫 테스트 CTA) / Loading(스켈레톤) / Streaming(LIVE 맥동) / 부분 실패(카드별) / 스트림 끊김(회색+재연결) / 서킷 브레이커(안전 종료 배너) / 쿼터 초과(경고 모달) / 도메인 미검증(버튼 비활성).

## 접근성 (WCAG 2.2 AA)
대비 4.5:1, 키보드 내비, 실시간 값 `aria-live`, 포커스 링, 차트 대체 텍스트/데이터 테이블 토글. 차트는 `overflow-x` 컨테이너로 오버플로.

## 경계면 규칙 (중요)
API shape이 불명확하면 추측해서 바인딩하지 말고 backend-builder에 확인한다. shape 불일치는 klaro 통합 버그의 최대 원인이다. 구현 화면·바인딩 API를 `_workspace/04_frontend-builder_manifest.md`에 기록.
