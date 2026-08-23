package io.klaro.apm;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertSame;

import io.opentelemetry.api.OpenTelemetry;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.sdk.OpenTelemetrySdk;
import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import java.util.concurrent.TimeUnit;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

class KlaroApmInitTest {

  @AfterEach
  void tearDown() {
    KlaroApm.shutdown();
  }

  private String serviceNameOf(OpenTelemetry sdk, InMemorySpanExporter exporter) {
    sdk.getTracer("test").spanBuilder("probe").startSpan().end();
    ((OpenTelemetrySdk) sdk).getSdkTracerProvider().forceFlush().join(10, TimeUnit.SECONDS);
    return exporter
        .getFinishedSpanItems()
        .get(0)
        .getResource()
        .getAttributes()
        .get(AttributeKey.stringKey("service.name"));
  }

  @Test
  void initIsIdempotentAndReturnsTheSameSdk() {
    InMemorySpanExporter exporter = InMemorySpanExporter.create();

    OpenTelemetry sdk1 = KlaroApm.init(KlaroConfig.builder().serviceName("svc-a"), exporter);
    OpenTelemetry sdk2 = KlaroApm.init(KlaroConfig.builder().serviceName("svc-b"), exporter);

    assertSame(sdk1, sdk2);
    // 두 번째 init 호출은 무시되므로 첫 설정(svc-a)이 유지된다.
    assertEquals("svc-a", serviceNameOf(sdk1, exporter));
  }

  @Test
  void shutdownClearsStateAllowingReinit() {
    KlaroApm.init(KlaroConfig.builder().serviceName("svc-a"), InMemorySpanExporter.create());

    KlaroApm.shutdown();

    InMemorySpanExporter exporter2 = InMemorySpanExporter.create();
    OpenTelemetry sdk2 = KlaroApm.init(KlaroConfig.builder().serviceName("svc-b"), exporter2);

    assertEquals("svc-b", serviceNameOf(sdk2, exporter2));
  }

  @Test
  void shutdownWithoutInitIsANoop() {
    KlaroApm.shutdown();
    KlaroApm.shutdown();
  }
}
