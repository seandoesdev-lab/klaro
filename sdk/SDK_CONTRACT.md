# klaro-apm SDK 계약 (언어 공통)

**적용 대상**: `sdk/klaro-apm-python/` (구현 완료). `sdk/klaro-apm-node/`, `sdk/klaro-apm-java/`는 향후
같은 계약을 따라야 한다(HOW-11, 착수 순서는 CLAUDE.md 미확정 사항 참조).

**원칙**: klaro-apm은 OpenTelemetry 공식 SDK/자동계측을 그대로 쓰는 **thin wrapper**다. OTel이 이미
제공하는 기능(비블로킹 배치 export, 큐 드롭 정책, 실패 시 예외 비전파, 재시도)을 재구현하지 않는다.
언어별 구현이 새로 만들어야 하는 부분은 아래 4가지뿐이다: 설정 로딩, 헤더 첨부, host_ident 정규화,
언어 프레임워크 연동 헬퍼.

---

## 1. 환경변수(설정 키)

모든 언어 SDK는 동일한 이름을 사용한다. 코드로 전달한 옵션이 있으면 환경변수보다 우선한다.

| 환경변수 | 의미 | 기본값 |
|---|---|---|
| `KLARO_OBS_KEY` | org 관측 키 시크릿(HOW-4). 없으면 인증 헤더를 첨부하지 않는다(로컬 무인증 개발 허용). | (없음) |
| `KLARO_OBS_ENDPOINT` | OTel Collector gRPC 엔드포인트 | `localhost:4317` |
| `KLARO_OBS_INSECURE` | 평문 gRPC 허용 여부(`true`/`false`) — mTLS 파일이 설정되면 무시됨 | `true` |
| `KLARO_OBS_SERVICE_NAME` | `service.name` 리소스 속성 | `unknown-service` |
| `KLARO_OBS_HOST_IDENT` | `service.instance.id` 강제 override(자동 탐지 무시) | (없음, 자동 탐지) |
| `KLARO_OBS_MAX_QUEUE_SIZE` | 배치 프로세서 큐 크기(포화 시 drop-oldest) | `2048` |
| `KLARO_OBS_SCHEDULE_DELAY_MILLIS` | 배치 export 주기(ms) | `5000` |
| `KLARO_OBS_MAX_EXPORT_BATCH_SIZE` | 1회 export 배치 최대 span 수 | `512` |
| `KLARO_OBS_CLIENT_CERTIFICATE_FILE` | mTLS 클라이언트 인증서 파일 경로(옵션 노출만, §4) | (없음) |
| `KLARO_OBS_CLIENT_KEY_FILE` | mTLS 클라이언트 개인키 파일 경로 | (없음) |
| `KLARO_OBS_ROOT_CERTIFICATE_FILE` | mTLS 신뢰 루트 CA 파일 경로 | (없음) |

pod UID 자동 탐지(§3)에 쓰이는 부가 환경변수: `KLARO_POD_UID`(klaro 전용, 최우선) 및 표준
`POD_UID`(k8s Downward API로 주입된 값이 있으면 사용).

## 2. 인증 헤더

`KLARO_OBS_KEY`(또는 동등한 init 옵션)가 설정되면, OTLP exporter의 gRPC 메타데이터에 다음 헤더를
**항상** 첨부한다:

```
klaro-obs-key: <시크릿 값>
```

키가 없으면 헤더를 생략한다(로컬 Collector가 무인증으로 받아주는 개발 편의 경로).

## 3. `service.instance.id` (host_ident) 정규화 — HOW-4 정합

OTel Resource 속성 `service.instance.id`를 아래 우선순위로 자동 계산한다. 모든 언어 SDK가 **동일한
우선순위와 값 포맷**을 지켜야 org의 `observability_hosts.host_ident`(HOW-4) 집계가 언어에 관계없이
일관된다.

