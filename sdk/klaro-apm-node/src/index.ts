/**
 * klaro-apm - klaro 상시 관측 플랫폼용 OpenTelemetry thin wrapper SDK (Node.js).
 *
 * 빠른 시작:
 *   import { init } from '@klaro/apm';
 *   init({ serviceName: 'checkout-api' });
 *
 * Express:
 *   import express from 'express';
 *   import { initExpress } from '@klaro/apm/express';
 *
 *   const app = express();
 *   initExpress(app, { serviceName: 'checkout-api' });
 *
 * 설정 키/리소스 속성 규칙은 sdk/SDK_CONTRACT.md 를 참조.
 */

export { VERSION } from './version';
export {
  DEFAULT_ENDPOINT,
  OBS_KEY_HEADER,
  otlpHeaders,
  resolveConfig,
  type KlaroConfig,
  type KlaroConfigOverrides,
} from './config';
export { detectPodUid, resolveHostIdent } from './hostIdent';
export {
  buildChannelCredentials,
  buildExporter,
  buildMetadata,
  buildResource,
  buildTracerProvider,
  init,
  shutdown,
  type InitOptions,
} from './tracing';
export { DropOldestBatchSpanProcessor } from './dropOldestBatchSpanProcessor';
