from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

import klaro_apm
from klaro_apm import tracing


def teardown_function(_fn):
    klaro_apm.shutdown()


def test_init_is_idempotent_and_returns_same_provider():
    exporter = InMemorySpanExporter()

    provider1 = klaro_apm.init(service_name="svc-a", exporter=exporter)
    provider2 = klaro_apm.init(service_name="svc-b", exporter=exporter)

    assert provider1 is provider2
    # 두 번째 init 호출은 무시되므로 첫 설정(svc-a)이 유지된다.
    assert provider1.resource.attributes["service.name"] == "svc-a"


def test_shutdown_clears_state_allowing_reinit():
    exporter = InMemorySpanExporter()
    klaro_apm.init(service_name="svc-a", exporter=exporter)

    klaro_apm.shutdown()

    assert tracing._state["provider"] is None
    assert tracing._state["processor"] is None

    provider2 = klaro_apm.init(service_name="svc-b", exporter=InMemorySpanExporter())
    assert provider2.resource.attributes["service.name"] == "svc-b"


def test_shutdown_without_init_is_a_noop():
    klaro_apm.shutdown()
    klaro_apm.shutdown()