1. **명시적 override** — 코드 옵션(`host_ident=...`) 또는 `KLARO_OBS_HOST_IDENT` 환경변수. 값을 그대로 사용.
2. **컨테이너 pod UID** — 다음 순서로 탐지:
   - `KLARO_POD_UID` 환경변수
   - `POD_UID` 환경변수(k8s Downward API, `fieldRef: metadata.uid`)
   - `/proc/self/cgroup` 내용에서 `kubepods` 계열 경로의 pod UID 패턴(`xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`, `_` 구분자 허용) 추출
   - 찾으면 값 포맷: `pod:<uid>` (하이픈 정규화, 소문자 그대로)
3. **hostname+PID 폴백** — 위에서 못 찾으면 `host:<hostname>:<pid>`.

## 4. mTLS

SDK↔Collector 통신은 CLAUDE.md 통신 규약상 **프로덕션에서 mTLS 필수**다. 이 SDK 계약은 **옵션
노출만** 요구한다 — 로컬 개발 기본값은 평문(`insecure=true`)이며, mTLS 강제는 Collector/배포 설정이
담당한다(SDK가 강제하지 않음). 언어 SDK는 클라이언트 인증서/키/루트CA 파일 경로를 옵션으로 받아
gRPC 채널 자격증명을 구성해야 한다. mTLS 옵션이 설정되면 `insecure` 설정은 무시된다.

## 5. 비블로킹 배치 export / 큐 정책

- export는 반드시 호출 스레드를 막지 않아야 한다(OTel 표준 BatchSpanProcessor 계열 사용).
- 큐가 포화되면 **가장 오래된 항목을 버린다(drop-oldest)** — 최신 데이터를 우선한다.
- Collector 연결 실패는 **고객 애플리케이션에 예외를 전파하지 않는다**. 실패는 조용히 기록되고,
  다음 배치 주기에 자연스럽게 재시도된다.
- Python 구현은 이 세 가지를 전부 OTel SDK의 기본 `BatchSpanProcessor`/`OTLPSpanExporter` 동작에
  위임한다(재구현 없음). 다른 언어도 동일 원칙을 따를 것 — 언어별 OTel SDK가 이미 제공하는
  배치/드롭/재시도 동작을 그대로 사용하고, 없는 경우에만 최소 구현을 추가한다.

## 6. 샘플링

기본 샘플러는 **head 샘플링 없이 전부 전송**(Python: `ParentBased(ALWAYS_ON)`)한다. tail-based
샘플링(APM-01 ≤2% 오버헤드 NFR 충족)은 **Collector의 tailsampling processor**가 전체 트레이스를 보고
결정한다 — SDK가 클라이언트 측에서 데이터를 먼저 버리면 tail 판단에 필요한 정보가 손실되므로, SDK는
절대 head 확률 샘플링으로 span을 드롭하지 않는다.

## 7. 프레임워크 연동 헬퍼

언어별 주요 웹 프레임워크에 대해 "한 줄 초기화" 헬퍼를 제공한다. Python은 FastAPI
(`klaro_apm.init_fastapi(app, service_name=...)`) — 내부적으로 `init()` 호출 + 해당 OTel
instrumentation 패키지의 `instrument_app`을 수행한다. 프레임워크 계측 패키지는 optional
dependency(extra)로 분리한다(불필요한 의존성 강제 설치 방지).

## 8. 버전 정책

SemVer. wrapper 공개 API(설정 키 이름, 함수 시그니처)의 breaking change만 major를 올린다. 내부 OTel
코어 SDK 버전 추종은 minor/patch로 흡수한다(OTLP 프로토콜 하위호환성에 의존).

## 9. Python 구현 매핑

