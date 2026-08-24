/**
 * 상관키 부착(SDK_CONTRACT §12) 테스트.
 *
 * 검증 대상은 "값이 있으면 붙는다"가 아니라 그 반대쪽 두 경우다: 스팬이 없을 때 0으로 채워진
 * id를 만들어내지 않는지, 그리고 호출측이 재사용하는 객체를 오염시키지 않는지. 상관 조회는 이
 * 키로 Loki를 필터하므로 잘못된 키는 조용히 빈 결과가 된다.
 */

import { describe, expect, it } from 'vitest';
import { BasicTracerProvider } from '@opentelemetry/sdk-trace-base';
import { AsyncLocalStorageContextManager } from '@opentelemetry/context-async-hooks';
import { context, trace } from '@opentelemetry/api';

import {
  SPAN_ID_KEY,
  TRACE_ID_KEY,
  correlationFields,
  withCorrelation,
} from '../src/correlation';

// correlationFields reads the *active* span, so the suite needs a real
// context manager - the same thing provider.register() installs in
// production. Without one the API default is a noop manager, context.with()
// never propagates, and these tests would pass for the wrong reason.
context.setGlobalContextManager(new AsyncLocalStorageContextManager().enable());

const tracer = new BasicTracerProvider().getTracer('test');

/** 스팬을 활성 컨텍스트로 만들고 fn을 실행한다. */
function inSpan<T>(fn: () => T): T {
  const span = tracer.startSpan('checkout');
  try {
    return context.with(trace.setSpan(context.active(), span), fn);
  } finally {
    span.end();
  }
}

describe('correlationFields', () => {
  it('스팬이 없으면 빈 객체를 반환한다', () => {
    expect(correlationFields()).toEqual({});
  });

  it('활성 스팬의 id를 백엔드가 기대하는 16진수 형식으로 반환한다', () => {
    const fields = inSpan(() => correlationFields());

    expect(fields[TRACE_ID_KEY]).toMatch(/^[0-9a-f]{32}$/);
    expect(fields[SPAN_ID_KEY]).toMatch(/^[0-9a-f]{16}$/);
    // 전부 0인 id는 존재하지 않는 트레이스를 조회하게 만든다.
    expect(fields[TRACE_ID_KEY]).not.toMatch(/^0+$/);
  });
});

describe('withCorrelation', () => {
  it('상관키를 덧붙인 새 객체를 반환하고 인자는 건드리지 않는다', () => {
    const payload = { msg: 'checkout completed' };
    const enriched = inSpan(() => withCorrelation(payload));

    expect(enriched.msg).toBe('checkout completed');
    expect(enriched[TRACE_ID_KEY]).toMatch(/^[0-9a-f]{32}$/);
    // 재사용되는 객체에 첫 요청의 trace_id가 남으면 그 뒤 모든 줄이 잘못 상관된다.
    expect(payload).toEqual({ msg: 'checkout completed' });
  });

  it('스팬이 없으면 페이로드를 그대로 통과시킨다', () => {
    expect(withCorrelation({ msg: 'startup' })).toEqual({ msg: 'startup' });
  });
});
