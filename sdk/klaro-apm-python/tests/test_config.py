from klaro_apm.config import KlaroConfig, DEFAULT_ENDPOINT, OBS_KEY_HEADER


def test_from_env_defaults_when_no_env_set():
    config = KlaroConfig.from_env(env={})

    assert config.obs_key is None
    assert config.endpoint == DEFAULT_ENDPOINT
    assert config.insecure is True
    assert config.service_name == "unknown-service"
    assert config.otlp_headers() == {}


def test_from_env_reads_klaro_obs_key_and_builds_header():
    config = KlaroConfig.from_env(env={"KLARO_OBS_KEY": "secret-123"})

    assert config.obs_key == "secret-123"
    assert config.otlp_headers() == {OBS_KEY_HEADER: "secret-123"}


def test_from_env_reads_endpoint_and_service_name():
    config = KlaroConfig.from_env(
        env={
            "KLARO_OBS_ENDPOINT": "collector.internal:4317",
            "KLARO_OBS_SERVICE_NAME": "checkout-api",
            "KLARO_OBS_INSECURE": "false",
        }
    )

    assert config.endpoint == "collector.internal:4317"
    assert config.service_name == "checkout-api"
    assert config.insecure is False


def test_explicit_overrides_win_over_env():
    config = KlaroConfig.from_env(
        env={"KLARO_OBS_SERVICE_NAME": "from-env"},
        service_name="from-init-arg",
    )

    assert config.service_name == "from-init-arg"


def test_none_overrides_are_ignored():
    config = KlaroConfig.from_env(env={"KLARO_OBS_SERVICE_NAME": "from-env"}, service_name=None)

    assert config.service_name == "from-env"
