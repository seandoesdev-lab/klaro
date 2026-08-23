import { InMemorySpanExporter } from '@opentelemetry/sdk-trace-base';
import { afterEach, describe, expect, it } from 'vitest';

import { init, shutdown } from '../src/tracing';

afterEach(async () => {
  await shutdown();
});

async function resourceOf(provider: ReturnType<typeof init>, exporter: InMemorySpanExporter) {
  provider.getTracer('test').startSpan('probe').end();
  await provider.forceFlush();
  const [span] = exporter.getFinishedSpans();
  return span.resource.attributes;
}

describe('init', () => {
  it('is idempotent and returns the same provider', async () => {
    const exporter = new InMemorySpanExporter();

    const provider1 = init({ serviceName: 'svc-a', exporter, enableHttpInstrumentation: false });
    const provider2 = init({ serviceName: 'svc-b', exporter, enableHttpInstrumentation: false });

    expect(provider1).toBe(provider2);
    // 두 번째 init 호출은 무시되므로 첫 설정(svc-a)이 유지된다.
    expect((await resourceOf(provider1, exporter))['service.name']).toBe('svc-a');
  });

  it('shutdown clears state, allowing re-init', async () => {
    init({ serviceName: 'svc-a', exporter: new InMemorySpanExporter(), enableHttpInstrumentation: false });

    await shutdown();

    const exporter2 = new InMemorySpanExporter();
    const provider2 = init({ serviceName: 'svc-b', exporter: exporter2, enableHttpInstrumentation: false });

    expect((await resourceOf(provider2, exporter2))['service.name']).toBe('svc-b');
  });

  it('shutdown without init is a no-op', async () => {
    await shutdown();
    await shutdown();
  });
});
