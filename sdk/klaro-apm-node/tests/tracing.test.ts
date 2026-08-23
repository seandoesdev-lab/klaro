import { InMemorySpanExporter } from '@opentelemetry/sdk-trace-base';
import { describe, expect, it } from 'vitest';

import { OBS_KEY_HEADER, resolveConfig } from '../src/config';
import { buildExporter, buildMetadata, buildTracerProvider } from '../src/tracing';

describe('buildMetadata', () => {
  it('attaches klaro-obs-key metadata when obsKey is set', () => {
    const config = resolveConfig({ obsKey: 'secret-123' }, {});

    const metadata = buildMetadata(config);

    expect(metadata.get(OBS_KEY_HEADER)).toEqual(['secret-123']);
  });

  it('omits the header when no obsKey is set', () => {
    const config = resolveConfig({}, {});

    const metadata = buildMetadata(config);

    expect(metadata.get(OBS_KEY_HEADER)).toEqual([]);
  });
});

describe('buildExporter', () => {
  it('builds an OTLP/gRPC exporter without throwing', () => {
    const config = resolveConfig({ obsKey: 'secret-123', endpoint: 'localhost:4317' }, {});

    expect(() => buildExporter(config)).not.toThrow();
  });
});

describe('buildTracerProvider', () => {
  it('sets service.name and host_ident resource attributes on emitted spans', async () => {
    const config = resolveConfig({ serviceName: 'checkout-api', hostIdentOverride: 'host:web-1:4242' }, {});
    const exporter = new InMemorySpanExporter();

    const { provider, processor } = buildTracerProvider(config, exporter);
    const tracer = provider.getTracer('test');

    tracer.startSpan('checkout').end();
    await processor.forceFlush();

    const [span] = exporter.getFinishedSpans();
    expect(span.resource.attributes['service.name']).toBe('checkout-api');
    expect(span.resource.attributes['service.instance.id']).toBe('host:web-1:4242');

    await processor.shutdown();
  });

  it('exports spans without blocking the caller (in-memory exporter)', async () => {
    const config = resolveConfig({ serviceName: 'checkout-api' }, {});
    const exporter = new InMemorySpanExporter();

    const { provider, processor } = buildTracerProvider(config, exporter);
    const tracer = provider.getTracer('test');

    tracer.startSpan('checkout').end();

    await processor.forceFlush();

    const finished = exporter.getFinishedSpans();
    expect(finished).toHaveLength(1);
    expect(finished[0].name).toBe('checkout');

    await processor.shutdown();
  });

  it('drops the oldest spans when the queue is saturated (drop-oldest, SDK_CONTRACT.md section 5)', async () => {
    const config = resolveConfig(
      {
        serviceName: 'checkout-api',
        maxQueueSize: 5,
        scheduleDelayMillis: 60_000,
        maxExportBatchSize: 5,
      },
      {},
    );
    const exporter = new InMemorySpanExporter();

    const { provider, processor } = buildTracerProvider(config, exporter);
    const tracer = provider.getTracer('test');

    for (let i = 0; i < 10; i++) {
      tracer.startSpan(`span-${i}`).end();
    }

    await processor.forceFlush();
    const finishedNames = new Set(exporter.getFinishedSpans().map((span) => span.name));

    expect(finishedNames.size).toBe(5);
    expect(finishedNames).toEqual(new Set(Array.from({ length: 5 }, (_, i) => `span-${i + 5}`)));

    await processor.shutdown();
  });
});
