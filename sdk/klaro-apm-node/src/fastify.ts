/**
 * Fastify 연동 헬퍼 - initFastify(app) 한 줄로 초기화 + 자동계측.
 *
 * @opentelemetry/instrumentation-fastify 는 optional peer dependency(extra)다 - 불필요한 의존성
 * 강제 설치를 피하기 위해 여기서만 지연 로딩한다(SDK_CONTRACT.md 7).
 */

import { registerInstrumentations } from '@opentelemetry/instrumentation';
import type { NodeTracerProvider } from '@opentelemetry/sdk-trace-node';

import { loadFastifyInstrumentation } from './fastifyInstrumentationLoader';
import { init, type InitOptions } from './tracing';

export function initFastify(_app: unknown, options: InitOptions = {}): NodeTracerProvider {
  let fastifyInstrumentationModule: typeof import('@opentelemetry/instrumentation-fastify');
  try {
    fastifyInstrumentationModule = loadFastifyInstrumentation();
  } catch {
    throw new Error(
      "Fastify 자동계측에는 '@opentelemetry/instrumentation-fastify'가 필요합니다. " +
        'npm install @opentelemetry/instrumentation-fastify 로 설치하세요.',
    );
  }

  const provider = init(options);
  registerInstrumentations({
    tracerProvider: provider,
    instrumentations: [new fastifyInstrumentationModule.FastifyInstrumentation()],
  });
  return provider;
}
