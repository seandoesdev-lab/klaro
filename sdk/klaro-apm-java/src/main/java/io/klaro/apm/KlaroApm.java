package io.klaro.apm;

import io.opentelemetry.api.OpenTelemetry;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.common.AttributesBuilder;
import io.opentelemetry.exporter.otlp.trace.OtlpGrpcSpanExporter;
import io.opentelemetry.exporter.otlp.trace.OtlpGrpcSpanExporterBuilder;
import io.opentelemetry.sdk.OpenTelemetrySdk;
import io.opentelemetry.sdk.resources.Resource;
import io.opentelemetry.sdk.trace.SdkTracerProvider;
import io.opentelemetry.sdk.trace.export.SpanExporter;
import io.opentelemetry.sdk.trace.samplers.Sampler;
import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.logging.Level;
import java.util.logging.Logger;
import javax.annotation.Nullable;

/**
 * klaro-apm 초기화 핵심.
 *
 * <p>thin wrapper 원칙(HOW-11): OTel 공식 SDK가 이미 제공하는 동작을 재구현하지 않는다.
 *
 * <ul>
 *   <li>비블로킹 배치 익스포트/큐 포화 시 drop-oldest: {@link DropOldestBatchSpanProcessor} 참조 -
 *       OTel Java 표준 {@code BatchSpanProcessor}는 drop-newest라서 최소 구현을 추가했다
 *       (SDK_CONTRACT.md 5).
 *   <li>연결 실패 시 조용한 재시도: {@code OtlpGrpcSpanExporter.export()}는 실패를
 *       {@code CompletableResultCode.ofFailure()}로 알릴 뿐 예외를 던지지 않는다. 실패한 배치는
 *       버려지고, 다음 배치 주기에 새로 쌓인 span으로 자연스럽게 재시도가 이어진다.
 *   <li><b>전역 등록(Java 특이사항)</b>: Python/Node의 {@code trace.set_tracer_provider}는 여러 번
 *       호출해도 마지막 값으로 조용히 덮어써지지만, Java의 {@code GlobalOpenTelemetry.set()}은 JVM당
 *       단 한 번만 허용되고 재호출 시 예외를 던진다. 이 SDK는 자체 상태 캐시로 idempotent한
 *       {@link #init}만 제공하고 전역 싱글턴 등록은 강제하지 않는다 - 필요하면 호출측이
 *       {@code GlobalOpenTelemetry.set(KlaroApm.init(...))}를 직접(한 번만) 호출한다. 자세한 내용은
 *       sdk/SDK_CONTRACT.md 11.
 * </ul>
 */
public final class KlaroApm {

  private static final Logger logger = Logger.getLogger(KlaroApm.class.getName());

  private static final Object LOCK = new Object();

  @Nullable private static OpenTelemetrySdk sdk;
  @Nullable private static DropOldestBatchSpanProcessor processor;

  private KlaroApm() {}

  /** 설정으로부터 OTel Resource(서비스명 + 정규화된 host_ident)를 만든다. */
  public static Resource buildResource(KlaroConfig config) {
    String hostIdent = HostIdent.resolve(config.getHostIdentOverride());
    AttributesBuilder attributes =
        Attributes.builder()
            .put(AttributeKey.stringKey("service.name"), config.getServiceName())
            .put(AttributeKey.stringKey("service.instance.id"), hostIdent);
    config.getExtraResourceAttributes().forEach((k, v) -> attributes.put(AttributeKey.stringKey(k), v));
    return Resource.create(attributes.build());
  }

  @Nullable
  private static byte[] readFileOrNull(@Nullable String path) {
    if (path == null) {
      return null;
    }
    try {
      return Files.readAllBytes(Path.of(path));
    } catch (IOException e) {
      throw new UncheckedIOException("klaro-apm: failed to read file " + path, e);
    }
  }

  /**
   * OTLP 엔드포인트 문자열에 스킴을 부여한다(Java 특이사항). SDK_CONTRACT.md는 언어 공통으로
   * {@code host:port} 형태(예: {@code localhost:4317})를 쓰지만, OTel Java의
   * {@code OtlpGrpcSpanExporterBuilder.setEndpoint()}는 {@code http://} 또는 {@code https://}
   * 스킴이 붙은 URL을 요구한다. 스킴이 이미 있으면 그대로 사용하고, 없으면 mTLS/insecure 여부에
   * 따라 붙인다.
   */
  public static String normalizeEndpoint(String endpoint, boolean useTls) {
    if (endpoint.startsWith("http://") || endpoint.startsWith("https://")) {
      return endpoint;
    }
    return (useTls ? "https://" : "http://") + endpoint;
  }