| 계약 항목 | Python 구현 위치 |
|---|---|
| 설정 로딩 | `klaro_apm/config.py` (`KlaroConfig.from_env`) |
| 인증 헤더 | `KlaroConfig.otlp_headers()` |
| host_ident 정규화 | `klaro_apm/host_ident.py` (`resolve_host_ident`) |
| mTLS 옵션 | `klaro_apm/tracing.py` (`_build_channel_credentials`) |
| 초기화/샘플링/배치 | `klaro_apm/tracing.py` (`init`, `build_tracer_provider`) |
| FastAPI 헬퍼 | `klaro_apm/fastapi.py` (`init_fastapi`) |
| 로그 상관키(§12) | `klaro_apm/correlation.py` (`install_log_correlation`, `correlation_fields`) |

## 10. Node 구현 매핑

| 계약 항목 | Node 구현 위치 |
|---|---|
| 설정 로딩 | `src/config.ts` (`resolveConfig`) |
| 인증 헤더 | `otlpHeaders()` → `buildMetadata()`(`src/tracing.ts`, `grpc.Metadata`로 변환) |
| host_ident 정규화 | `src/hostIdent.ts` (`resolveHostIdent`) |
| mTLS 옵션 | `src/tracing.ts` (`buildChannelCredentials`) |
| 초기화/샘플링/배치 | `src/tracing.ts` (`init`, `buildTracerProvider`) |
| Express 헬퍼 | `src/express.ts` (`initExpress`) — `@opentelemetry/instrumentation-express`는 optional peer dependency |
| Fastify 헬퍼 | `src/fastify.ts` (`initFastify`) — `@opentelemetry/instrumentation-fastify`는 optional peer dependency |
| 로그 상관키(§12) | `src/correlation.ts` (`correlationFields`, `withCorrelation`) |

**§5 drop-oldest 관련 Node 특이사항**: OTel JS 표준 `BatchSpanProcessor`는 큐 포화 시
drop-newest(신규 span 거부)라서 §5가 요구하는 drop-oldest와 반대다. Python은 `deque(maxlen=N)`이
자연히 drop-oldest라 재구현이 필요 없었지만, Node에는 동등한 표준 컴포넌트가 없어 §5의 "없는
경우에만 최소 구현을 추가한다" 조항에 따라 `src/dropOldestBatchSpanProcessor.ts`
(`DropOldestBatchSpanProcessor`)를 최소 구현으로 추가했다. 실제 네트워크 전송/실패 판정은 여전히
주입된 `OTLPTraceExporter`에 전량 위임한다(재시도 로직 자체 구현 없음).

## 11. Java 구현 매핑

| 계약 항목 | Java 구현 위치 |
|---|---|
| 설정 로딩 | `src/main/java/io/klaro/apm/KlaroConfig.java` (`KlaroConfig.resolve`) |
| 인증 헤더 | `KlaroConfig.otlpHeaders()` → `OtlpGrpcSpanExporterBuilder.addHeader()`(`KlaroApm.buildExporter`) |
| host_ident 정규화 | `src/main/java/io/klaro/apm/HostIdent.java` (`HostIdent.resolve`) |
| mTLS 옵션 | `KlaroApm.buildExporter()`(`setTrustedCertificates`/`setClientTls`) |
| 초기화/샘플링/배치 | `src/main/java/io/klaro/apm/KlaroApm.java` (`init`, `buildTracerProvider`) |
| Spring Boot 헬퍼 | `src/main/java/io/klaro/apm/spring/KlaroApmEnvironmentPostProcessor.java` — `spring-boot`는 compileOnly(선택 의존성), 실제 자동계측은 공식 `io.opentelemetry.instrumentation:opentelemetry-spring-boot-starter`와 조합해 "starter" 형태로 제공 |
| 로그 상관키(§12) | `src/main/java/io/klaro/apm/Correlation.java` (`fields`, `traceId`, `spanId`) |

