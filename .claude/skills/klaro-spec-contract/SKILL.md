---
name: klaro-spec-contract
description: klaro 설계 문서(docs/klaro/00~05 + prototype.html)를 읽어 구현 요구사항 계약으로 변환한다. klaro 기능/서비스 구현·재구현·보완을 시작하거나, "무엇을 만들지 정리", "요구사항 뽑아줘", "명세 계약", "스펙 분석"을 요청하면 반드시 이 스킬을 사용할 것. spec-analyst 에이전트 전용.
---

# klaro-spec-contract — 명세 → 요구사항 계약

klaro는 코드가 아직 없는 설계 단계 프로젝트다. 문서가 단일 진실 공급원이므로, 구현 착수 전 문서를 실행 가능한 계약으로 압축한다.

## 절차

1. **범위 확정** — 사용자 요청이 어느 서비스/기능에 해당하는지 식별(Control Plane / S1 부하 / S2 스캔 / S3 APM / S4 리포트 / Billing / 프론트).

2. **근거 문서 읽기** — 해당 범위에 맞는 문서만 읽어 컨텍스트를 아낀다:
   - 데이터: `02-data-model.md` · API: `03-api-spec.md` · 아키텍처/불변식: `01-technical-design.md` · UI: `05-ui-ux-design.md` · 스택/비용: `00`, `04`.

3. **요구사항 계약 작성** — 항목마다:
   - **설명**(WHAT, HOW 금지) · **관련 ID**(`[SC-01]` 등) · **수용 기준**(관찰 가능한 조건) · **데이터 모델 참조**(테이블/컬럼) · **API 참조**(경로/메서드).

4. **불변식 체크리스트** — 이 범위가 건드리는 klaro 불변식을 MUST로 명시:
   RLS(`org_id`+`app.current_org`) · 도메인 검증 게이트([SC-01]) · 잡 상태머신 · 서킷 브레이커 · Ephemeral 소스 · mTLS · 워커 idle=0 · 시계열 RDB 분리 · 비용 게이트([COST-05]).

5. **결정 필요 목록** — 스택 미확정 항목(언어/오케스트레이션/큐/Bedrock/격리)이 이 범위에 걸리면 `architect`에게 넘길 질문으로 분리.

6. **모호/모순** — 문서 근거가 없거나 문서끼리 충돌하는 지점을 추측 없이 기록.

## 산출물
`_workspace/01_spec-analyst_contract.md`에 위 6개 섹션으로 저장.

## 왜 이렇게 하나
계약이 ID 단위로 추적 가능해야 이후 QA·리뷰가 "이 요구가 충족됐는가"를 객관적으로 확인할 수 있다. HOW를 섞으면 architect의 설계 자유도를 뺏고 계약이 조기에 낡는다.

## 예시 계약 항목
```
### 부하 테스트 생성 (다중 API)
- 설명: 검증된 사이트 1개에 등록된 여러 API를 가중치 혼합으로 동시 부하.
- 관련 ID: [LG-04], [CAT-01], [SC-01]
- 수용 기준: 미검증 도메인이면 403 DOMAIN_NOT_VERIFIED / 선택 endpoint가 domain 소속 아니면 422 / 결과에 per_api[] 포함.
- 데이터: load_tests(domain_id, scenario.apis[]), endpoints, load_test_results(endpoint_id)
- API: POST /projects/:id/load-tests, GET /load-tests/:id/results
```
