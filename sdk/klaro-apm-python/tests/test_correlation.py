"""상관키 부착(SDK_CONTRACT §12) 테스트.

여기서 검증하는 것은 "값이 있으면 붙는다"가 아니라 그 반대쪽 두 경우다: 스팬이 없을 때 포맷터를
깨뜨리지 않는지, 그리고 존재하지 않는 트레이스를 가리키는 0 id를 만들어내지 않는지. 상관 조회는
이 키로 Loki를 필터하므로, 잘못된 키는 조용히 빈 결과가 된다.
"""

from __future__ import annotations

import logging

from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider

from klaro_apm.correlation import (
    SPAN_ID_KEY,
    TRACE_ID_KEY,
    KlaroCorrelationFilter,
    correlation_fields,
    install_log_correlation,
)


def test_correlation_fields_is_empty_without_a_span() -> None:
    assert correlation_fields() == {}


def test_correlation_fields_carries_the_active_span() -> None:
    tracer = TracerProvider().get_tracer("test")
    with tracer.start_as_current_span("checkout") as span:
        fields = correlation_fields()
        ctx = span.get_span_context()

    # 백엔드가 기대하는 형식: 소문자 16진수, trace 32자 / span 16자.
    assert fields[TRACE_ID_KEY] == trace.format_trace_id(ctx.trace_id)
    assert fields[SPAN_ID_KEY] == trace.format_span_id(ctx.span_id)
    assert len(fields[TRACE_ID_KEY]) == 32
    assert len(fields[SPAN_ID_KEY]) == 16


def _record() -> logging.LogRecord:
    return logging.LogRecord("t", logging.INFO, __file__, 1, "hello", None, None)


def test_filter_always_sets_the_attributes_so_a_formatter_cannot_break() -> None:
    """스팬이 없는 줄에도 속성이 있어야 %(trace_id)s 포맷이 KeyError를 내지 않는다."""
    record = _record()
    assert KlaroCorrelationFilter().filter(record) is True
    assert getattr(record, TRACE_ID_KEY) == ""
    assert getattr(record, SPAN_ID_KEY) == ""

    formatted = logging.Formatter("[%(trace_id)s] %(message)s").format(record)
    assert formatted == "[] hello"


def test_filter_stamps_the_active_span() -> None:
    tracer = TracerProvider().get_tracer("test")
    record = _record()
    with tracer.start_as_current_span("checkout"):
        KlaroCorrelationFilter().filter(record)

    stamped = getattr(record, TRACE_ID_KEY)
    assert len(stamped) == 32
    # 전부 0인 id는 존재하지 않는 트레이스를 조회하게 만든다.
    assert set(stamped) != {"0"}


def test_install_is_idempotent() -> None:
    logger = logging.getLogger("klaro_apm.test.correlation")
    logger.filters.clear()

    first = install_log_correlation(logger)
    second = install_log_correlation(logger)

    assert first is second
    assert len([f for f in logger.filters if isinstance(f, KlaroCorrelationFilter)]) == 1


def test_install_defaults_to_the_root_logger() -> None:
    root = logging.getLogger()
    installed = install_log_correlation()
    try:
        assert installed in root.filters
    finally:
        root.removeFilter(installed)
