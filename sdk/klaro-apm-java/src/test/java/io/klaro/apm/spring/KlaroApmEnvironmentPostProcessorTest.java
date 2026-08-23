package io.klaro.apm.spring;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.klaro.apm.KlaroConfig;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.boot.SpringApplication;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.StandardEnvironment;

class KlaroApmEnvironmentPostProcessorTest {

  @Test
  void derivePropertiesTranslatesObsKeyToOtelHeaders() {
    Map<String, Object> properties =
        KlaroApmEnvironmentPostProcessor.derivePropertiesFrom(Map.of(KlaroConfig.ENV_OBS_KEY, "secret-123"));

    assertEquals("klaro-obs-key=secret-123", properties.get("otel.exporter.otlp.headers"));
  }

  @Test
  void derivePropertiesOmitsHeadersWhenNoObsKey() {
    Map<String, Object> properties = KlaroApmEnvironmentPostProcessor.derivePropertiesFrom(Map.of());

    assertFalse(properties.containsKey("otel.exporter.otlp.headers"));
  }

  @Test
  void derivePropertiesTranslatesServiceNameAndEndpoint() {
    Map<String, Object> properties =
        KlaroApmEnvironmentPostProcessor.derivePropertiesFrom(
            Map.of(
                KlaroConfig.ENV_SERVICE_NAME, "checkout-api",
                KlaroConfig.ENV_ENDPOINT, "collector.internal:4317",
                KlaroConfig.ENV_INSECURE, "false"));

    assertEquals("checkout-api", properties.get("otel.service.name"));
    assertEquals("https://collector.internal:4317", properties.get("otel.exporter.otlp.endpoint"));
  }

  @Test
  void derivePropertiesTranslatesHostIdentOverrideToResourceAttributes() {
    Map<String, Object> properties =
        KlaroApmEnvironmentPostProcessor.derivePropertiesFrom(
            Map.of(KlaroConfig.ENV_HOST_IDENT, "host:web-1:4242"));

    assertEquals("service.instance.id=host:web-1:4242", properties.get("otel.resource.attributes"));
  }

  @Test
  void postProcessEnvironmentAddsDerivedPropertySourceAsLowestPriority() {
    StandardEnvironment environment = new StandardEnvironment();
    environment
        .getPropertySources()
        .addFirst(new MapPropertySource("test", Map.of("otel.service.name", "already-configured")));

    new KlaroApmEnvironmentPostProcessor().postProcessEnvironment(environment, new SpringApplication());

    // 이미 설정된 프로퍼티가 우선한다(derived 소스는 addLast로 낮은 우선순위에 추가됨).
    assertEquals("already-configured", environment.getProperty("otel.service.name"));
    assertTrue(
        environment
            .getPropertySources()
            .contains(KlaroApmEnvironmentPostProcessor.PROPERTY_SOURCE_NAME));
  }
}
