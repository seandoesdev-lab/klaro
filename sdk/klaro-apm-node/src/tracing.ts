/**
 * klaro-apm 초기화 핵심.
 *
 * thin wrapper 원칙(HOW-11): OTel 공식 SDK가 이미 제공하는 동작을 재구현하지 않는다.
 *
 * - 비블로킹 배치 익스포트/큐 포화 시 drop-oldest: DropOldestBatchSpanProcessor
 *   (./dropOldestBatchSpanProcessor) 참조 - OTel JS 표준 BatchSpanProcessor는 drop-newest라서
 *   최소 구현을 추가했다(SDK_CONTRACT.md 5).
 * - 연결 실패 시 조용한 재시도: OTLPTraceExporter.export()는 gRPC 에러를 내부에서 처리해
 *   ExportResultCode.FAILURE로 콜백할 뿐 예외를 던지지 않는다. 실패한 배치는 버려지고, 다음
 *   배치 주기에 새로 쌓인 span으로 자연스럽게 재시도가 이어진다.
 */

import { readFileSync } from 'node:fs';

import * as grpc from '@grpc/grpc-js';
import { registerInstrumentations } from '@opentelemetry/instrumentation';
import { HttpInstrumentation } from '@opentelemetry/instrumentation-http';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-grpc';
import { resourceFromAttributes, type Resource } from '@opentelemetry/resources';
import { AlwaysOnSampler, ParentBasedSampler, type SpanExporter } from '@opentelemetry/sdk-trace-base';
import { NodeTracerProvider } from '@opentelemetry/sdk-trace-node';

import { type KlaroConfig, type KlaroConfigOverrides, otlpHeaders, resolveConfig } from './config';
import { DropOldestBatchSpanProcessor } from './dropOldestBatchSpanProcessor';
import { resolveHostIdent } from './hostIdent';

export interface InitOptions extends KlaroConfigOverrides {
  enableHttpInstrumentation?: boolean;
  exporter?: SpanExporter;
}

interface KlaroState {
  provider: NodeTracerProvider | null;
  processor: DropOldestBatchSpanProcessor | null;
  unregisterInstrumentations: (() => void) | null;
}

const state: KlaroState = { provider: null, processor: null, unregisterInstrumentations: null };
let shutdownHookRegistered = false;

export function buildResource(config: KlaroConfig): Resource {
  const hostIdent = resolveHostIdent(config.hostIdentOverride);
  return resourceFromAttributes({
    'service.name': config.serviceName,
    'service.instance.id': hostIdent,
    ...config.extraResourceAttributes,
  });
}

function readFileOrUndefined(path: string | undefined): Buffer | undefined {
  if (!path) return undefined;
  return readFileSync(path);
}

export function buildChannelCredentials(config: KlaroConfig): grpc.ChannelCredentials {
  if (config.clientCertificateFile && config.clientKeyFile) {
    return grpc.credentials.createSsl(
      readFileOrUndefined(config.rootCertificateFile),
      readFileOrUndefined(config.clientKeyFile),
      readFileOrUndefined(config.clientCertificateFile),
    );
  }
  return config.insecure ? grpc.credentials.createInsecure() : grpc.credentials.createSsl();
}

export function buildMetadata(config: KlaroConfig): grpc.Metadata {
  const metadata = new grpc.Metadata();
  for (const [key, value] of Object.entries(otlpHeaders(config))) {
    metadata.set(key, value);
  }
  return metadata;
}

export function buildExporter(config: KlaroConfig): OTLPTraceExporter {
  return new OTLPTraceExporter({
    url: config.endpoint,
    credentials: buildChannelCredentials(config),
    metadata: buildMetadata(config),
  });
}

export function buildTracerProvider(
  config: KlaroConfig,
  exporter?: SpanExporter,
): { provider: NodeTracerProvider; processor: DropOldestBatchSpanProcessor } {
  const resource = buildResource(config);
  const spanExporter = exporter ?? buildExporter(config);

  const processor = new DropOldestBatchSpanProcessor(spanExporter, {
    maxQueueSize: config.maxQueueSize,
    scheduledDelayMillis: config.scheduleDelayMillis,
    maxExportBatchSize: config.maxExportBatchSize,
  });

  const provider = new NodeTracerProvider({
    resource,
    sampler: new ParentBasedSampler({ root: new AlwaysOnSampler() }),
    spanProcessors: [processor],
  });

  return { provider, processor };
}

export function init(options: InitOptions = {}): NodeTracerProvider {
  if (state.provider) return state.provider;

  const { enableHttpInstrumentation = true, exporter, ...overrides } = options;
  const config = resolveConfig(overrides);

  const { provider, processor } = buildTracerProvider(config, exporter);
  provider.register();

  let unregisterInstrumentations: (() => void) | null = null;
  if (enableHttpInstrumentation) {
    unregisterInstrumentations = registerInstrumentations({
      tracerProvider: provider,
      instrumentations: [new HttpInstrumentation()],
    });
  }

  state.provider = provider;
  state.processor = processor;
  state.unregisterInstrumentations = unregisterInstrumentations;

  if (!shutdownHookRegistered) {
    process.once('beforeExit', () => {
      void shutdown();
    });
    shutdownHookRegistered = true;
  }

  return provider;
}

export async function shutdown(): Promise<void> {
  const { processor, unregisterInstrumentations } = state;
  if (!processor) return;

  try {
    unregisterInstrumentations?.();
  } catch {
    // no-op
  }

  try {
    await processor.shutdown();
  } catch {
    // no-op
  }

  state.provider = null;
  state.processor = null;
  state.unregisterInstrumentations = null;
}
