# klaro-apm

klaro 상시 관측 플랫폼(observability)용 Python SDK. OpenTelemetry 공식 SDK/자동계측을 그대로 의존성으로
쓰는 **thin wrapper**다 — 커스텀 프로토콜이나 자체 익스포터를 새로 만들지 않는다.

설정 키·리소스 속성 규칙 등 언어 공통 계약은 [`../SDK_CONTRACT.md`](../SDK_CONTRACT.md)를 참조.

## 설치

```bash
pip install klaro-apm
# FastAPI 자동계측을 쓰려면
pip install "klaro-apm[fastapi]"
```

## 빠른 시작

```python
import klaro_apm

klaro_apm.init(service_name="checkout-api")
```

환경변수로도 설정할 수 있다(코드 인자가 있으면 우선):

```bash
export KLARO_OBS_KEY="<org 관측 키>"
export KLARO_OBS_ENDPOINT="collector.klaro.internal:4317"
```

## FastAPI 연동 (한 줄)

```python
from fastapi import FastAPI
import klaro_apm

app = FastAPI()
klaro_apm.init_fastapi(app, service_name="checkout-api")
```

## 개발

```bash
pip install -e ".[test,fastapi]"
pytest
```
