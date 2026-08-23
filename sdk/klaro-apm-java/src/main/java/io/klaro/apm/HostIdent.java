package io.klaro.apm;

import java.io.IOException;
import java.net.InetAddress;
import java.net.UnknownHostException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;
import java.util.function.Supplier;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import javax.annotation.Nullable;

/**
 * {@code service.instance.id} 정규화(HOW-4 host_ident 정합).
 *
 * 우선순위: (1) 명시적 override(코드 인자 또는 KLARO_OBS_HOST_IDENT) -&gt; (2) 컨테이너 pod UID
 * (Downward API 환경변수 또는 cgroup 경로에서 추출) -&gt; (3) hostname+PID 폴백.
 */
public final class HostIdent {

  /** k8s Downward API로 주입 가능한 pod UID 환경변수(우선순위 순). */
  private static final String[] POD_UID_ENV_VARS = {"KLARO_POD_UID", "POD_UID"};

  /** cgroup 경로(/proc/self/cgroup)에 나타나는 kubepods pod UID 패턴(하이픈/언더스코어 혼용 허용). */
  private static final Pattern CGROUP_UID_PATTERN =
      Pattern.compile(
          "[0-9a-fA-F]{8}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{12}");

  private HostIdent() {}

  /** 기본 cgroup 파일(/proc/self/cgroup)을 읽는다. 읽을 수 없으면 빈 문자열. */
  public static String readCgroup() {
    return readCgroup(Path.of("/proc/self/cgroup"));
  }

  static String readCgroup(Path path) {
    try {
      return Files.readString(path, StandardCharsets.UTF_8);
    } catch (IOException e) {
      return "";
    }
  }

  /** 컨테이너 pod UID를 탐지한다. 못 찾으면 null(호출측이 hostname+PID로 폴백). */
  @Nullable
  public static String detectPodUid(Supplier<String> cgroupReader, Map<String, String> env) {
    for (String key : POD_UID_ENV_VARS) {
      String value = env.get(key);
      if (value != null && !value.isBlank()) {
        return value.trim();
      }
    }

    Matcher matcher = CGROUP_UID_PATTERN.matcher(cgroupReader.get());
    if (matcher.find()) {
      return matcher.group().replace('_', '-');
    }
    return null;
  }

  /** 기본 환경(System.getenv(), 실제 cgroup 파일, 실제 hostname/PID)으로 host_ident를 계산한다. */
  public static String resolve(@Nullable String override) {
    return resolve(override, HostIdent::readCgroup, System.getenv(), HostIdent::defaultHostname, ProcessHandle.current().pid());
  }

  /** {@code service.instance.id}로 사용할 정규화된 호스트 식별자를 계산한다(테스트 주입용 오버로드). */
  public static String resolve(
      @Nullable String override,
      Supplier<String> cgroupReader,
      Map<String, String> env,
      Supplier<String> hostnameFn,
      long pid) {
    if (override != null && !override.isEmpty()) {
      return override;
    }

    String podUid = detectPodUid(cgroupReader, env);
    if (podUid != null) {
      return "pod:" + podUid;
    }

    return "host:" + hostnameFn.get() + ":" + pid;
  }

  private static String defaultHostname() {
    try {
      return InetAddress.getLocalHost().getHostName();
    } catch (UnknownHostException e) {
      return "unknown-host";
    }
  }
}
