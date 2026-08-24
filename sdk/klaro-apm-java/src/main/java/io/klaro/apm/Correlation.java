package io.klaro.apm;

import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanContext;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * 로그↔트레이스 상관키 부착(SDK_CONTRACT.md §12).
 *
 * <p>klaro-apm은 트레이스만 보내는 thin wrapper다. 고객 애플리케이션의 로그는 이 SDK를 거치지
 * 않고 자체 로거(logback/log4j2)로 나가므로, 그 줄에 {@code trace_id}가 실리지 않으면 관측
 * 플랫폼의 연계분석({@code GET /orgs/:orgId/obs/traces/:traceId/correlated})이 스팬을 로그에
 * 연결할 방법이 없다. 트레이스도 있고 로그도 있는데 둘을 잇는 키만 없는 상태가 되어, 화면에는
 * "이 트레이스에 해당하는 로그 없음"으로 보인다 &mdash; 실제로는 로그가 존재하는데도.
 *
 * <p>이 클래스는 그 한 가지 격차만 메운다: 현재 스팬 컨텍스트를 로그 한 줄에 붙일 값으로
 * 돌려준다. 로그를 전송하지도, 로거를 교체하지도 않는다.
 *
 * <pre>{@code
 * // MDC 패턴 (logback/log4j2 공통)
 * try (MDC.MDCCloseable ignored = MDC.putCloseable("trace_id", Correlation.traceId())) {
 *   log.info("checkout completed");
 * }
 * // 또는 구조화 로깅에 한 번에
 * Correlation.fields().forEach(MDC::put);
 * }</pre>
 *
 * <p>MDC에 직접 쓰지 않는 이유: MDC는 스레드 로컬이라 이 SDK가 넣으면 지우는 책임까지 가져가야
 * 하고, 스레드 풀에서 지우지 못한 값은 다음 요청의 로그에 남는다. 조용히 틀린 상관은 상관이
 * 없는 것보다 나쁘므로, 수명은 호출측(try-with-resources)에 맡긴다.
 *
 * <p>OTel 공식 {@code opentelemetry-logback-mdc} appender를 쓰는 애플리케이션은 이 클래스가
 * 필요 없다 &mdash; 그 appender가 같은 키를 자동으로 넣는다. 이것은 그 의존성을 강제하지 않는
 * 최소 경로다.
 */
public final class Correlation {

  /**
   * 로그 필드/MDC에 쓰는 키 이름.
   *
   * <p>Loki의 OTLP 수신기가 로그 레코드의 트레이스 컨텍스트를 structured metadata에 넣을 때 쓰는
   * 이름과 같아야 한다 &mdash; 백엔드의 상관 조회({@code internal/explorer/logs.go})가 이 이름으로
   * 필터한다.
   */
  public static final String TRACE_ID_KEY = "trace_id";

  /** 스팬 단위 상관키. */
  public static final String SPAN_ID_KEY = "span_id";

  private Correlation() {}

  /**
   * 현재 스팬의 상관키. 유효한 스팬이 없으면 빈 Map.
   *
   * <p>0으로 채워진 id를 반환하지 않는 이유: OTel의 invalid SpanContext는 trace_id가 전부 0인
   * "정상 값"처럼 보이므로, 그대로 찍히면 존재하지 않는 트레이스를 조회하게 만든다.
   */
  public static Map<String, String> fields() {
    SpanContext ctx = Span.current().getSpanContext();
    if (!ctx.isValid()) {
      return Collections.emptyMap();
    }
    Map<String, String> out = new LinkedHashMap<>(2);
    out.put(TRACE_ID_KEY, ctx.getTraceId());
    out.put(SPAN_ID_KEY, ctx.getSpanId());
    return Collections.unmodifiableMap(out);
  }

  /** 현재 trace id, 스팬이 없으면 빈 문자열. 포맷 패턴에 바로 넣을 수 있는 형태. */
  public static String traceId() {
    SpanContext ctx = Span.current().getSpanContext();
    return ctx.isValid() ? ctx.getTraceId() : "";
  }

  /** 현재 span id, 스팬이 없으면 빈 문자열. */
  public static String spanId() {
    SpanContext ctx = Span.current().getSpanContext();
    return ctx.isValid() ? ctx.getSpanId() : "";
  }
}
