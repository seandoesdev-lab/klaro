---
name: klaro-build
description: klaro(배포 적합성 SaaS)의 명세→구현→검증 전체 워크플로우를 조율하는 오케스트레이터. klaro 기능/서비스를 구현·개발·빌드하거나, "klaro 만들어줘", "이 서비스 구현", "부하/스캔/APM/리포트/과금/대시보드 개발", 그리고 후속 요청("다시 실행", "재실행", "업데이트", "수정", "보완", "이전 결과 기반으로", "~부분만 다시") 시 반드시 이 스킬을 사용할 것. 단순 질문은 직접 응답 가능.
---

# klaro-build — 오케스트레이터

klaro 설계 문서를 실제 구현으로 옮기는 팀을 조율한다. 실행 모드는 **하이브리드**: 분석·설계는 파이프라인, 구현은 에이전트 팀, 검증은 생성-검증.

에이전트/스킬 목록은 `.claude/agents/`, `.claude/skills/`에서 관리한다. 모든 Agent 호출은 `model: "opus"`.

## Phase 0: 컨텍스트 확인 (초기/후속/부분 판별)
1. `_workspace/` 존재 여부 확인.
   - 미존재 → **초기 실행**.
   - 존재 + 사용자가 부분 수정 요청("~부분만") → **부분 재실행**(해당 에이전트만 재호출).
   - 존재 + 새 입력/범위 → **새 실행**(`_workspace/`를 `_workspace_prev/`로 이동 후 시작).
2. 실행 모드를 사용자에게 한 줄로 알리고 진행.

## Phase 1: 분석·설계 (파이프라인, 서브 에이전트)
**실행 모드:** 파이프라인 (순차 의존). 팀 통신 불필요 → 서브 에이전트.
1. `Agent(spec-analyst, model:"opus")` → `_workspace/01_spec-analyst_contract.md`.
2. 계약의 "결정 필요 목록"에 사용자 확인이 필요한 스택 결정이 있으면 **여기서 사용자에게 확인**.
3. `Agent(architect, model:"opus")` → `_workspace/02_architect_design.md`.
4. 산출물을 검토하고 빌드 순서를 확정.

## Phase 2: 구현 (에이전트 팀)
**실행 모드:** 에이전트 팀 (실시간 경계면 합의 필요).
1. `TeamCreate`로 팀 구성: `backend-builder`, `frontend-builder`, `qa-verifier`.
2. `TaskCreate`로 빌드 순서 기반 작업 할당(의존 관계 포함). 팀 크기 3명 → 팀원당 4~6개 작업.
3. 팀원 자체 조율:
   - backend↔frontend: `SendMessage`로 **API shape 합의**(경계면 버그 예방).
   - 각 빌더는 모듈 완료 시 `TaskUpdate` → `qa-verifier`가 **증분 QA** 즉시 수행.
4. 리더(오케스트레이터)는 진행 모니터링, 블로커 중재.
5. QA 결함은 `_workspace/05_qa-verifier_report.md` + 해당 빌더에게 수정 작업 등록.
6. Phase 종료 시 팀 정리(다음 Phase는 다른 조합).

## Phase 3: 최종 검토 (생성-검증, 서브 에이전트)
**실행 모드:** 서브 에이전트 (독립 검증).
1. `Agent(invariants-reviewer, model:"opus")` → `_workspace/06_invariants-reviewer_verdict.md`.
2. 종합 판정이 **차단**이면 위반을 빌더에게 넘겨 Phase 2 부분 재실행. **조건부/배포 가능**이면 종료.

## 데이터 전달 프로토콜
- **파일 기반**(주 산출물): `_workspace/{phase}_{agent}_{artifact}.{ext}`. 중간 파일 보존(감사 추적).
- **태스크 기반**(팀 조율): `TaskCreate`/`TaskUpdate`로 의존·진행 관리.
- **메시지 기반**(실시간): `SendMessage`로 경계면 합의·결함 통지.
- 최종 산출물(코드)만 프로젝트 트리에 출력. `_workspace/`는 유지.

## 에러 핸들링
- 에이전트 1회 재시도 후 재실패 → 해당 결과 없이 진행하되 최종 보고에 **누락 명시**.
- 상충 데이터(문서 vs 코드 등)는 삭제하지 않고 출처 병기.
- Phase 경계에서 선행 산출물이 없으면 그 Phase를 먼저 실행(dead link 방지).

## 테스트 시나리오
- **정상 흐름**: "부하 테스트 서비스(S1) 구현" → Phase0(초기) → spec-analyst 계약 → architect 설계(언어 확인) → 팀(backend가 API/워커, frontend가 생성/대시보드 화면, qa가 per_api·서킷브레이커 증분 검증) → invariants-reviewer 판정(배포 가능) → 완료.
- **에러 흐름**: invariants-reviewer가 "미검증 도메인 부하 허용" critical 발견 → 판정 차단 → backend-builder 부분 재실행으로 도메인 게이트 추가 → 재검토 통과.

## 완료 후
사용자에게 피드백 기회를 제공한다("워크플로우/팀 구성에 바꿀 점이 있나요?"). 변경은 CLAUDE.md 하네스 변경 이력에 기록한다(하네스는 진화하는 시스템).
