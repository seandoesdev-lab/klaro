import { describe, expect, it } from 'vitest';

import { DEFAULT_ENDPOINT, OBS_KEY_HEADER, otlpHeaders, resolveConfig } from '../src/config';

describe('resolveConfig', () => {
  it('defaults when no env set', () => {
    const config = resolveConfig({}, {});

    expect(config.obsKey).toBeUndefined();
    expect(config.endpoint).toBe(DEFAULT_ENDPOINT);
    expect(config.insecure).toBe(true);
    expect(config.serviceName).toBe('unknown-service');
    expect(otlpHeaders(config)).toEqual({});
  });

  it('reads KLARO_OBS_KEY and builds header', () => {
    const config = resolveConfig({}, { KLARO_OBS_KEY: 'secret-123' });

    expect(config.obsKey).toBe('secret-123');
    expect(otlpHeaders(config)).toEqual({ [OBS_KEY_HEADER]: 'secret-123' });
  });

  it('reads endpoint and service name from env', () => {
    const config = resolveConfig(
      {},
      {
        KLARO_OBS_ENDPOINT: 'collector.internal:4317',
        KLARO_OBS_SERVICE_NAME: 'checkout-api',
        KLARO_OBS_INSECURE: 'false',
      },
    );

    expect(config.endpoint).toBe('collector.internal:4317');
    expect(config.serviceName).toBe('checkout-api');
    expect(config.insecure).toBe(false);
  });

  it('explicit overrides win over env', () => {
    const config = resolveConfig({ serviceName: 'from-init-arg' }, { KLARO_OBS_SERVICE_NAME: 'from-env' });

    expect(config.serviceName).toBe('from-init-arg');
  });

  it('undefined overrides are ignored', () => {
    const config = resolveConfig({ serviceName: undefined }, { KLARO_OBS_SERVICE_NAME: 'from-env' });

    expect(config.serviceName).toBe('from-env');
  });
});
