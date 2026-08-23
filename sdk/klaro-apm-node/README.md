# @klaro/apm

klaro 상시 관측 플랫폼(observability)용 Node.js SDK. OpenTelemetry 공식 SDK/자동계측을 그대로
의존성으로 쓰는 **thin wrapper**다 - 커스텀 프로토콜이나 자체 익스포터를 새로 만들지 않는다.

설정 키·리소스 속성 규칙 등 언어 공통 계약은 [`../SDK_CONTRACT.md`](../SDK_CONTRACT.md)를 참조.

## 설치

```bash
npm install @klaro/apm
# Express 자동계측을 쓰려면
npm install @opentelemetry/instrumentation-express
# Fastify 자동계측을 쓰려면
npm install @opentelemetry/instrumentation-fastify
```

## 빠른 시작

```ts
import { init } from '@klaro/apm';

init({ serviceName: 'checkout-api' });
```

환경변수로도 설정할 수 있다(코드 옵션이 있으면 우선):

```bash
export KLARO_OBS_KEY="<org 관측 키>"
export KLARO_OBS_ENDPOINT="collector.klaro.internal:4317"
```

## Express 연동 (한 줄)

```ts
import express from 'express';
import { initExpress } from '@klaro/apm/express';

const app = express();
initExpress(app, { serviceName: 'checkout-api' });
```

## Fastify 연동 (한 줄)

```ts
import Fastify from 'fastify';
import { initFastify } from '@klaro/apm/fastify';

const app = Fastify();
initFastify(app, { serviceName: 'checkout-api' });
```

## 설계 메모 (Python 레퍼런스와의 차이점)

- **큐 포화 시 drop-oldest**: OTel JS 표준 `BatchSpanProcessor`는 큐가 가득 차면 새로 들어오는
  span을 버리는(drop-newest) 반면, SDK_CONTRACT.md §5는 drop-oldest를 요구한다. Python은
  `deque(maxlen=N)`이 자연히 drop-oldest라 재구현이 필요 없었지만, Node에는 그런 표준 컴포넌트가
  없어 `DropOldestBatchSpanProcessor`라는 최소 구현을 추가했다(§5의 "없는 경우에만 최소 구현을
  추가한다" 조항).
- **기본 HTTP 자동계측**: `init()`은 `@opentelemetry/instrumentation-http`를 기본으로 활성화한다
  (`enableHttpInstrumentation: false`로 끌 수 있음). Express/Fastify 전용 계측 패키지는 각각의
  헬퍼에서만 지연 로딩되는 optional peer dependency다(불필요한 의존성 강제 설치 방지).

## 개발

```bash
npm install
npm run build
npm test
```
