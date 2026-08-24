"""로그↔트레이스 상관키 부착 (SDK_CONTRACT.md §12).

klaro-apm은 트레이스만 보내는 thin wrapper다. 고객 애플리케이션의 로그는 이 SDK를 거치지 않고
자체 로거로 나가므로, 그 줄에 `trace_id`가 실리지 않으면 관측 플랫폼의 연계분석
(`GET /orgs/:orgId/obs/traces/:traceId/correlated`)이 스팬을 로그에 연결할 방법이 없다. 트레이스도
있고 로그도 있는데 둘을 잇는 키만 없는 상태가 되어, 화면에는 "이 트레이스에 해당하는 로그 없음"으로
보인다 — 실제로는 로그가 존재하는데도.

이 모듈은 그 한 가지 격차만 메운다: **현재 스팬 컨텍스트를 로그 레코드에 옮겨 적는다.** 로그를
전송하지도, 로거를 교체하지도 않는다(§12의 "SDK는 로그 파이프라인을 소유하지 않는다").

    import klaro_apm
    klaro_apm.init(service_name="checkout-api")
    klaro_apm.install_log_correlation()          # 루트 로거에 필터 부착

    logging.basicConfig(
        format="%(asctime)s %(levelname)s [trace_id=%(trace_id)s] %(message)s"
    )

포맷터에 `%(trace_id)s`를 쓰려면 스팬이 없는 로그 줄에도 그 속성이 **반드시** 있어야 한다(없으면
logging이 포맷 도중 KeyError를 낸다). 그래서 필터는 스팬이 없을 때도 빈 문자열을 채운다 — 관측을
켰다는 이유로 애플리케이션 로깅이 깨지는 일은 없어야 한다.
"""

from __future__ import annotations

import logging
from typing import Dict, Optional

from opentelemetry import trace

#: 로그 레코드/속성에 쓰는 키 이름. Loki의 OTLP 수신기가 로그 레코드의 트레이스 컨텍스트를
#: structured metadata에 넣을 때 쓰는 이름과 같아야 한다 — 백엔드의 상관 조회
#: (`services/observability/internal/explorer/logs.go`)가 이 이름으로 필터한다.
TRACE_ID_KEY = "trace_id"
SPAN_ID_KEY = "span_id"

#: 스팬이 없을 때 채우는 값. None이나 0으로 채워진 id가 아니라 빈 문자열인 이유: OTel의
#: INVALID_SPAN_CONTEXT는 trace_id가 전부 0인 "정상 값"처럼 보이므로, 그대로 찍히면 존재하지 않는
#: 트레이스를 조회하게 만든다.
EMPTY = ""


def correlation_fields() -> Dict[str, str]:
    """현재 스팬의 상관키를 반환한다. 유효한 스팬이 없으면 빈 dict.

    직접 구조화 로깅을 하는 코드(`logger.info(..., extra=...)`, structlog, JSON 로거 등)가 필터
    없이도 쓸 수 있도록 공개한다.
    """
    ctx = trace.get_current_span().get_span_context()
    if not ctx.is_valid:
        return {}
    return {
        TRACE_ID_KEY: trace.format_trace_id(ctx.trace_id),
        SPAN_ID_KEY: trace.format_span_id(ctx.span_id),
    }


class KlaroCorrelationFilter(logging.Filter):
    """모든 로그 레코드에 `trace_id`/`span_id` 속성을 붙이는 logging 필터.

    Handler나 Formatter가 아니라 Filter인 이유: 필터는 레코드가 핸들러에 도달하기 전에 한 번
    실행되고 레코드를 그대로 통과시키므로, 애플리케이션이 어떤 핸들러·포매터·서드파티 로깅 스택을
    쓰고 있어도 그것을 교체하지 않는다. 관측 SDK가 고객의 로깅 설정을 가져가는 것은 thin wrapper가
    아니다.
    """

    def filter(self, record: logging.LogRecord) -> bool:
        fields = correlation_fields()
        setattr(record, TRACE_ID_KEY, fields.get(TRACE_ID_KEY, EMPTY))
        setattr(record, SPAN_ID_KEY, fields.get(SPAN_ID_KEY, EMPTY))
        # 항상 True: 이 필터는 걸러내는 용도가 아니라 덧붙이는 용도다. False를 반환하면 관측을 켠
        # 대가로 고객의 로그가 사라진다.
        return True


def install_log_correlation(logger: Optional[logging.Logger] = None) -> KlaroCorrelationFilter:
    """`logger`(기본값: 루트 로거)에 상관 필터를 부착하고 그 필터를 반환한다.

    이미 부착되어 있으면 기존 필터를 그대로 반환한다 — `init()`처럼 idempotent해야 한다. 두 번
    부착되면 레코드마다 같은 값을 두 번 쓰는 낭비가 생긴다.
    """
    target = logger if logger is not None else logging.getLogger()
    for existing in target.filters:
        if isinstance(existing, KlaroCorrelationFilter):
            return existing
    installed = KlaroCorrelationFilter()
    target.addFilter(installed)
    return installed
