/**
 * 로그↔트레이스 상관키 부착 (SDK_CONTRACT.md §12).
 *
 * klaro-apm은 트레이스만 보내는 thin wrapper다. 고객 애플리케이션의 로그는 이 SDK를 거치지
 * 않고 자체 로거로 나가므로, 그 줄에 trace_id가 실리지 않으면 관측 플랫폼의 연계분석
 * (GET /orgs/:orgId/obs/traces/:traceId/correlated)이 스팬을 로그에 연결할 방법이 없다.
 * 트레이스도 있고 로그도 있는데 둘을 잇는 키만 없는 상태가 되어, 화면에는 "이 트레이스에
 * 해당하는 로그 없음"으로 보인다 - 실제로는 로그가 존재하는데도.
 *
 * 이 모듈은 그 한 가지 격차만 메운다: 현재 스팬 컨텍스트를 읽어 로그 한 줄에 붙일 필드로
 * 돌려준다. 로그를 전송하지도, 로거를 교체하지도 않는다.
 *
 *   import { correlationFields, withCorrelation } from '@klaro/apm';
 *
 *   logger.info(withCorrelation({ msg: 'checkout completed', orderId }));
 *   // pino/winston child logger를 쓰는 경우:
 *   const log = logger.child(correlationFields());
 *
 * Python의 logging.Filter에 대응하는 전역 훅을 두지 않는 이유: Node에는 표준 로깅 파이프라인이
 * 없어서 훅을 걸 지점이 로거마다 다르다(pino의 mixin, winston의 format, console 래핑). 라이브러리
 * 하나를 골라 특별대우하는 대신, 어느 로거에도 넣을 수 있는 값을 반환한다.
 */

import { trace } from '@opentelemetry/api';

/**
 * 로그 필드에 쓰는 키 이름.
 *
 * Loki의 OTLP 수신기가 로그 레코드의 트레이스 컨텍스트를 structured metadata에 넣을 때 쓰는
 * 이름과 같아야 한다 - 백엔드의 상관 조회
 * (services/observability/internal/explorer/logs.go)가 이 이름으로 필터한다.
 */
export const TRACE_ID_KEY = 'trace_id';
export const SPAN_ID_KEY = 'span_id';

/** 한 로그 줄에 붙는 상관키. 유효한 스팬이 없으면 두 키 모두 없다. */
export interface CorrelationFields {
  trace_id?: string;
  span_id?: string;
}

/**
 * 현재 스팬의 상관키를 반환한다. 유효한 스팬이 없으면 빈 객체.
 *
 * 0으로 채워진 id를 반환하지 않는 이유: OTel의 INVALID_SPAN_CONTEXT는 trace_id가 전부 0인
 * "정상 값"처럼 보이므로, 그대로 찍히면 존재하지 않는 트레이스를 조회하게 만든다.
 */
export function correlationFields(): CorrelationFields {
  const ctx = trace.getActiveSpan()?.spanContext();
  if (!ctx || !ctx.traceId || /^0+$/.test(ctx.traceId)) return {};
  return { [TRACE_ID_KEY]: ctx.traceId, [SPAN_ID_KEY]: ctx.spanId };
}

/**
 * 로그 페이로드에 상관키를 덧붙인 새 객체를 반환한다.
 *
 * 인자를 제자리에서 바꾸지 않는 이유: 호출측이 재사용하는 상수 객체를 넘겼을 때 첫 요청의
 * trace_id가 그 뒤 모든 로그 줄에 남는다 - 조용히 틀린 상관은 상관이 없는 것보다 나쁘다.
 */
export function withCorrelation<T extends object>(payload: T): T & CorrelationFields {
  return { ...payload, ...correlationFields() };
}
