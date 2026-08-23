# klaro-apm (Java)

klaro 상시 관측 플랫폼(observability)용 Java SDK. OpenTelemetry 공식 SDK를 그대로 의존성으로 쓰는
**thin wrapper**다 - 커스텀 프로토콜이나 자체 익스포터를 새로 만들지 않는다.

설정 키·리소스 속성 규칙 등 언어 공통 계약은 [`../SDK_CONTRACT.md`](../SDK_CONTRACT.md)를 참조.

## 설치 (Gradle)

```kotlin
dependencies {
    implementation("io.klaro:klaro-apm:0.1.0")
}
```

(Maven Central에는 아직 게시되지 않았다 - 사내 레포/로컬 설치로 사용한다.)

## 빠른 시작

```java
import io.klaro.apm.KlaroApm;
import io.klaro.apm.KlaroConfig;

KlaroApm.init(KlaroConfig.builder().serviceName("checkout-api"));
```

환경변수로도 설정할 수 있다(빌더 옵션이 있으면 우선):

```bash
export KLARO_OBS_KEY="<org 관측 키>"
export KLARO_OBS_ENDPOINT="collector.klaro.internal:4317"
```

`KlaroApm.init()`은 idempotent하다 - 이미 초기화된 경우 기존 `OpenTelemetry` 인스턴스를 그대로
반환한다. 애플리케이션 종료 시 `KlaroApm.shutdown()`을 호출하거나(JVM shutdown hook이 자동
등록되므로 생략 가능) 명시적으로 호출해 배치 프로세서를 flush한다.

## Spring Boot 연동 (starter)

애플리케이션 코드를 바꾸지 않고 두 의존성만 추가하면 자동계측이 활성화된다:

```kotlin
dependencies {
    implementation("io.klaro:klaro-apm:0.1.0")
    implementation("io.opentelemetry.instrumentation:opentelemetry-spring-boot-starter:2.31.0")
}
```

`io.klaro.apm.spring.KlaroApmEnvironmentPostProcessor`(`META-INF/spring.factories`로 등록)가
`KLARO_OBS_*` 환경변수를 공식 OTel Spring Boot starter가 읽는 `otel.*` 프로퍼티
(`otel.service.name`, `otel.exporter.otlp.endpoint`, `otel.exporter.otlp.headers`,
`otel.resource.attributes`)로 변환해 `Environment`에 낮은 우선순위로 주입한다 - 사용자가 이미
`application.properties`나 `OTEL_*` 환경변수로 같은 키를 지정했다면 그 값이 항상 우선한다.

## mTLS

```java
KlaroApm.init(
    KlaroConfig.builder()
        .endpoint("collector.klaro.internal:4317")
        .clientCertificateFile("/etc/klaro/client.crt")
        .clientKeyFile("/etc/klaro/client.key")
        .rootCertificateFile("/etc/klaro/ca.crt"));
```

## 설계 메모 (Python/Node 레퍼런스와의 차이점)

- **큐 포화 시 drop-oldest**: OTel Java 표준 `BatchSpanProcessor`는 큐가 가득 차면 새로 들어오는
  span을 버리는(drop-newest) 반면, SDK_CONTRACT.md 5는 drop-oldest를 요구한다(Node와 동일한
  이유로 재구현 필요 - Python만 `deque(maxlen=N)`이 우연히 drop-oldest였다). 이 SDK는
  `DropOldestBatchSpanProcessor`라는 최소 구현을 추가했다. 실제 네트워크 전송/실패 판정은 여전히
  주입된 `SpanExporter`(OTel `OtlpGrpcSpanExporter`)에 전량 위임한다.
- **엔드포인트 형식**: SDK_CONTRACT.md는 언어 공통으로 `host:port`(예: `localhost:4317`)를 쓰지만,
  OTel Java의 `OtlpGrpcSpanExporterBuilder.setEndpoint()`는 `http://`/`https://` 스킴이 붙은 URL을
  요구한다. `KlaroApm.normalizeEndpoint()`가 insecure/mTLS 여부에 따라 스킴을 자동으로 붙인다.
- **전역 등록**: Python/Node의 전역 tracer provider 등록은 재호출해도 조용히 덮어써지지만, Java의
  `GlobalOpenTelemetry.set()`은 JVM당 단 한 번만 허용되고 재호출 시 예외를 던진다. 이 SDK는
  자체 상태 캐시로 idempotent한 `init()`만 제공하고 전역 싱글턴 등록은 강제하지 않는다 - 필요하면
  `GlobalOpenTelemetry.set(KlaroApm.init(...))`을 호출측이 직접(한 번만) 호출한다.
- **자동계측**: 순수 SDK 임베딩 경로(`KlaroApm.init()`)는 Python 코어와 마찬가지로 임의 라이브러리를
  자동계측하지 않는다. 프레임워크 자동계측이 필요하면 (1) Spring Boot는 위 starter 조합을 쓰거나,
  (2) 다른 프레임워크는 공식 OTel Java agent(`-javaagent:opentelemetry-javaagent.jar`, HOW-11이
  명시적으로 허용하는 "agent 또는 SDK" 중 agent 경로)를 붙이고 `KLARO_OBS_KEY`를
  `OTEL_EXPORTER_OTLP_HEADERS=klaro-obs-key=<key>`로, `KLARO_OBS_ENDPOINT`를
  `OTEL_EXPORTER_OTLP_ENDPOINT`로 매핑해 실행한다.

## 개발

```bash
./gradlew build   # 컴파일 + 테스트 + jar
./gradlew test    # 테스트만
```