**§5 drop-oldest 관련 Java 특이사항**: OTel Java 표준 `BatchSpanProcessor`도 Node와 동일하게
drop-newest다(`Worker.addSpan()`이 고정 크기 큐에 `offer()`만 시도하고 실패하면 새 span을 버림 —
OTel Java 1.65.0 소스로 확인). §5의 "없는 경우에만 최소 구현을 추가한다" 조항에 따라
`src/main/java/io/klaro/apm/DropOldestBatchSpanProcessor.java`를 추가했다. Node의 이벤트 루프
기반 구현과 달리 Java는 실제 데몬 스레드로 배치를 처리한다(비블로킹은 `onEnd()`가 동기화된 큐에
push만 하고 반환하는 것으로 보장). 실제 네트워크 전송/실패 판정은 여전히 주입된 `SpanExporter`
(OTel `OtlpGrpcSpanExporter`)에 전량 위임한다.

**Java 특이사항(그 외)**:
- **엔드포인트 스킴**: OTel Java의 `OtlpGrpcSpanExporterBuilder.setEndpoint()`는 `http://`/
  `https://` 스킴이 붙은 URL을 요구해, 언어 공통 `host:port` 형식에 스킴을 자동으로 붙이는
  `KlaroApm.normalizeEndpoint()`가 필요했다(§1의 `endpoint` 값 자체는 계약대로 스킴 없이 유지).
- **전역 등록**: Python(`trace.set_tracer_provider`)과 Node(`provider.register()`)는 재호출해도
  마지막 값으로 조용히 덮어써지지만, Java의 `GlobalOpenTelemetry.set()`은 JVM당 단 한 번만
  허용되고 재호출 시 예외를 던진다. `KlaroApm.init()`은 자체 상태 캐시로 idempotent하지만 전역
  싱글턴 등록은 강제하지 않는다 — 필요하면 호출측이 `GlobalOpenTelemetry.set(KlaroApm.init(...))`
  을 직접(한 번만) 호출한다.
- **자동계측(HOW-11 "agent 또는 SDK")**: 순수 SDK 임베딩 경로(`KlaroApm.init()`)는 임의
  라이브러리를 자동계측하지 않는다(Python 코어와 동일). Spring Boot는 위 starter 조합으로 제로
  코드 자동계측을 제공하고, 그 외 프레임워크는 공식 OTel Java agent를 붙이는 경로를 문서화했다
  (`sdk/klaro-apm-java/README.md`).

## 12. 상관키 규약 (로그↔트레이스↔메트릭)

관측 플랫폼의 연계분석 엔드포인트(`GET /orgs/:orgId/obs/traces/:traceId/correlated`)는 하나의
트레이스를 **그 안에서 쓰인 로그**와 **그 서비스·호스트의 메트릭**에 조인한다. 조인 키는 방향마다
다르고, 언어 SDK가 책임지는 부분도 다르다.

| 방향 | 조인 키 | 실리는 위치 | 정확도 |
|---|---|---|---|
| 트레이스 → 로그 | `trace_id` (+ `span_id`) | **로그 레코드**(속성/structured metadata) | 정확 |
| 트레이스 → 메트릭 | `service.name` + `service.instance.id` | **리소스 속성** | 근사(같은 프로세스·같은 구간) |

### 12.1 키 이름과 형식

- 이름은 정확히 `trace_id`, `span_id`다. Loki의 OTLP 수신기가 로그 레코드의 트레이스 컨텍스트를
  structured metadata에 넣을 때 쓰는 이름이며, 백엔드의 상관 조회
  (`services/observability/internal/explorer/logs.go`)가 이 이름으로 LogQL label filter를 만든다.
  다른 이름을 쓰면 조회는 **에러가 아니라 빈 결과**가 된다.
- 형식은 소문자 16진수: `trace_id` 32자, `span_id` 16자. W3C traceparent와 OTel `format_trace_id`의
  형식이다.
