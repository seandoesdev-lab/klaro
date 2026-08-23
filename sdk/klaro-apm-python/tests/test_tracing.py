from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

from klaro_apm.config import KlaroConfig, OBS_KEY_HEADER
from klaro_apm.tracing import build_exporter, build_tracer_provider


def test_build_exporter_attaches_klaro_obs_key_header():
    config = KlaroConfig.from_env(env={}, obs_key="secret-123", endpoint="localhost:4317")

    exporter = build_exporter(config)

    headers = dict(exporter._headers or ())
    assert headers.get(OBS_KEY_HEADER) == "secret-123"


def test_build_exporter_omits_header_when_no_obs_key():
    config = KlaroConfig.from_env(env={}, endpoint="localhost:4317")

    exporter = build_exporter(config)

    assert not exporter._headers


def test_build_tracer_provider_sets_service_name_and_host_ident_resource_attrs():
    config = KlaroConfig.from_env(
        env={},
        service_name="checkout-api",
        host_ident_override="host:web-1:4242",
    )
    exporter = InMemorySpanExporter()

    provider, _processor = build_tracer_provider(config, exporter=exporter)

    resource_attrs = provider.resource.attributes
    assert resource_attrs["service.name"] == "checkout-api"
    assert resource_attrs["service.instance.id"] == "host:web-1:4242"


def test_export_is_non_blocking_and_reaches_in_memory_exporter():
    """BatchSpanProcessor는 백그라운드 스레드에서 export한다 — span 종료 호출이 즉시 반환된다."""
    config = KlaroConfig.from_env(env={}, service_name="checkout-api")
    exporter = InMemorySpanExporter()

    provider, processor = build_tracer_provider(config, exporter=exporter)
    tracer = provider.get_tracer("test")

    with tracer.start_as_current_span("checkout"):
        pass

    processor.force_flush()

    finished = exporter.get_finished_spans()
    assert len(finished) == 1
    assert finished[0].name == "checkout"

    processor.shutdown()


def test_batch_processor_drops_oldest_when_queue_full():
    """큐 포화 시 drop-oldest(OBS 요구사항) — OTel BatchSpanProcessor의 deque(maxlen=N) 동작에 의존.

    schedule_delay_millis를 크게 잡아 백그라운드 flush가 끼어들기 전에 큐를 채우고, force_flush로
    남아있는 것만 내보내 가장 오래된 span들이 실제로 버려졌는지 검증한다.
    """
    config = KlaroConfig.from_env(
        env={},
        service_name="checkout-api",
        max_queue_size=5,
        schedule_delay_millis=60_000,
        max_export_batch_size=5,
    )
    exporter = InMemorySpanExporter()

    provider, processor = build_tracer_provider(config, exporter=exporter)
    tracer = provider.get_tracer("test")

    for i in range(10):
        with tracer.start_as_current_span(f"span-{i}"):
            pass

    processor.force_flush()
    finished_names = {span.name for span in exporter.get_finished_spans()}

    assert len(finished_names) == 5
    # 가장 오래된(span-0..span-4)은 버려지고, 가장 최근 것들만 남아야 drop-oldest다.
    assert finished_names == {f"span-{i}" for i in range(5, 10)}

    processor.shutdown()
