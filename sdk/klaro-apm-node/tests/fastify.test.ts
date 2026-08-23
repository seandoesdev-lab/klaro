import { InMemorySpanExporter } from '@opentelemetry/sdk-trace-base';
import { afterEach, describe, expect, it } from 'vitest';

import { shutdown } from '../src/tracing';

afterEach(async () => {
  await shutdown();
});

describe('initFastify', () => {
  it('initializes tracing and registers Fastify instrumentation', async () => {
    const { initFastify } = await import('../src/fastify');
    const exporter = new InMemorySpanExporter();

    const provider = initFastify(
      {},
      { serviceName: 'checkout-api', exporter, enableHttpInstrumentation: false },
    );

    provider.getTracer('test').startSpan('probe').end();
    await provider.forceFlush();

    const [span] = exporter.getFinishedSpans();
    expect(span.resource.attributes['service.name']).toBe('checkout-api');
  });
});
