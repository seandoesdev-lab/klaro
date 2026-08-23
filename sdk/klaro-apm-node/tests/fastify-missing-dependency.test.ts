import { InMemorySpanExporter } from '@opentelemetry/sdk-trace-base';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { shutdown } from '../src/tracing';

vi.mock('../src/fastifyInstrumentationLoader', () => ({
  loadFastifyInstrumentation: () => {
    throw new Error('simulated: extra not installed');
  },
}));

afterEach(async () => {
  await shutdown();
});

describe('initFastify without the optional instrumentation package', () => {
  it('throws a helpful error naming the missing package', async () => {
    const { initFastify } = await import('../src/fastify');

    expect(() =>
      initFastify({}, { exporter: new InMemorySpanExporter(), enableHttpInstrumentation: false }),
    ).toThrow(/@opentelemetry\/instrumentation-fastify/);
  });
});
