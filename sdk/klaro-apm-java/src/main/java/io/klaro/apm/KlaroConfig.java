package io.klaro.apm;

import java.util.LinkedHashMap;
import java.util.Map;
import javax.annotation.Nullable;

/**
 * klaro-apm 런타임 설정.
 *
 * 설정 키(환경변수) 이름과 의미는 sdk/SDK_CONTRACT.md 가 단일 진실 공급원이며, 이 클래스는 그
 * 계약의 Java 구현체다. 다른 언어(Python/Node) SDK도 동일한 환경변수 이름을 사용한다.
 */
public final class KlaroConfig {

  /** OTLP gRPC 메타데이터 헤더 이름 - org 관측 키 인증(HOW-4). */
  public static final String OBS_KEY_HEADER = "klaro-obs-key";

  /** 로컬 개발 기본 Collector 엔드포인트(평문 gRPC). */
  public static final String DEFAULT_ENDPOINT = "localhost:4317";

  public static final String ENV_OBS_KEY = "KLARO_OBS_KEY";
  public static final String ENV_ENDPOINT = "KLARO_OBS_ENDPOINT";
  public static final String ENV_INSECURE = "KLARO_OBS_INSECURE";
  public static final String ENV_SERVICE_NAME = "KLARO_OBS_SERVICE_NAME";
  public static final String ENV_HOST_IDENT = "KLARO_OBS_HOST_IDENT";
  public static final String ENV_MAX_QUEUE_SIZE = "KLARO_OBS_MAX_QUEUE_SIZE";
  public static final String ENV_SCHEDULE_DELAY_MILLIS = "KLARO_OBS_SCHEDULE_DELAY_MILLIS";
  public static final String ENV_MAX_EXPORT_BATCH_SIZE = "KLARO_OBS_MAX_EXPORT_BATCH_SIZE";
  public static final String ENV_CLIENT_CERTIFICATE_FILE = "KLARO_OBS_CLIENT_CERTIFICATE_FILE";
  public static final String ENV_CLIENT_KEY_FILE = "KLARO_OBS_CLIENT_KEY_FILE";
  public static final String ENV_ROOT_CERTIFICATE_FILE = "KLARO_OBS_ROOT_CERTIFICATE_FILE";

  @Nullable private final String obsKey;
  private final String endpoint;
  private final boolean insecure;
  private final String serviceName;
  @Nullable private final String hostIdentOverride;
  private final int maxQueueSize;
  private final int scheduleDelayMillis;
  private final int maxExportBatchSize;
  @Nullable private final String clientCertificateFile;
  @Nullable private final String clientKeyFile;
  @Nullable private final String rootCertificateFile;
  private final Map<String, String> extraResourceAttributes;

  private KlaroConfig(Builder b) {
    this.obsKey = b.obsKey;
    this.endpoint = b.endpoint != null ? b.endpoint : DEFAULT_ENDPOINT;
    this.insecure = b.insecure != null ? b.insecure : true;
    this.serviceName = b.serviceName != null ? b.serviceName : "unknown-service";
    this.hostIdentOverride = b.hostIdentOverride;
    this.maxQueueSize = b.maxQueueSize != null ? b.maxQueueSize : 2048;
    this.scheduleDelayMillis = b.scheduleDelayMillis != null ? b.scheduleDelayMillis : 5000;
    this.maxExportBatchSize = b.maxExportBatchSize != null ? b.maxExportBatchSize : 512;
    this.clientCertificateFile = b.clientCertificateFile;
    this.clientKeyFile = b.clientKeyFile;
    this.rootCertificateFile = b.rootCertificateFile;
    this.extraResourceAttributes = Map.copyOf(b.extraResourceAttributes);
  }

  @Nullable
  public String getObsKey() {
    return obsKey;
  }

  public String getEndpoint() {
    return endpoint;
  }

  public boolean isInsecure() {
    return insecure;
  }

  public String getServiceName() {
    return serviceName;
  }

  @Nullable
  public String getHostIdentOverride() {
    return hostIdentOverride;
  }

  public int getMaxQueueSize() {
    return maxQueueSize;
  }

  public int getScheduleDelayMillis() {
    return scheduleDelayMillis;
  }

  public int getMaxExportBatchSize() {
    return maxExportBatchSize;
  }

  @Nullable
  public String getClientCertificateFile() {
    return clientCertificateFile;
  }

  @Nullable
  public String getClientKeyFile() {
    return clientKeyFile;
  }

  @Nullable
  public String getRootCertificateFile() {
    return rootCertificateFile;
  }

  public Map<String, String> getExtraResourceAttributes() {
    return extraResourceAttributes;
  }

  /** OTLP exporter에 첨부할 메타데이터 헤더. obsKey가 없으면 빈 맵(로컬 무인증 개발 허용). */
  public Map<String, String> otlpHeaders() {
    if (obsKey == null || obsKey.isEmpty()) {
      return Map.of();
    }
    return Map.of(OBS_KEY_HEADER, obsKey);
  }

  public static Builder builder() {
    return new Builder();
  }

  private static boolean boolEnv(Map<String, String> env, String name, boolean defaultValue) {
    String raw = env.get(name);
    if (raw == null) {
      return defaultValue;
    }
    String v = raw.trim().toLowerCase(java.util.Locale.ROOT);
    return v.equals("1") || v.equals("true") || v.equals("yes") || v.equals("on");
  }

