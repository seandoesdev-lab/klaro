/**
 * `service.instance.id` 정규화(HOW-4 host_ident 정합).
 *
 * 우선순위: (1) 명시적 override(코드 인자 또는 KLARO_OBS_HOST_IDENT) → (2) 컨테이너 pod UID
 * (Downward API 환경변수 또는 cgroup 경로에서 추출) → (3) hostname+PID 폴백.
 */

import { readFileSync } from 'node:fs';
import { hostname as osHostname } from 'node:os';

import type { EnvLike } from './config';

/** k8s Downward API로 주입 가능한 pod UID 환경변수(우선순위 순). */
const POD_UID_ENV_VARS = ['KLARO_POD_UID', 'POD_UID'] as const;

/** cgroup 경로(`/proc/self/cgroup`)에 나타나는 kubepods pod UID 패턴(하이픈/언더스코어 혼용 허용). */
const CGROUP_UID_PATTERN =
  /[0-9a-fA-F]{8}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{12}/;

export function readCgroup(path = '/proc/self/cgroup'): string {
  try {
    return readFileSync(path, 'utf-8');
  } catch {
    return '';
  }
}

export interface DetectPodUidOptions {
  cgroupReader?: () => string;
  env?: EnvLike;
}

/** 컨테이너 pod UID를 탐지한다. 못 찾으면 undefined(호출측이 hostname+PID로 폴백). */
export function detectPodUid(options: DetectPodUidOptions = {}): string | undefined {
  const { cgroupReader = readCgroup, env = process.env } = options;

  for (const key of POD_UID_ENV_VARS) {
    const value = env[key];
    if (value) return value.trim();
  }

  const match = CGROUP_UID_PATTERN.exec(cgroupReader());
  if (match) return match[0].replace(/_/g, '-');
  return undefined;
}

export interface ResolveHostIdentOptions extends DetectPodUidOptions {
  hostnameFn?: () => string;
  pidFn?: () => number;
}

/** `service.instance.id`로 사용할 정규화된 호스트 식별자를 계산한다. */
export function resolveHostIdent(override?: string, options: ResolveHostIdentOptions = {}): string {
  if (override) return override;

  const podUid = detectPodUid(options);
  if (podUid) return `pod:${podUid}`;

  const { hostnameFn = osHostname, pidFn = () => process.pid } = options;
  return `host:${hostnameFn()}:${pidFn()}`;
}
