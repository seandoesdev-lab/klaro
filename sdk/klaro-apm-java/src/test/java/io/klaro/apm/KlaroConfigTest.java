package io.klaro.apm;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.Map;
import org.junit.jupiter.api.Test;

class KlaroConfigTest {

  @Test
  void defaultsWhenNoEnvSet() {
    KlaroConfig config = KlaroConfig.resolve(KlaroConfig.builder(), Map.of());

    assertNull(config.getObsKey());
    assertEquals(KlaroConfig.DEFAULT_ENDPOINT, config.getEndpoint());
    assertTrue(config.isInsecure());
    assertEquals("unknown-service", config.getServiceName());
    assertTrue(config.otlpHeaders().isEmpty());
  }

  @Test
  void readsKlaroObsKeyAndBuildsHeader() {
    KlaroConfig config = KlaroConfig.resolve(KlaroConfig.builder(), Map.of(KlaroConfig.ENV_OBS_KEY, "secret-123"));

    assertEquals("secret-123", config.getObsKey());
    assertEquals(Map.of(KlaroConfig.OBS_KEY_HEADER, "secret-123"), config.otlpHeaders());
  }

  @Test
  void readsEndpointAndServiceNameFromEnv() {
    KlaroConfig config =
        KlaroConfig.resolve(
            KlaroConfig.builder(),
            Map.of(
                KlaroConfig.ENV_ENDPOINT, "collector.internal:4317",
                KlaroConfig.ENV_SERVICE_NAME, "checkout-api",
                KlaroConfig.ENV_INSECURE, "false"));

    assertEquals("collector.internal:4317", config.getEndpoint());
    assertEquals("checkout-api", config.getServiceName());
    assertTrue(!config.isInsecure());
  }

  @Test
  void explicitOverridesWinOverEnv() {
    KlaroConfig config =
        KlaroConfig.resolve(
            KlaroConfig.builder().serviceName("from-builder-arg"),
            Map.of(KlaroConfig.ENV_SERVICE_NAME, "from-env"));

    assertEquals("from-builder-arg", config.getServiceName());
  }

  @Test
  void unsetBuilderFieldsFallBackToEnv() {
    KlaroConfig config = KlaroConfig.resolve(KlaroConfig.builder(), Map.of(KlaroConfig.ENV_SERVICE_NAME, "from-env"));

    assertEquals("from-env", config.getServiceName());
  }

  @Test
  void extraResourceAttributesAreCollected() {
    KlaroConfig config =
        KlaroConfig.resolve(
            KlaroConfig.builder().extraResourceAttribute("deployment.environment", "staging"), Map.of());

    assertEquals(Map.of("deployment.environment", "staging"), config.getExtraResourceAttributes());
  }
}
