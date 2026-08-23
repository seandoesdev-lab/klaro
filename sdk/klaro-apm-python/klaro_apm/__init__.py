"""klaro-apm — klaro 상시 관측 플랫폼용 OpenTelemetry thin wrapper SDK.

빠른 시작::

    import klaro_apm
    klaro_apm.init(service_name="checkout-api")

FastAPI::

    from fastapi import FastAPI
    import klaro_apm

    app = FastAPI()
    klaro_apm.init_fastapi(app, service_name="checkout-api")

설정 키·리소스 속성 규칙은 `sdk/SDK_CONTRACT.md`를 참조.
"""

from ._version import __version__
from .config import KlaroConfig
from .fastapi import init_fastapi
from .tracing import init, shutdown

__all__ = [
    "__version__",
    "KlaroConfig",
    "init",
    "shutdown",
    "init_fastapi",
]
