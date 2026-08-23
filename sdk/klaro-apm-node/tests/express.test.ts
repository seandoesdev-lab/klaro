import { InMemorySpanExporter } from '@opentelemetry/sdk-trace-base';
import { afterEach, describe, expect, it } from 'vitest';

import { shutdown } from '../src/tracing';

afterEach(async () => {
  await shutdown();
});

describe('initExpress', () => {
  it('initializes tracing and registers Express instrumentation', async () => {
    const { initExpress } = await import('../src/express');
    const exporter = new InMemorySpanExporter();

    const provider = initExpress(
      {},
      { serviceName: 'checkout-api', exporter, enableHttpInstrumentation: false },
    );

    provider.getTracer('test').startSpan('probe').end();
    await provider.forceFlush();

    const [span] = exporter.getFinishedSpans();
    expect(span.resource.attributes['service.name']).toBe('checkout-api');
  });
});