- **스팬이 없을 때 0으로 채워진 id를 만들어내지 않는다.** OTel의 invalid SpanContext는 trace_id가
  전부 0인 정상 값처럼 보이므로, 그대로 로그에 찍히면 존재하지 않는 트레이스를 조회하게 만든다.
  스팬이 없으면 키를 생략하거나 빈 문자열을 쓴다.

### 12.2 `trace_id`는 리소스 속성이 아니다

리소스는 **프로세스 단위** 속성이고 trace_id는 **요청 단위** 값이다. 리소스에 실으면 그 프로세스의
모든 텔레메트리가 하나의 트레이스에 속한 것으로 표시된다. `trace_id`/`span_id`는 반드시 로그
레코드(또는 그 속성)에 싣는다. 리소스가 담당하는 것은 §3의 `service.name`/`service.instance.id`,
즉 **메트릭 방향의 2차 키**다 — 메트릭 시리즈에는 trace_id가 존재할 수 없으므로(카운터는 요청
단위가 아니다) 스팬은 서비스+호스트로만 자기 메트릭에 도달한다.

또한 `trace_id`를 **Loki 스트림 라벨로 색인하지 않는다**. 요청마다 값이 달라 카디널리티가 무한하므로
요청당 스트림 하나가 만들어진다. structured metadata에 두고 label filter로 조회하는 것이 규약이다.

### 12.3 SDK가 하는 일 / 하지 않는 일

klaro-apm은 **로그 파이프라인을 소유하지 않는다.** 고객 애플리케이션의 로그는 자체 로거로 나가고,
SDK는 그 로거를 교체하지 않는다(관측 SDK가 로깅 설정을 가져가는 것은 thin wrapper가 아니다).
SDK가 제공하는 것은 **현재 스팬 컨텍스트를 로그 한 줄에 붙일 수 있는 형태로 노출하는 것**뿐이다.

OTel 공식 로그 브리지/MDC appender(예: `opentelemetry-logback-mdc`, `LoggingInstrumentor`)를
사용하는 애플리케이션은 이미 같은 키가 붙으므로 이 API가 필요 없다. 아래는 그 의존성을 강제하지
않는 최소 경로다.

| 언어 | API | 위치 |
|---|---|---|
| Python | `install_log_correlation()`(logging.Filter 부착), `correlation_fields()` | `klaro_apm/correlation.py` |
| Node | `correlationFields()`, `withCorrelation(payload)` | `src/correlation.ts` |
| Java | `Correlation.fields()`, `Correlation.traceId()`, `Correlation.spanId()` | `io/klaro/apm/Correlation.java` |

Python만 전역 훅(`logging.Filter`)을 제공하는 이유: Python에는 표준 logging 파이프라인이 있어 필터
하나로 모든 레코드를 덮을 수 있다. Node/Java에는 그런 단일 지점이 없어(pino mixin, winston format,
logback MDC가 서로 다르다) 특정 로깅 라이브러리를 특별대우하는 대신 **어느 로거에도 넣을 수 있는
값**을 반환한다. Java는 MDC에 직접 쓰지 않는다 — MDC는 스레드 로컬이라 SDK가 넣으면 지우는 책임까지
져야 하고, 스레드 풀에서 지우지 못한 값은 다음 요청의 로그에 남는다. 조용히 틀린 상관은 상관이
없는 것보다 나쁘므로 수명은 호출측(try-with-resources)에 맡긴다.

### 12.4 상관이 비어 보일 때

`correlated` 응답의 `notes`에 "no log line carries this trace id"가 오면, 원인은 대부분 로그가
없는 것이 아니라 **로그에 키가 없는 것**이다. 확인 순서: (1) 애플리케이션이 위 API 또는 OTel 로그
브리지로 `trace_id`를 붙이는지, (2) 로그를 만든 코드가 실제로 스팬 안에서 실행되는지(백그라운드
워커는 부모 컨텍스트를 상속하지 않는 경우가 많다), (3) 로그가 Collector의 logs 파이프라인을 통해
Loki에 들어가는지.
