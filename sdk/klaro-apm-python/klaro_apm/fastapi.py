"""FastAPI 연동 헬퍼 — `klaro_apm.init_fastapi(app)` 한 줄로 초기화 + 자동계측."""

from __future__ import annotations

from typing import Any, Dict, Optional

from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SpanExporter

from . import tracing


def init_fastapi(
    app: Any,
    *,
    service_name: Optional[str] = None,
    obs_key: Optional[str] = None,
    endpoint: Optional[str] = None,
    insecure: Optional[bool] = None,
    host_ident: Optional[str] = None,
    extra_resource_attributes: Optional[Dict[str, str]] = None,
    exporter: Optional[SpanExporter] = None,
) -> TracerProvider:
    """klaro-apm을 초기화하고 FastAPI 앱에 OTel 자동계측을 붙인다.

    사용법::

        app = FastAPI()
        klaro_apm.init_fastapi(app, service_name="checkout-api")

    `opentelemetry-instrumentation-fastapi`가 설치되어 있어야 한다
    (``pip install klaro-apm[fastapi]``).
    """
    try:
        from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
    except ImportError as exc:  # pragma: no cover - 환경별 optional dependency 분기
        raise RuntimeError(
            "FastAPI 자동계측에는 'opentelemetry-instrumentation-fastapi'가 필요합니다. "
            "pip install klaro-apm[fastapi] 로 설치하세요."
        ) from exc

    provider = tracing.init(
        service_name=service_name,
        obs_key=obs_key,
        endpoint=endpoint,
        insecure=insecure,
        host_ident=host_ident,
        extra_resource_attributes=extra_resource_attributes,
        exporter=exporter,
    )
    FastAPIInstrumentor.instrument_app(app, tracer_provider=provider)
    return provider
