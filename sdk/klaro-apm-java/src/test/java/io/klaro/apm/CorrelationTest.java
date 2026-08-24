package io.klaro.apm;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.Tracer;
import io.opentelemetry.context.Scope;
import io.opentelemetry.sdk.trace.SdkTracerProvider;
import java.util.Map;
import org.junit.jupiter.api.Test;

/**
 * 상관키 부착(SDK_CONTRACT §12) 테스트.
 *
 * <p>검증 대상은 "값이 있으면 붙는다"가 아니라 그 반대쪽이다: 스팬이 없을 때 0으로 채워진 id를
 * 만들어내지 않는지. 상관 조회는 이 키로 Loki를 필터하므로 잘못된 키는 조용히 빈 결과가 된다.
 */
class CorrelationTest {

  private static final Tracer TRACER = SdkTracerProvider.builder().build().get("test");

  @Test
  void fieldsAreEmptyWithoutASpan() {
    assertTrue(Correlation.fields().isEmpty());
    assertEquals("", Correlation.traceId());
    assertEquals("", Correlation.spanId());
  }

  @Test
  void fieldsCarryTheActiveSpanAsHex() {
    Span span = TRACER.spanBuilder("checkout").startSpan();
    Map<String, String> fields;
    try (Scope ignored = span.makeCurrent()) {
      fields = Correlation.fields();
      assertEquals(span.getSpanContext().getTraceId(), Correlation.traceId());
      assertEquals(span.getSpanContext().getSpanId(), Correlation.spanId());
    } finally {
      span.end();
    }

    assertTrue(fields.get(Correlation.TRACE_ID_KEY).matches("[0-9a-f]{32}"));
    assertTrue(fields.get(Correlation.SPAN_ID_KEY).matches("[0-9a-f]{16}"));
    // 전부 0인 id는 존재하지 않는 트레이스를 조회하게 만든다.
    assertFalse(fields.get(Correlation.TRACE_ID_KEY).matches("0+"));
  }
}
