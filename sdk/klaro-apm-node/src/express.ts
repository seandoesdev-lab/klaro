/**
 * Express 연동 헬퍼 - initExpress(app) 한 줄로 초기화 + 자동계측.
 *
 * @opentelemetry/instrumentation-express 는 optional peer dependency(extra)다 - 불필요한 의존성
 * 강제 설치를 피하기 위해 여기서만 지연 로딩한다(SDK_CONTRACT.md 7).
 */

import { registerInstrumentations } from '@opentelemetry/instrumentation';
import type { NodeTracerProvider } from '@opentelemetry/sdk-trace-node';

import { loadExpressInstrumentation } from './expressInstrumentationLoader';
import { init, type InitOptions } from './tracing';

export function initExpress(_app: unknown, options: InitOptions = {}): NodeTracerProvider {
  let expressInstrumentationModule: typeof import('@opentelemetry/instrumentation-express');
  try {
    expressInstrumentationModule = loadExpressInstrumentation();
  } catch {
    throw new Error(
      "Express 자동계측에는 '@opentelemetry/instrumentation-express'가 필요합니다. " +
        'npm install @opentelemetry/instrumentation-express 로 설치하세요.',
    );
  }

  const provider = init(options);
  registerInstrumentations({
    tracerProvider: provider,
    instrumentations: [new expressInstrumentationModule.ExpressInstrumentation()],
  });
  return provider;
}