  private static int intEnv(Map<String, String> env, String name, int defaultValue) {
    String raw = env.get(name);
    if (raw == null) {
      return defaultValue;
    }
    try {
      return Integer.parseInt(raw.trim());
    } catch (NumberFormatException e) {
      return defaultValue;
    }
  }

  @Nullable
  private static String emptyToNull(@Nullable String value) {
    return (value == null || value.isEmpty()) ? null : value;
  }

  /**
   * 환경변수로 기본값을 채우고, {@code overrides}에 명시적으로 설정된 필드로 덮어쓴다(override
   * 우선). {@code overrides}의 builder 메서드를 호출하지 않은 필드는 env/기본값이 유지된다.
   */
  public static KlaroConfig resolve(Builder overrides, Map<String, String> env) {
    Builder resolved = new Builder();
    resolved.obsKey = emptyToNull(env.get(ENV_OBS_KEY));
    resolved.endpoint = env.getOrDefault(ENV_ENDPOINT, DEFAULT_ENDPOINT);
    resolved.insecure = boolEnv(env, ENV_INSECURE, true);
    resolved.serviceName = env.getOrDefault(ENV_SERVICE_NAME, "unknown-service");
    resolved.hostIdentOverride = emptyToNull(env.get(ENV_HOST_IDENT));
    resolved.maxQueueSize = intEnv(env, ENV_MAX_QUEUE_SIZE, 2048);
    resolved.scheduleDelayMillis = intEnv(env, ENV_SCHEDULE_DELAY_MILLIS, 5000);
    resolved.maxExportBatchSize = intEnv(env, ENV_MAX_EXPORT_BATCH_SIZE, 512);
    resolved.clientCertificateFile = emptyToNull(env.get(ENV_CLIENT_CERTIFICATE_FILE));
    resolved.clientKeyFile = emptyToNull(env.get(ENV_CLIENT_KEY_FILE));
    resolved.rootCertificateFile = emptyToNull(env.get(ENV_ROOT_CERTIFICATE_FILE));

    if (overrides.obsKey != null) resolved.obsKey = overrides.obsKey;
    if (overrides.endpoint != null) resolved.endpoint = overrides.endpoint;
    if (overrides.insecure != null) resolved.insecure = overrides.insecure;
    if (overrides.serviceName != null) resolved.serviceName = overrides.serviceName;
    if (overrides.hostIdentOverride != null) resolved.hostIdentOverride = overrides.hostIdentOverride;
    if (overrides.maxQueueSize != null) resolved.maxQueueSize = overrides.maxQueueSize;
    if (overrides.scheduleDelayMillis != null) resolved.scheduleDelayMillis = overrides.scheduleDelayMillis;
    if (overrides.maxExportBatchSize != null) resolved.maxExportBatchSize = overrides.maxExportBatchSize;
    if (overrides.clientCertificateFile != null) resolved.clientCertificateFile = overrides.clientCertificateFile;
    if (overrides.clientKeyFile != null) resolved.clientKeyFile = overrides.clientKeyFile;
    if (overrides.rootCertificateFile != null) resolved.rootCertificateFile = overrides.rootCertificateFile;
    if (!overrides.extraResourceAttributes.isEmpty()) {
      resolved.extraResourceAttributes.putAll(overrides.extraResourceAttributes);
    }

    return new KlaroConfig(resolved);
  }

  /** 환경변수(builder 메서드로 지정된 값이 우선) + 명시적 override로 설정을 만드는 빌더. */
  public static final class Builder {
    @Nullable private String obsKey;
    @Nullable private String endpoint;
    @Nullable private Boolean insecure;
    @Nullable private String serviceName;
    @Nullable private String hostIdentOverride;
    @Nullable private Integer maxQueueSize;
    @Nullable private Integer scheduleDelayMillis;
    @Nullable private Integer maxExportBatchSize;
    @Nullable private String clientCertificateFile;
    @Nullable private String clientKeyFile;
    @Nullable private String rootCertificateFile;
    private final Map<String, String> extraResourceAttributes = new LinkedHashMap<>();

    public Builder obsKey(String v) {
      this.obsKey = v;
      return this;
    }

    public Builder endpoint(String v) {
      this.endpoint = v;
      return this;
    }

    public Builder insecure(boolean v) {
      this.insecure = v;
      return this;
    }

    public Builder serviceName(String v) {
      this.serviceName = v;
      return this;
    }

    public Builder hostIdent(String v) {
      this.hostIdentOverride = v;
      return this;
    }

    public Builder maxQueueSize(int v) {
      this.maxQueueSize = v;
      return this;
    }

    public Builder scheduleDelayMillis(int v) {
      this.scheduleDelayMillis = v;
      return this;
    }

    public Builder maxExportBatchSize(int v) {
      this.maxExportBatchSize = v;
      return this;
    }

    public Builder clientCertificateFile(String v) {
      this.clientCertificateFile = v;
      return this;
    }

    public Builder clientKeyFile(String v) {
      this.clientKeyFile = v;
      return this;
    }

    public Builder rootCertificateFile(String v) {
      this.rootCertificateFile = v;
      return this;
    }

    public Builder extraResourceAttribute(String key, String value) {
      this.extraResourceAttributes.put(key, value);
      return this;
    }

    /** 환경변수(System.getenv())로 설정을 완성한다. */
    public KlaroConfig build() {
      return KlaroConfig.resolve(this, System.getenv());
    }
  }
}
