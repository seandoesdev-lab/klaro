package io.klaro.apm;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

import java.util.Map;
import org.junit.jupiter.api.Test;

class HostIdentTest {

  private static final String CGROUP_WITH_POD_UID =
      "12:memory:/kubepods.slice/kubepods-burstable.slice/"
          + "kubepods-burstable-pod1a2b3c4d_5e6f_7890_abcd_1234567890ab.slice/"
          + "docker-abc123.scope\n";
  private static final String CGROUP_WITHOUT_POD_UID = "12:memory:/docker/abc123def456\n";

  @Test
  void resolveReturnsExplicitOverride() {
    assertEquals("custom-ident", HostIdent.resolve("custom-ident", () -> "", Map.of(), () -> "web-1", 4242));
  }

  @Test
  void resolveUsesPodUidWhenAvailable() {
    String result = HostIdent.resolve(null, () -> CGROUP_WITH_POD_UID, Map.of(), () -> "web-1", 4242);

    assertEquals("pod:1a2b3c4d-5e6f-7890-abcd-1234567890ab", result);
  }

  @Test
  void resolveFallsBackToHostnameAndPid() {
    String result = HostIdent.resolve(null, () -> CGROUP_WITHOUT_POD_UID, Map.of(), () -> "web-1", 4242);

    assertEquals("host:web-1:4242", result);
  }

  @Test
  void detectPodUidPrefersKlaroEnvVar() {
    String podUid = HostIdent.detectPodUid(() -> "", Map.of("KLARO_POD_UID", "  pod-abc  "));

    assertEquals("pod-abc", podUid);
  }

  @Test
  void detectPodUidFallsBackToStandardPodUidEnvVar() {
    String podUid = HostIdent.detectPodUid(() -> "", Map.of("POD_UID", "pod-xyz"));

    assertEquals("pod-xyz", podUid);
  }

  @Test
  void detectPodUidExtractsUidFromCgroupPath() {
    String podUid = HostIdent.detectPodUid(() -> CGROUP_WITH_POD_UID, Map.of());

    assertEquals("1a2b3c4d-5e6f-7890-abcd-1234567890ab", podUid);
  }

  @Test
  void detectPodUidReturnsNullWhenNotContainerized() {
    String podUid = HostIdent.detectPodUid(() -> CGROUP_WITHOUT_POD_UID, Map.of());

    assertNull(podUid);
  }
}
