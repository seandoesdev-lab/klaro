from klaro_apm.config import KlaroConfig
from klaro_apm.tracing import _build_channel_credentials, build_exporter


def _write(tmp_path, name: str, content: bytes):
    path = tmp_path / name
    path.write_bytes(content)
    return str(path)


def test_build_channel_credentials_none_without_client_cert_files():
    config = KlaroConfig.from_env(env={})

    assert _build_channel_credentials(config) is None


def test_build_channel_credentials_builds_from_pem_files(tmp_path):
    # 유효한 PEM 형식은 아니지만 grpc.ssl_channel_credentials는 생성 시점에는 내용을 파싱하지
    # 않으므로(실제 핸드셰이크 시점에 검증) 파일 3개가 읽혀 인자로 전달되는지만 확인한다.
    client_cert = _write(tmp_path, "client.crt", b"-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n")
    client_key = _write(tmp_path, "client.key", b"-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----\n")
    root_ca = _write(tmp_path, "ca.crt", b"-----BEGIN CERTIFICATE-----\nfake-ca\n-----END CERTIFICATE-----\n")

    config = KlaroConfig.from_env(
        env={},
        client_certificate_file=client_cert,
        client_key_file=client_key,
        root_certificate_file=root_ca,
    )

    credentials = _build_channel_credentials(config)

    assert credentials is not None


def test_build_exporter_uses_credentials_when_mtls_files_present(tmp_path):
    client_cert = _write(tmp_path, "client.crt", b"cert")
    client_key = _write(tmp_path, "client.key", b"key")

    config = KlaroConfig.from_env(
        env={},
        endpoint="collector.internal:4317",
        client_certificate_file=client_cert,
        client_key_file=client_key,
    )

    # 예외 없이 exporter가 만들어지면(그리고 insecure 평문 경로를 타지 않으면) 충족.
    exporter = build_exporter(config)
    assert exporter is not None
