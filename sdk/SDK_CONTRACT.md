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

**§5 drop-oldest 관련 Node 특이사항**: OTel JS 표준 `BatchSpanProcessor`는 큐 포화 시
drop-newest(신규 span 거부)라서 §5가 요구하는 drop-oldest와 반대다. Python은 `deque(maxlen=N)`이
자연히 drop-oldest라 재구현이 필요 없었지만, Node에는 동등한 표준 컴포넌트가 없어 §5의 "없는
경우에만 최소 구현을 추가한다" 조항에 따라 `src/dropOldestBatchSpanProcessor.ts`
(`DropOldestBatchSpanProcessor`)를 최소 구현으로 추가했다. 실제 네트워크 전송/실패 판정은 여전히
주입된 `OTLPTraceExporter`에 전량 위임한다(재시도 로직 자체 구현 없음).
