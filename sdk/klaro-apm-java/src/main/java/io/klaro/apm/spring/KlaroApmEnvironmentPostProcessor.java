package io.klaro.apm.spring;

import io.klaro.apm.HostIdent;
import io.klaro.apm.KlaroApm;
import io.klaro.apm.KlaroConfig;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.stream.Collectors;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.env.EnvironmentPostProcessor;
import org.springframework.core.env.ConfigurableEnvironment;
import org.springframework.core.env.MapPropertySource;

/**
 * klaro-apm 환경변수({@code KLARO_OBS_*})를 공식 OTel Spring Boot starter
 * ({@code io.opentelemetry.instrumentation:opentelemetry-spring-boot-starter})가 읽는
 * {@code otel.*} Spring 프로퍼티로 변환해 {@link ConfigurableEnvironment}에 주입하는
 * {@link EnvironmentPostProcessor}다(SDK_CONTRACT.md 7의 "Spring Boot 연동 헬퍼" 요구를
 * "starter" 형태로 충족 - 애플리케이션 코드 변경 없이, klaro-apm + 공식 OTel Spring Boot
 * starter 두 의존성만 추가하면 자동계측이 활성화된다).
 *
 * <p>등록: {@code META-INF/spring.factories}의
 * {@code org.springframework.boot.env.EnvironmentPostProcessor=io.klaro.apm.spring.KlaroApmEnvironmentPostProcessor}.
 * Spring Boot는 이 SPI를 컨텍스트 리프레시보다 훨씬 이전에 실행하므로, 여기서 설정한 프로퍼티를
 * 공식 starter의 자동설정이 그대로 읽는다.
 *
 * <p>낮은 우선순위({@link ConfigurableEnvironment#getPropertySources()}의 {@code addLast})로
 * 추가되므로, 사용자가 이미 {@code application.properties}/{@code OTEL_*} 환경변수로 같은 키를
 * 지정했다면 그 값이 항상 우선한다 - 이 프로세서는 값을 덮어쓰지 않고 기본값만 제공한다.
 */
public final class KlaroApmEnvironmentPostProcessor implements EnvironmentPostProcessor {

  static final String PROPERTY_SOURCE_NAME = "klaroApmDerivedOtelProperties";

  @Override
  public void postProcessEnvironment(ConfigurableEnvironment environment, SpringApplication application) {
    Map<String, Object> properties = derivePropertiesFrom(System.getenv());
    if (!properties.isEmpty()) {
      environment.getPropertySources().addLast(new MapPropertySource(PROPERTY_SOURCE_NAME, properties));
    }
  }

  /** env(KLARO_OBS_*)로부터 OTel Spring Boot starter용 otel.* 프로퍼티 맵을 만든다(테스트용으로 분리). */
  static Map<String, Object> derivePropertiesFrom(Map<String, String> env) {
    KlaroConfig config = KlaroConfig.resolve(KlaroConfig.builder(), env);
    Map<String, Object> properties = new LinkedHashMap<>();

    boolean mtls = config.getClientCertificateFile() != null && config.getClientKeyFile() != null;
    boolean useTls = mtls || !config.isInsecure();

    properties.put("otel.service.name", config.getServiceName());
    properties.put("otel.exporter.otlp.endpoint", KlaroApm.normalizeEndpoint(config.getEndpoint(), useTls));

    Map<String, String> headers = config.otlpHeaders();
    if (!headers.isEmpty()) {
      String headerValue =
          headers.entrySet().stream().map(e -> e.getKey() + "=" + e.getValue()).collect(Collectors.joining(","));
      properties.put("otel.exporter.otlp.headers", headerValue);
    }

    String hostIdent = HostIdent.resolve(config.getHostIdentOverride());
    properties.put("otel.resource.attributes", "service.instance.id=" + hostIdent);

    return properties;
  }
}
