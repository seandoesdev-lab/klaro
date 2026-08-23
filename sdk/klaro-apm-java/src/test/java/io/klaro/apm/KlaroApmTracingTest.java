package io.klaro.apm;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.trace.Tracer;
import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import io.opentelemetry.sdk.trace.SdkTracerProvider;
import io.opentelemetry.sdk.trace.data.SpanData;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.TimeUnit;
import org.junit.jupiter.api.Test;

class KlaroApmTracingTest {

  @Test
  void buildExporterDoesNotThrow() {
    KlaroConfig config =
        KlaroConfig.resolve(KlaroConfig.builder().obsKey("secret-123").endpoint("localhost:4317"), Map.of());

    assertDoesNotThrow(() -> KlaroApm.buildExporter(config));
  }

  @Test
  void buildTracerProviderSetsServiceNameAndHostIdentOnEmittedSpans() {
    KlaroConfig config =
        KlaroConfig.resolve(
            KlaroConfig.builder().serviceName("checkout-api").hostIdent("host:web-1:4242"), Map.of());
    InMemorySpanExporter exporter = InMemorySpanExporter.create();

    KlaroApm.TracerProviderAndProcessor built = KlaroApm.buildTracerProvider(config, exporter);
    SdkTracerProvider provider = built.tracerProvider;
    Tracer tracer = provider.get("test");

    tracer.spanBuilder("checkout").startSpan().end();
    built.processor.forceFlush().join(10, TimeUnit.SECONDS);

    List<SpanData> finished = exporter.getFinishedSpanItems();
    assertEquals(1, finished.size());
    assertEquals("checkout-api", finished.get(0).getResource().getAttributes().get(AttributeKey.stringKey("service.name")));
    assertEquals("host:web-1:4242", finished.get(0).getResource().getAttributes().get(AttributeKey.stringKey("service.instance.id")));

    built.processor.shutdown().join(10, TimeUnit.SECONDS);
  }

  @Test
  void exportsSpansWithoutBlockingTheCaller() {
    KlaroConfig config = KlaroConfig.resolve(KlaroConfig.builder().serviceName("checkout-api"), Map.of());
    InMemorySpanExporter exporter = InMemorySpanExporter.create();

    KlaroApm.TracerProviderAndProcessor built = KlaroApm.buildTracerProvider(config, exporter);
    Tracer tracer = built.tracerProvider.get("test");

    tracer.spanBuilder("checkout").startSpan().end();

    boolean flushed = built.processor.forceFlush().join(10, TimeUnit.SECONDS).isSuccess();
    assertTrue(flushed);

    List<SpanData> finished = exporter.getFinishedSpanItems();
    assertEquals(1, finished.size());
    assertEquals("checkout", finished.get(0).getName());

    built.processor.shutdown().join(10, TimeUnit.SECONDS);
  }

  @Test
  void dropsOldestSpansWhenTheQueueIsSaturated() {
    // maxExportBatchSize를 총 span 수보다 훨씬 크게 잡아, 백그라운드 워커 스레드가 테스트의 동기
    // for 루프가 끝나기 전에 조기 flush를 시작하는 경쟁 상태를 원천 차단한다(Java는 실제 스레드라
    // Node/Python과 달리 단일 스레드 순서 보장이 없다). scheduleDelayMillis도 크게 잡아 타이머
    // 기반 flush가 테스트 도중 끼어들지 않게 한다 - drop-oldest 자체는 maxQueueSize=5 로 검증.
    KlaroConfig config =
        KlaroConfig.resolve(
            KlaroConfig.builder()
                .serviceName("checkout-api")
                .maxQueueSize(5)
                .maxExportBatchSize(1000)
                .scheduleDelayMillis(60_000),
            Map.of());
    InMemorySpanExporter exporter = InMemorySpanExporter.create();

    KlaroApm.TracerProviderAndProcessor built = KlaroApm.buildTracerProvider(config, exporter);
    Tracer tracer = built.tracerProvider.get("test");

    for (int i = 0; i < 10; i++) {
      tracer.spanBuilder("span-" + i).startSpan().end();
    }

    built.processor.forceFlush().join(10, TimeUnit.SECONDS);

    Set<String> finishedNames = new LinkedHashSet<>();
    for (SpanData span : exporter.getFinishedSpanItems()) {
      finishedNames.add(span.getName());
    }

    Set<String> expected = new LinkedHashSet<>();
    for (int i = 5; i < 10; i++) {
      expected.add("span-" + i);
    }

    assertEquals(5, finishedNames.size());
    assertEquals(expected, finishedNames);

    built.processor.shutdown().join(10, TimeUnit.SECONDS);
  }
}
