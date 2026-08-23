import { describe, expect, it } from 'vitest';

import { detectPodUid, resolveHostIdent } from '../src/hostIdent';

const CGROUP_WITH_POD_UID =
  '12:memory:/kubepods.slice/kubepods-burstable.slice/' +
  'kubepods-burstable-pod1a2b3c4d_5e6f_7890_abcd_1234567890ab.slice/' +
  'docker-abc123.scope\n';
const CGROUP_WITHOUT_POD_UID = '12:memory:/docker/abc123def456\n';

describe('resolveHostIdent', () => {
  it('returns explicit override', () => {
    expect(resolveHostIdent('custom-ident')).toBe('custom-ident');
  });

  it('uses pod uid when available', () => {
    const result = resolveHostIdent(undefined, {
      cgroupReader: () => CGROUP_WITH_POD_UID,
      env: {},
    });

    expect(result).toBe('pod:1a2b3c4d-5e6f-7890-abcd-1234567890ab');
  });

  it('falls back to hostname and pid', () => {
    const result = resolveHostIdent(undefined, {
      cgroupReader: () => CGROUP_WITHOUT_POD_UID,
      env: {},
      hostnameFn: () => 'web-1',
      pidFn: () => 4242,
    });

    expect(result).toBe('host:web-1:4242');
  });
});

describe('detectPodUid', () => {
  it('prefers KLARO_POD_UID env var', () => {
    expect(detectPodUid({ cgroupReader: () => '', env: { KLARO_POD_UID: '  pod-abc  ' } })).toBe('pod-abc');
  });

  it('falls back to standard POD_UID env var', () => {
    expect(detectPodUid({ cgroupReader: () => '', env: { POD_UID: 'pod-xyz' } })).toBe('pod-xyz');
  });

  it('extracts uid from cgroup path', () => {
    expect(detectPodUid({ cgroupReader: () => CGROUP_WITH_POD_UID, env: {} })).toBe(
      '1a2b3c4d-5e6f-7890-abcd-1234567890ab',
    );
  });

  it('returns undefined when not containerized', () => {
    expect(detectPodUid({ cgroupReader: () => CGROUP_WITHOUT_POD_UID, env: {} })).toBeUndefined();
  });
});
