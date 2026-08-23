"""klaro-apm 초기화 핵심.

thin wrapper 원칙(HOW-11): 여기서는 OTel 공식 SDK가 이미 제공하는 동작을 재구현하지 않는다.

- **비블로킹 배치 익스포트**: `BatchSpanProcessor`가 백그라운드 스레드에서 export한다(호출 스레드를
  막지 않음).
- **큐 포화 시 drop-oldest**: `BatchSpanProcessor`의 내부 큐는 `collections.deque(maxlen=...)`이라
  가득 찬 상태에서 새 span을 append하면 OS가 자동으로 가장 오래된 항목을 버린다(표준 라이브러리 동작).
  이 SDK는 별도 드롭 로직을 구현하지 않고 이 동작에 의존한다 — 회귀 감지를 위한 테스트는
  `tests/test_tracing.py::test_batch_processor_drops_oldest_when_queue_full`.
- **연결 실패 시 조용한 재시도**: `OTLPSpanExporter.export()`는 gRPC 예외를 내부에서 처리해
  `SpanExportResult.FAILURE`를 반환할 뿐 예외를 던지지 않는다. `BatchSpanProcessor`도 워커 스레드에서
  export를 호출하므로 실패가 호출자(고객 앱) 스레드로 전파되지 않는다. 다음 배치 주기에 다시
  시도되므로 재시도는 스케줄 주기(`schedule_delay_millis`)에 자연히 포함된다.
"""

from __future__ import annotations

import atexit
import logging
import threading
from typing import Any, Dict, Optional

from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SpanExporter, BatchSpanProcessor
from opentelemetry.sdk.trace.sampling import ALWAYS_ON, ParentBased

from .config import KlaroConfig
from .host_ident import resolve_host_ident

_logger = logging.getLogger("klaro_apm")

_lock = threading.Lock()
_state: Dict[str, Any] = {"provider": None, "processor": None}


def build_resource(config: KlaroConfig) -> Resource:
    """설정으로부터 OTel Resource(서비스명 + 정규화된 host_ident)를 만든다."""
    host_ident = resolve_host_ident(config.host_ident_override)
    attributes: Dict[str, str] = {
        "service.name": config.service_name,
        "service.instance.id": host_ident,
    }
    attributes.update(config.extra_resource_attributes)
    return Resource.create(attributes)


def _read_file_bytes(path: Optional[str]) -> Optional[bytes]:
    if not path:
        return None
    with open(path, "rb") as fh:
        return fh.read()


def _build_channel_credentials(config: KlaroConfig):
    """mTLS 클라이언트 인증서가 설정된 경우에만 gRPC ChannelCredentials를 만든다.

    옵션 노출만 한다(HOW-11) — 로컬 개발은 인증서 없이 `insecure=True` 평문을 그대로 허용한다.
    """
    if not (config.client_certificate_file and config.client_key_file):
        return None

    import grpc  # opentelemetry-exporter-otlp-proto-grpc의 전이 의존성

    return grpc.ssl_channel_credentials(
        root_certificates=_read_file_bytes(config.root_certificate_file),
        private_key=_read_file_bytes(config.client_key_file),
        certificate_chain=_read_file_bytes(config.client_certificate_file),
    )


def build_exporter(config: KlaroConfig) -> OTLPSpanExporter:
    """`klaro-obs-key` 헤더가 첨부된 OTLP/gRPC exporter를 만든다.

    mTLS는 옵션으로만 노출한다(로컬은 `insecure=True` 평문 허용, Collector가 배포에서 mTLS를
    강제한다 — HOW-11/CLAUDE.md 통신 규약).
    """
    headers = config.otlp_headers() or None
    credentials = _build_channel_credentials(config)
    if credentials is not None:
        return OTLPSpanExporter(endpoint=config.endpoint, credentials=credentials, headers=headers)
    return OTLPSpanExporter(endpoint=config.endpoint, insecure=config.insecure, headers=headers)


def build_tracer_provider(
    config: KlaroConfig,
    exporter: Optional[SpanExporter] = None,
) -> tuple[TracerProvider, BatchSpanProcessor]:
    """TracerProvider + BatchSpanProcessor를 구성한다. 테스트에서 in-memory exporter 주입용으로 분리."""
    resource = build_resource(config)

    # tail-based 샘플링은 Collector(tailsampling processor)가 전체 트레이스를 보고 결정한다.
    # SDK는 head 샘플링으로 데이터를 먼저 버리지 않고 항상 전송한다(ParentBased(ALWAYS_ON)).
    # APM-01(≤2% 오버헤드)은 비동기 배치 export로 달성하며, span 드롭이 아니다.
    provider = TracerProvider(resource=resource, sampler=ParentBased(ALWAYS_ON))

    span_exporter = exporter if exporter is not None else build_exporter(config)
    processor = BatchSpanProcessor(
        span_exporter,
        max_queue_size=config.max_queue_size,
        schedule_delay_millis=config.schedule_delay_millis,
        max_export_batch_size=config.max_export_batch_size,
    )
    provider.add_span_processor(processor)
    return provider, processor


def init(
    *,
    service_name: Optional[str] = None,
    obs_key: Optional[str] = None,
    endpoint: Optional[str] = None,
    insecure: Optional[bool] = None,
    host_ident: Optional[str] = None,
    extra_resource_attributes: Optional[Dict[str, str]] = None,
    exporter: Optional[SpanExporter] = None,
) -> TracerProvider:
    """klaro-apm을 초기화하고 전역 TracerProvider를 설정한다(idempotent).

    설정 우선순위: 이 함수의 키워드 인자 > 환경변수(`KLARO_OBS_*`) > 기본값.
    이미 초기화된 경우 기존 TracerProvider를 그대로 반환한다(중복 초기화로 인한 프로세서
    중복 방지).
    """
    with _lock:
        if _state["provider"] is not None:
            return _state["provider"]

        config = KlaroConfig.from_env(
            service_name=service_name,
            obs_key=obs_key,
            endpoint=endpoint,
            insecure=insecure,
            host_ident_override=host_ident,
            extra_resource_attributes=extra_resource_attributes,
        )

        provider, processor = build_tracer_provider(config, exporter=exporter)
        trace.set_tracer_provider(provider)

        _state["provider"] = provider
        _state["processor"] = processor
        atexit.register(shutdown)
        return provider


def shutdown(timeout_millis: int = 5000) -> None:
    """배치 프로세서를 flush하고 종료한다. 실패해도 예외를 호출자에게 전파하지 않는다."""
    with _lock:
        processor = _state.get("processor")
        if processor is None:
            return
        try:
            processor.shutdown()
        except Exception:  # pragma: no cover - defensive: 고객 앱에 예외 전파 금지(APM-01)
            _logger.debug("klaro-apm: shutdown 중 예외 억제", exc_info=True)
        _state["provider"] = None
        _state["processor"] = None
