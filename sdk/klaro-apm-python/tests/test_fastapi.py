import builtins

import pytest

from klaro_apm.fastapi import init_fastapi


def test_init_fastapi_raises_helpful_error_without_extra(monkeypatch):
    real_import = builtins.__import__

    def fake_import(name, *args, **kwargs):
        if name == "opentelemetry.instrumentation.fastapi":
            raise ImportError("simulated: extra not installed")
        return real_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", fake_import)

    with pytest.raises(RuntimeError, match="klaro-apm\\[fastapi\\]"):
        init_fastapi(app=object())
