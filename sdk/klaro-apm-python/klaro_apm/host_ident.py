"""`service.instance.id` 정규화(HOW-4 host_ident 정합).

우선순위: (1) 명시적 override(코드 인자 또는 KLARO_OBS_HOST_IDENT) → (2) 컨테이너 pod UID
(Downward API 환경변수 또는 cgroup 경로에서 추출) → (3) hostname+PID 폴백.
"""

from __future__ import annotations

import os
import re
import socket
from typing import Callable, Dict, Optional

#: k8s Downward API로 주입 가능한 pod UID 환경변수(우선순위 순).
_POD_UID_ENV_VARS = ("KLARO_POD_UID", "POD_UID")

#: cgroup 경로(`/proc/self/cgroup`)에 나타나는 kubepods pod UID 패턴(하이픈/언더스코어 혼용 허용).
_CGROUP_UID_PATTERN = re.compile(
    r"[0-9a-fA-F]{8}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{12}"
)


def _read_cgroup(path: str = "/proc/self/cgroup") -> str:
    try:
        with open(path, "r", encoding="utf-8") as fh:
            return fh.read()
    except OSError:
        return ""


def detect_pod_uid(
    cgroup_reader: Callable[[], str] = _read_cgroup,
    env: Optional[Dict[str, str]] = None,
) -> Optional[str]:
    """컨테이너 pod UID를 탐지한다. 못 찾으면 None(호출측이 hostname+PID로 폴백)."""
    env = env if env is not None else os.environ
    for var in _POD_UID_ENV_VARS:
        value = env.get(var)
        if value:
            return value.strip()

    match = _CGROUP_UID_PATTERN.search(cgroup_reader())
    if match:
        return match.group(0).replace("_", "-")
    return None


def resolve_host_ident(
    override: Optional[str] = None,
    *,
    cgroup_reader: Callable[[], str] = _read_cgroup,
    env: Optional[Dict[str, str]] = None,
    hostname_fn: Callable[[], str] = socket.gethostname,
    pid_fn: Callable[[], int] = os.getpid,
) -> str:
    """`service.instance.id`로 사용할 정규화된 호스트 식별자를 계산한다."""
    if override:
        return override

    pod_uid = detect_pod_uid(cgroup_reader=cgroup_reader, env=env)
    if pod_uid:
        return f"pod:{pod_uid}"

    return f"host:{hostname_fn()}:{pid_fn()}"