  /**
   * {@code klaro-obs-key} 헤더가 첨부된 OTLP/gRPC exporter를 만든다.
   *
   * <p>mTLS는 옵션으로만 노출한다(로컬은 평문 허용, Collector가 배포에서 mTLS를 강제한다 -
   * HOW-11/CLAUDE.md 통신 규약).
   */
  public static SpanExporter buildExporter(KlaroConfig config) {
    boolean mtls = config.getClientCertificateFile() != null && config.getClientKeyFile() != null;
    boolean useTls = mtls || !config.isInsecure();

    OtlpGrpcSpanExporterBuilder builder =
        OtlpGrpcSpanExporter.builder().setEndpoint(normalizeEndpoint(config.getEndpoint(), useTls));

    for (Map.Entry<String, String> header : config.otlpHeaders().entrySet()) {
      builder.addHeader(header.getKey(), header.getValue());
    }

    if (mtls) {
      byte[] rootCert = readFileOrNull(config.getRootCertificateFile());
      if (rootCert != null) {
        builder.setTrustedCertificates(rootCert);
      }
      builder.setClientTls(
          readFileOrNull(config.getClientKeyFile()), readFileOrNull(config.getClientCertificateFile()));
    }

    return builder.build();
  }

  /** {@link #buildTracerProvider} 반환값 - TracerProvider와, 테스트에서 직접 flush/shutdown할 processor. */
  public static final class TracerProviderAndProcessor {
    public final SdkTracerProvider tracerProvider;
    public final DropOldestBatchSpanProcessor processor;

    TracerProviderAndProcessor(SdkTracerProvider tracerProvider, DropOldestBatchSpanProcessor processor) {
      this.tracerProvider = tracerProvider;
      this.processor = processor;
    }
  }

  /** SdkTracerProvider + DropOldestBatchSpanProcessor를 구성한다. 테스트에서 in-memory exporter 주입용으로 분리. */
  public static TracerProviderAndProcessor buildTracerProvider(KlaroConfig config, @Nullable SpanExporter exporter) {
    SpanExporter spanExporter = exporter != null ? exporter : buildExporter(config);

    DropOldestBatchSpanProcessor processor =
        DropOldestBatchSpanProcessor.builder(spanExporter)
            .setMaxQueueSize(config.getMaxQueueSize())
            .setScheduleDelayMillis(config.getScheduleDelayMillis())
            .setMaxExportBatchSize(config.getMaxExportBatchSize())
            .build();

    // tail-based 샘플링은 Collector(tailsampling processor)가 전체 트레이스를 보고 결정한다.
    // SDK는 head 샘플링으로 데이터를 먼저 버리지 않고 항상 전송한다(ParentBased(AlwaysOn)).
    SdkTracerProvider tracerProvider =
        SdkTracerProvider.builder()
            .setResource(buildResource(config))
            .setSampler(Sampler.parentBased(Sampler.alwaysOn()))
            .addSpanProcessor(processor)
            .build();

    return new TracerProviderAndProcessor(tracerProvider, processor);
  }

  /** 환경변수만으로 klaro-apm을 초기화한다(idempotent). */
  public static OpenTelemetry init() {
    return init(KlaroConfig.builder(), null);
  }

  /** 빌더 옵션 + 환경변수로 klaro-apm을 초기화한다(옵션이 우선, idempotent). */
  public static OpenTelemetry init(KlaroConfig.Builder overrides) {
    return init(overrides, null);
  }

  /**
   * klaro-apm을 초기화하고 {@link OpenTelemetrySdk} 인스턴스를 반환한다(idempotent).
   *
   * <p>설정 우선순위: {@code overrides} &gt; 환경변수({@code KLARO_OBS_*}) &gt; 기본값. 이미
   * 초기화된 경우 기존 인스턴스를 그대로 반환한다(중복 초기화로 인한 프로세서 중복 방지).
   *
   * @param exporter 테스트/커스텀 파이프라인용 exporter 주입. null이면 OTLP/gRPC exporter를 만든다.
   */
  public static OpenTelemetry init(KlaroConfig.Builder overrides, @Nullable SpanExporter exporter) {
    synchronized (LOCK) {
      if (sdk != null) {
        return sdk;
      }

      KlaroConfig config = KlaroConfig.resolve(overrides, System.getenv());
      TracerProviderAndProcessor built = buildTracerProvider(config, exporter);

      OpenTelemetrySdk openTelemetrySdk = OpenTelemetrySdk.builder().setTracerProvider(built.tracerProvider).build();

      sdk = openTelemetrySdk;
      processor = built.processor;
      Runtime.getRuntime().addShutdownHook(new Thread(KlaroApm::shutdown, "klaro-apm-shutdown-hook"));
      return openTelemetrySdk;
    }
  }

  /** 배치 프로세서를 flush하고 종료한다. 실패해도 예외를 호출자에게 전파하지 않는다. */
  public static void shutdown() {
    synchronized (LOCK) {
      if (processor != null) {
        try {
          processor.shutdown().join(10, TimeUnit.SECONDS);
        } catch (Throwable t) {
          // 고객 앱에 예외 전파 금지(APM-01).
          logger.log(Level.FINE, "klaro-apm: shutdown 중 예외 억제", t);
        }
      }
      sdk = null;
      processor = null;
    }
  }
}
