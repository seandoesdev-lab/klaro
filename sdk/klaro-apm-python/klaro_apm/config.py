"""klaro-apm 설정 로딩.

설정 키(환경변수) 이름과 의미는 `sdk/SDK_CONTRACT.md`가 단일 진실 공급원이며, 이 모듈은 그 계약의
Python 구현체다. 다른 언어(Node/Java) SDK도 동일한 환경변수 이름을 사용해야 한다.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field, replace
from typing import Dict, Optional

#: OTLP gRPC 메타데이터 헤더 이름 — org 관측 키 인증(HOW-4).
OBS_KEY_HEADER = "klaro-obs-key"

#: 로컬 개발 기본 Collector 엔드포인트(평문 gRPC).
DEFAULT_ENDPOINT = "localhost:4317"

ENV_OBS_KEY = "KLARO_OBS_KEY"
ENV_ENDPOINT = "KLARO_OBS_ENDPOINT"
ENV_INSECURE = "KLARO_OBS_INSECURE"
ENV_SERVICE_NAME = "KLARO_OBS_SERVICE_NAME"
ENV_HOST_IDENT = "KLARO_OBS_HOST_IDENT"
ENV_MAX_QUEUE_SIZE = "KLARO_OBS_MAX_QUEUE_SIZE"
ENV_SCHEDULE_DELAY_MILLIS = "KLARO_OBS_SCHEDULE_DELAY_MILLIS"
ENV_MAX_EXPORT_BATCH_SIZE = "KLARO_OBS_MAX_EXPORT_BATCH_SIZE"
ENV_CLIENT_CERTIFICATE_FILE = "KLARO_OBS_CLIENT_CERTIFICATE_FILE"
ENV_CLIENT_KEY_FILE = "KLARO_OBS_CLIENT_KEY_FILE"
ENV_ROOT_CERTIFICATE_FILE = "KLARO_OBS_ROOT_CERTIFICATE_FILE"


def _bool_env(env: Dict[str, str], name: str, default: bool) -> bool:
    raw = env.get(name)
    if raw is None:
        return default
    return raw.strip().lower() in ("1", "true", "yes", "on")


@dataclass(frozen=True)
class KlaroConfig:
    """klaro-apm 런타임 설정. 환경변수(`from_env`) 또는 `init()` 키워드 인자로 채워진다."""

    obs_key: Optional[str] = None
    endpoint: str = DEFAULT_ENDPOINT
    insecure: bool = True
    service_name: str = "unknown-service"
    host_ident_override: Optional[str] = None
    max_queue_size: int = 2048
    schedule_delay_millis: int = 5000
    max_export_batch_size: int = 512
    client_certificate_file: Optional[str] = None
    client_key_file: Optional[str] = None
    root_certificate_file: Optional[str] = None
    extra_resource_attributes: Dict[str, str] = field(default_factory=dict)

    @classmethod
    def from_env(cls, env: Optional[Dict[str, str]] = None, **overrides) -> "KlaroConfig":
        """환경변수로 기본값을 채우고, 명시적으로 전달된 키워드로 덮어쓴다(override 우선)."""
        env = env if env is not None else os.environ
        base = cls(
            obs_key=env.get(ENV_OBS_KEY),
            endpoint=env.get(ENV_ENDPOINT, DEFAULT_ENDPOINT),
            insecure=_bool_env(env, ENV_INSECURE, True),
            service_name=env.get(ENV_SERVICE_NAME, "unknown-service"),
            host_ident_override=env.get(ENV_HOST_IDENT),
            max_queue_size=int(env.get(ENV_MAX_QUEUE_SIZE, 2048)),
            schedule_delay_millis=int(env.get(ENV_SCHEDULE_DELAY_MILLIS, 5000)),
            max_export_batch_size=int(env.get(ENV_MAX_EXPORT_BATCH_SIZE, 512)),
            client_certificate_file=env.get(ENV_CLIENT_CERTIFICATE_FILE),
            client_key_file=env.get(ENV_CLIENT_KEY_FILE),
            root_certificate_file=env.get(ENV_ROOT_CERTIFICATE_FILE),
        )
        overrides = {k: v for k, v in overrides.items() if v is not None}
        return replace(base, **overrides) if overrides else base

    def otlp_headers(self) -> Dict[str, str]:
        """OTLP exporter에 첨부할 메타데이터 헤더. obs_key가 없으면 빈 dict(로컬 무인증 개발 허용)."""
        if not self.obs_key:
            return {}
        return {OBS_KEY_HEADER: self.obs_key}
