package io.klaro.apm;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class KlaroApmMtlsTest {

  // 테스트 전용 자기서명 인증서/키 - 이 테스트만을 위해 생성됨(비밀값 아님, 실제 서비스와 무관).
  private static final String TEST_CERT =
      "-----BEGIN CERTIFICATE-----\n"
          + "MIIDEzCCAfugAwIBAgIUS5Xzwhl1vXdtplJJug5nZWdgrx4wDQYJKoZIhvcNAQEL\n"
          + "BQAwGTEXMBUGA1UEAwwOa2xhcm8tYXBtLXRlc3QwHhcNMjYwODIzMDY0NDMyWhcN\n"
          + "MzYwODIwMDY0NDMyWjAZMRcwFQYDVQQDDA5rbGFyby1hcG0tdGVzdDCCASIwDQYJ\n"
          + "KoZIhvcNAQEBBQADggEPADCCAQoCggEBALwVcHb8Bg9wwg+RarlQtcmYy6fsVFmV\n"
          + "X+qokmpJZ9qP62Yu6vWAR9CSsJ76iDX+bTcar7szJGphUrLP4E57rdtisHZqX8aj\n"
          + "2sv2ZN/E7ftF45DsWmF/LCNDMB27678gJshgU+s2SsHIVCV1zMUCuzk0fIwxpLga\n"
          + "MXmhajbmGXzJfTBpH807lED8YD6b7WcItSM7Yw6CleD6sWk2sMj0VgODBOxxIOxV\n"
          + "Fm8tiUbJzrzTXNSfCyYuJG7JhdBV8Ju0FbRCc9/xxS4B6Ij/ZoIL+S6flHBARvlS\n"
          + "ytgULNKOMOtQjn9mF14jY0FoMRLKOU7gxU4qPqMbF4hT9kpGLxF9ECsCAwEAAaNT\n"
          + "MFEwHQYDVR0OBBYEFDdfRAnsAm9GCHMdJLhVPC1cJkSQMB8GA1UdIwQYMBaAFDdf\n"
          + "RAnsAm9GCHMdJLhVPC1cJkSQMA8GA1UdEwEB/wQFMAMBAf8wDQYJKoZIhvcNAQEL\n"
          + "BQADggEBAC/4NAxpPPB7noup5FcIrnFhIxFys3f0ClDIal9eeKIGU275c5XCNJ6Z\n"
          + "a54zQfR9VlqO+EBawTzHcUC+FEfB6uvBY77vaA38OBKzKSu51oR9KF1kVggZ8+lp\n"
          + "dYQcyCr1znzzDwb8riLBmg2hgG+Y/yF0+Wi+vyOftsK1jqn2A6ieLUWD4GF9ix7f\n"
          + "3qq3BH1YP1qeCc7cn15c5aslE1LpxCj0YwaYWQYS1phSMVWTGtKJ5ZSKKuEJJmJi\n"
          + "fLgEClgR5c4fvayoGYwK6/Cu1BVpwOIcM4TTnOhiBH/sYVKJ2tsft080NpRCIebr\n"
          + "gUS3sAXRRZJCinoQe7j2HDgsn+7jbA4=\n"
          + "-----END CERTIFICATE-----\n";

  private static final String TEST_KEY =
      "-----BEGIN PRIVATE KEY-----\n"
          + "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC8FXB2/AYPcMIP\n"
          + "kWq5ULXJmMun7FRZlV/qqJJqSWfaj+tmLur1gEfQkrCe+og1/m03Gq+7MyRqYVKy\n"
          + "z+BOe63bYrB2al/Go9rL9mTfxO37ReOQ7FphfywjQzAdu+u/ICbIYFPrNkrByFQl\n"
          + "dczFArs5NHyMMaS4GjF5oWo25hl8yX0waR/NO5RA/GA+m+1nCLUjO2MOgpXg+rFp\n"
          + "NrDI9FYDgwTscSDsVRZvLYlGyc6801zUnwsmLiRuyYXQVfCbtBW0QnPf8cUuAeiI\n"
          + "/2aCC/kun5RwQEb5UsrYFCzSjjDrUI5/ZhdeI2NBaDESyjlO4MVOKj6jGxeIU/ZK\n"
          + "Ri8RfRArAgMBAAECggEABYgVV5YkeasM79/ptrcAamzit7yvzDZSz6mYzvZYoH1/\n"
          + "0wfjrGrqVLIKS8KsV/dGAZEP5350ShYdN0KJIzRRFNrhR4u9W4z0UAdYX8jGYHiU\n"
          + "GD+YX3A8yP1v94/nf5r2piCNro3Nq/W0KDUoW6r1cTblx+1kY7UC9ayhF54Q1GCo\n"
          + "G6QjhjPqN2m9TmjB4yyWn/XYDimfhmONRH3m8iW5VF9OmgjG/BPTxmssY58gOlWu\n"
          + "Xie2OAC3Jb6dPET5uSUycH1RFofVTM5zQY5gKYBNCMATe32w6wPlM82KAJNQV+/v\n"
          + "tFf/WcNa4uqfi9wgsMPr6ETx8e1nB94sWCWR1yCtkQKBgQDiIOHq1QRt7YH6dRuw\n"
          + "PG8jfwzwcS5jKfS4+dfa+TCvGVAHZ0VwHI0rUk4cp6EN+5NID7OwCRbOcEY6aAes\n"
          + "j4NHuqVLbOC8PAni4FX4qkmNCFPUSJhpn7ZaQNwCV0XWCNopEwgPnoegkqRyoCVz\n"
          + "+kbIu0vcIq8bxsfKsbTQZm4lFQKBgQDU7foe2jNgCLg+TvZPh0qtgiqIlsdtwb9I\n"
          + "3lK2/cZtFxRWSBWnCeVBJV4rggciRDJLqB+i699ysbQW7478NqR7ylz6iftlZd4H\n"
          + "o1+3an9pR/sQKyujTKfuCOjQrn8GurmTujbBv8fFCY5pFzAJGADwi+WyjzDcaVFE\n"
          + "he++lzYwPwKBgDkqhvKPF6eSu6FNqcpL/OzEWckPU+LN4IhC4UcCaERb6dd1TCCj\n"
          + "lyy0ifrEhfq69ujoz0xZf+KAj8CEPCxru4yOqur+g3IS24z3mcRbiGyXBlpMX/uT\n"
          + "3M3ER9pvpcAOTNjFbuxD75WwfNJdmhpP00U71Fm6ivpCro+XjVaCDqZhAoGAXsAj\n"
          + "HBWG5QYcToW+r3cJsRoKKUvHJL0hjKB5+DoHUUYC474h/Hm3zXx+YifzWrk0FFyU\n"
          + "71+8yAHxnH8vhmYeXYOYSliaSO3Clm2Jy0mVtti0DObY/UrAM3k9eJcdqXXv3J/x\n"
          + "e9gGYlS1TWhnFLTcvi3Sodl8Kain5DEhlRMepusCgYEAs7owU9hBtXM+OZMLmxHC\n"
          + "j6A39Bvg+0xWRL41dS9cP7m0YbJgw4BlVfFFJcA0BHMeSei0NZAfwIKUYFj/auWt\n"
          + "i0abuD2cpzMme7+V880tNBJE36uXhMXjr3Ek1w0GX/VBEhCB5P7rymqvVCq87Qi/\n"
          + "ntaC8EZHwrRz7vRcvtWIs9Y=\n"
          + "-----END PRIVATE KEY-----\n";

  @TempDir Path tempDir;

  @Test
  void normalizeEndpointAddsHttpWhenInsecure() {
    assertEquals("http://localhost:4317", KlaroApm.normalizeEndpoint("localhost:4317", false));
  }

  @Test
  void normalizeEndpointAddsHttpsWhenTls() {
    assertEquals("https://collector.internal:4317", KlaroApm.normalizeEndpoint("collector.internal:4317", true));
  }

  @Test
  void normalizeEndpointLeavesExplicitSchemeAlone() {
    assertEquals("https://collector.internal:4317", KlaroApm.normalizeEndpoint("https://collector.internal:4317", false));
  }

  @Test
  void buildExporterBuildsWithoutThrowingWhenMtlsFilesArePresent() throws IOException {
    Path clientCert = writeTemp("client.crt", TEST_CERT);
    Path clientKey = writeTemp("client.key", TEST_KEY);
    Path rootCa = writeTemp("ca.crt", TEST_CERT);

    KlaroConfig config =
        KlaroConfig.resolve(
            KlaroConfig.builder()
                .endpoint("collector.internal:4317")
                .clientCertificateFile(clientCert.toString())
                .clientKeyFile(clientKey.toString())
                .rootCertificateFile(rootCa.toString()),
            Map.of());

    assertDoesNotThrow(() -> KlaroApm.buildExporter(config));
  }

  @Test
  void buildExporterBuildsWithoutThrowingWhenNoMtlsFilesArePresent() {
    KlaroConfig config = KlaroConfig.resolve(KlaroConfig.builder(), Map.of());

    assertDoesNotThrow(() -> KlaroApm.buildExporter(config));
  }

  private Path writeTemp(String name, String content) throws IOException {
    Path path = tempDir.resolve(name);
    Files.writeString(path, content, StandardCharsets.UTF_8);
    return path;
  }
}
