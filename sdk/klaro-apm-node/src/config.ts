/**
 * klaro-apm 설정 로딩.
 *
 * 설정 키(환경변수) 이름과 의미는 `sdk/SDK_CONTRACT.md`가 단일 진실 공급원이며, 이 모듈은 그 계약의
 * Node.js 구현체다. 다른 언어(Python/Java) SDK도 동일한 환경변수 이름을 사용해야 한다.
 */

/** OTLP gRPC 메타데이터 헤더 이름 — org 관측 키 인증(HOW-4). */
export const OBS_KEY_HEADER = 'klaro-obs-key';

/** 로컬 개발 기본 Collector 엔드포인트(평문 gRPC). */
export const DEFAULT_ENDPOINT = 'localhost:4317';

export const ENV_OBS_KEY = 'KLARO_OBS_KEY';
export const ENV_ENDPOINT = 'KLARO_OBS_ENDPOINT';
export const ENV_INSECURE = 'KLARO_OBS_INSECURE';
export const ENV_SERVICE_NAME = 'KLARO_OBS_SERVICE_NAME';
export const ENV_HOST_IDENT = 'KLARO_OBS_HOST_IDENT';
export const ENV_MAX_QUEUE_SIZE = 'KLARO_OBS_MAX_QUEUE_SIZE';
export const ENV_SCHEDULE_DELAY_MILLIS = 'KLARO_OBS_SCHEDULE_DELAY_MILLIS';
export const ENV_MAX_EXPORT_BATCH_SIZE = 'KLARO_OBS_MAX_EXPORT_BATCH_SIZE';
export const ENV_CLIENT_CERTIFICATE_FILE = 'KLARO_OBS_CLIENT_CERTIFICATE_FILE';
export const ENV_CLIENT_KEY_FILE = 'KLARO_OBS_CLIENT_KEY_FILE';
export const ENV_ROOT_CERTIFICATE_FILE = 'KLARO_OBS_ROOT_CERTIFICATE_FILE';

export type EnvLike = Record<string, string | undefined>;

function boolEnv(env: EnvLike, name: string, defaultValue: boolean): boolean {
  const raw = env[name];
  if (raw === undefined) return defaultValue;
  return ['1', 'true', 'yes', 'on'].includes(raw.trim().toLowerCase());
}

function intEnv(env: EnvLike, name: string, defaultValue: number): number {
  const raw = env[name];
  if (raw === undefined) return defaultValue;
  const parsed = Number.parseInt(raw, 10);
  return Number.isNaN(parsed) ? defaultValue : parsed;
}

/** klaro-apm 런타임 설정. `resolveConfig()`(환경변수)와 `init()` 옵션으로 채워진다. */
export interface KlaroConfig {
  readonly obsKey?: string;
  readonly endpoint: string;
  readonly insecure: boolean;
  readonly serviceName: string;
  readonly hostIdentOverride?: string;
  readonly maxQueueSize: number;
  readonly scheduleDelayMillis: number;
  readonly maxExportBatchSize: number;
  readonly clientCertificateFile?: string;
  readonly clientKeyFile?: string;
  readonly rootCertificateFile?: string;
  readonly extraResourceAttributes: Readonly<Record<string, string>>;
}

/** `resolveConfig()`에 전달하는 명시적 override. `undefined`는 "지정 안 함"으로 취급되어 무시된다. */
export interface KlaroConfigOverrides {
  obsKey?: string;
  endpoint?: string;
  insecure?: boolean;
  serviceName?: string;
  hostIdentOverride?: string;
  maxQueueSize?: number;
  scheduleDelayMillis?: number;
  maxExportBatchSize?: number;
  clientCertificateFile?: string;
  clientKeyFile?: string;
  rootCertificateFile?: string;
  extraResourceAttributes?: Record<string, string>;
}

/**
 * 환경변수로 기본값을 채우고, 명시적으로 전달된 override로 덮어쓴다(override 우선).
 * `overrides`에서 `undefined`인 필드는 "지정 안 함"으로 취급되어 무시된다(env/기본값 유지).
 */
export function resolveConfig(overrides: KlaroConfigOverrides = {}, env: EnvLike = process.env): KlaroConfig {
  const base: KlaroConfig = {
    obsKey: env[ENV_OBS_KEY] || undefined,
    endpoint: env[ENV_ENDPOINT] || DEFAULT_ENDPOINT,
    insecure: boolEnv(env, ENV_INSECURE, true),
    serviceName: env[ENV_SERVICE_NAME] || 'unknown-service',
    hostIdentOverride: env[ENV_HOST_IDENT] || undefined,
    maxQueueSize: intEnv(env, ENV_MAX_QUEUE_SIZE, 2048),
    scheduleDelayMillis: intEnv(env, ENV_SCHEDULE_DELAY_MILLIS, 5000),
    maxExportBatchSize: intEnv(env, ENV_MAX_EXPORT_BATCH_SIZE, 512),
    clientCertificateFile: env[ENV_CLIENT_CERTIFICATE_FILE] || undefined,
    clientKeyFile: env[ENV_CLIENT_KEY_FILE] || undefined,
    rootCertificateFile: env[ENV_ROOT_CERTIFICATE_FILE] || undefined,
    extraResourceAttributes: {},
  };

  const cleanedOverrides = Object.fromEntries(
    Object.entries(overrides).filter(([, value]) => value !== undefined),
  ) as Partial<KlaroConfig>;

  return { ...base, ...cleanedOverrides };
}

/** OTLP exporter에 첨부할 메타데이터 헤더. obsKey가 없으면 빈 객체(로컬 무인증 개발 허용). */
export function otlpHeaders(config: KlaroConfig): Record<string, string> {
  if (!config.obsKey) return {};
  return { [OBS_KEY_HEADER]: config.obsKey };
}
