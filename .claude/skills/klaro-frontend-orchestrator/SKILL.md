---
name: klaro-frontend-orchestrator
description: klaro 프런트엔드 대시보드 작업을 조율하는 오케스트레이터. klaro 대시보드/화면/UI를 만들거나 수정·재실행·업데이트·보완할 때, 실시간 화면이나 리포트 화면을 구현할 때 사용. "대시보드 만들어", "화면 만들어", "UI 수정", "다시 실행", "디자인 보완" 등의 요청을 처리.
---

# klaro 프런트엔드 오케스트레이터

klaro 프런트엔드 화면 작업을 **생성-검증(하이브리드)** 모드로 조율한다: 디자이너가 생성 → QA가 경계면 검증 → 리더(오케스트레이터)가 브라우저 e2e로 최종 확인.

## 실행 모드
서브 에이전트(반환값 + 파일 기반). 팀 통신 오버헤드가 불필요한 규모라 서브로 구성한다. 모든 Agent 호출은 `model: "opus"`.

## Phase 0: 컨텍스트 확인
- `services/load-test/web/index.html` 존재 여부 확인.
  - 없음 → **초기 실행**(전체 생성).
  - 있음 + 부분 수정 요청 → **부분 재실행**(디자이너에 해당 부분만 지시).
  - 있음 + 새 요구 → 기존 파일을 참고로 재생성.

## Phase 1: 생성 (klaro-frontend-designer)
`Agent(subagent_type 우선 "klaro-frontend-designer", 미등록 시 general-purpose, model:"opus")`로 디자이너를 호출한다. 프롬프트에 반드시 포함:
- `klaro-dashboard-design` 스킬을 읽고 따를 것.
- 산출물: `services/load-test/web/index.html` + `internal/api/router.go`(`//go:embed`) 수정.
- `golang:1.25` Docker로 `go build ./...` 통과까지 자체 검증.
- API 실제 계약은 `internal/api/*.go`·`internal/worker/worker.go`를 읽어 확정.

## Phase 2: 검증 (klaro-frontend-qa)
`Agent(subagent_type 우선 "klaro-frontend-qa", 미등록 시 general-purpose, model:"opus")`로 경계면 정합성을 교차 검증한다. 불일치가 나오면 Phase 1 디자이너에 수정 지시(1회 재시도).

## Phase 3: 최종 e2e (리더)
`docker compose up --build -d` 후 브라우저/HTTP로 대시보드 로드·테스트 생성·실시간·결과를 직접 확인한다. 로컬 Go 미설치이므로 빌드/실행은 Docker로 한다.

## 데이터 전달
- 반환값 기반(에이전트 결과 수집) + 파일 기반(`web/index.html`, `router.go`가 산출물 겸 전달 매개).

## 에러 핸들링
- 빌드/검증 실패 시 1회 재시도 후에도 실패하면 원인을 사용자에게 보고하고 부분 산출물을 보존한다.
- API 계약 불일치는 삭제하지 말고 QA 보고에 출처(파일:라인) 병기.

## 테스트 시나리오
- **정상 흐름**: 초기 실행 → 디자이너 생성(빌드 통과) → QA 정합성 OK → compose e2e에서 대시보드 로드·실시간 그래프·결과 표시 확인.
- **에러 흐름**: QA가 WS 스키마 불일치(예: UI가 `p95` vs 실제 `latency_p95_ms`) 발견 → 디자이너 수정 → 재검증 통과.
