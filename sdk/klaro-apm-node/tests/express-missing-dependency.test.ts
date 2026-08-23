import { InMemorySpanExporter } from '@opentelemetry/sdk-trace-base';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { shutdown } from '../src/tracing';

vi.mock('../src/expressInstrumentationLoader', () => ({
  loadExpressInstrumentation: () => {
    throw new Error('simulated: extra not installed');
  },
}));

afterEach(async () => {
  await shutdown();
});

describe('initExpress without the optional instrumentation package', () => {
  it('throws a helpful error naming the missing package', async () => {
    const { initExpress } = await import('../src/express');

    expect(() =>
      initExpress({}, { exporter: new InMemorySpanExporter(), enableHttpInstrumentation: false }),
    ).toThrow(/@opentelemetry\/instrumentation-express/);
  });
});
