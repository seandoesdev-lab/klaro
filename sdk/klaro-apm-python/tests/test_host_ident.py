from klaro_apm.host_ident import detect_pod_uid, resolve_host_ident

CGROUP_WITH_POD_UID = (
    "12:memory:/kubepods.slice/kubepods-burstable.slice/"
    "kubepods-burstable-pod1a2b3c4d_5e6f_7890_abcd_1234567890ab.slice/"
    "docker-abc123.scope\n"
)
CGROUP_WITHOUT_POD_UID = "12:memory:/docker/abc123def456\n"


def test_resolve_host_ident_returns_explicit_override():
    result = resolve_host_ident("custom-ident")

    assert result == "custom-ident"


def test_detect_pod_uid_prefers_klaro_env_var():
    pod_uid = detect_pod_uid(cgroup_reader=lambda: "", env={"KLARO_POD_UID": "  pod-abc  "})

    assert pod_uid == "pod-abc"


def test_detect_pod_uid_falls_back_to_standard_pod_uid_env_var():
    pod_uid = detect_pod_uid(cgroup_reader=lambda: "", env={"POD_UID": "pod-xyz"})

    assert pod_uid == "pod-xyz"


def test_detect_pod_uid_extracts_uid_from_cgroup_path():
    pod_uid = detect_pod_uid(cgroup_reader=lambda: CGROUP_WITH_POD_UID, env={})

    assert pod_uid == "1a2b3c4d-5e6f-7890-abcd-1234567890ab"


def test_detect_pod_uid_returns_none_when_not_containerized():
    pod_uid = detect_pod_uid(cgroup_reader=lambda: CGROUP_WITHOUT_POD_UID, env={})

    assert pod_uid is None


def test_resolve_host_ident_uses_pod_uid_when_available():
    result = resolve_host_ident(
        None,
        cgroup_reader=lambda: CGROUP_WITH_POD_UID,
        env={},
    )

    assert result == "pod:1a2b3c4d-5e6f-7890-abcd-1234567890ab"


def test_resolve_host_ident_falls_back_to_hostname_and_pid():
    result = resolve_host_ident(
        None,
        cgroup_reader=lambda: CGROUP_WITHOUT_POD_UID,
        env={},
        hostname_fn=lambda: "web-1",
        pid_fn=lambda: 4242,
    )

    assert result == "host:web-1:4242"
