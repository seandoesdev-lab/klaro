import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

import { resolveConfig } from '../src/config';
import { buildChannelCredentials, buildExporter } from '../src/tracing';

// 테스트 전용 자기서명 인증서/키 - 실제 서비스와 무관하게 이 테스트만을 위해 생성됨(비밀값 아님).
// grpc-js의 createSsl()은 생성 시점에 실제로 PEM을 파싱하므로(Python grpc와 달리), 형식이 유효한
// 더미 인증서가 필요하다.
const TEST_CERT = `-----BEGIN CERTIFICATE-----
MIIDEzCCAfugAwIBAgIUS5Xzwhl1vXdtplJJug5nZWdgrx4wDQYJKoZIhvcNAQEL
BQAwGTEXMBUGA1UEAwwOa2xhcm8tYXBtLXRlc3QwHhcNMjYwODIzMDY0NDMyWhcN
MzYwODIwMDY0NDMyWjAZMRcwFQYDVQQDDA5rbGFyby1hcG0tdGVzdDCCASIwDQYJ
KoZIhvcNAQEBBQADggEPADCCAQoCggEBALwVcHb8Bg9wwg+RarlQtcmYy6fsVFmV
X+qokmpJZ9qP62Yu6vWAR9CSsJ76iDX+bTcar7szJGphUrLP4E57rdtisHZqX8aj
2sv2ZN/E7ftF45DsWmF/LCNDMB27678gJshgU+s2SsHIVCV1zMUCuzk0fIwxpLga
MXmhajbmGXzJfTBpH807lED8YD6b7WcItSM7Yw6CleD6sWk2sMj0VgODBOxxIOxV
Fm8tiUbJzrzTXNSfCyYuJG7JhdBV8Ju0FbRCc9/xxS4B6Ij/ZoIL+S6flHBARvlS
ytgULNKOMOtQjn9mF14jY0FoMRLKOU7gxU4qPqMbF4hT9kpGLxF9ECsCAwEAAaNT
MFEwHQYDVR0OBBYEFDdfRAnsAm9GCHMdJLhVPC1cJkSQMB8GA1UdIwQYMBaAFDdf
RAnsAm9GCHMdJLhVPC1cJkSQMA8GA1UdEwEB/wQFMAMBAf8wDQYJKoZIhvcNAQEL
BQADggEBAC/4NAxpPPB7noup5FcIrnFhIxFys3f0ClDIal9eeKIGU275c5XCNJ6Z
a54zQfR9VlqO+EBawTzHcUC+FEfB6uvBY77vaA38OBKzKSu51oR9KF1kVggZ8+lp
dYQcyCr1znzzDwb8riLBmg2hgG+Y/yF0+Wi+vyOftsK1jqn2A6ieLUWD4GF9ix7f
3qq3BH1YP1qeCc7cn15c5aslE1LpxCj0YwaYWQYS1phSMVWTGtKJ5ZSKKuEJJmJi
fLgEClgR5c4fvayoGYwK6/Cu1BVpwOIcM4TTnOhiBH/sYVKJ2tsft080NpRCIebr
gUS3sAXRRZJCinoQe7j2HDgsn+7jbA4=
-----END CERTIFICATE-----
`;

const TEST_KEY = `-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC8FXB2/AYPcMIP
kWq5ULXJmMun7FRZlV/qqJJqSWfaj+tmLur1gEfQkrCe+og1/m03Gq+7MyRqYVKy
z+BOe63bYrB2al/Go9rL9mTfxO37ReOQ7FphfywjQzAdu+u/ICbIYFPrNkrByFQl
dczFArs5NHyMMaS4GjF5oWo25hl8yX0waR/NO5RA/GA+m+1nCLUjO2MOgpXg+rFp
NrDI9FYDgwTscSDsVRZvLYlGyc6801zUnwsmLiRuyYXQVfCbtBW0QnPf8cUuAeiI
/2aCC/kun5RwQEb5UsrYFCzSjjDrUI5/ZhdeI2NBaDESyjlO4MVOKj6jGxeIU/ZK
Ri8RfRArAgMBAAECggEABYgVV5YkeasM79/ptrcAamzit7yvzDZSz6mYzvZYoH1/
0wfjrGrqVLIKS8KsV/dGAZEP5350ShYdN0KJIzRRFNrhR4u9W4z0UAdYX8jGYHiU
GD+YX3A8yP1v94/nf5r2piCNro3Nq/W0KDUoW6r1cTblx+1kY7UC9ayhF54Q1GCo
G6QjhjPqN2m9TmjB4yyWn/XYDimfhmONRH3m8iW5VF9OmgjG/BPTxmssY58gOlWu
Xie2OAC3Jb6dPET5uSUycH1RFofVTM5zQY5gKYBNCMATe32w6wPlM82KAJNQV+/v
tFf/WcNa4uqfi9wgsMPr6ETx8e1nB94sWCWR1yCtkQKBgQDiIOHq1QRt7YH6dRuw
PG8jfwzwcS5jKfS4+dfa+TCvGVAHZ0VwHI0rUk4cp6EN+5NID7OwCRbOcEY6aAes
j4NHuqVLbOC8PAni4FX4qkmNCFPUSJhpn7ZaQNwCV0XWCNopEwgPnoegkqRyoCVz
+kbIu0vcIq8bxsfKsbTQZm4lFQKBgQDU7foe2jNgCLg+TvZPh0qtgiqIlsdtwb9I
3lK2/cZtFxRWSBWnCeVBJV4rggciRDJLqB+i699ysbQW7478NqR7ylz6iftlZd4H
o1+3an9pR/sQKyujTKfuCOjQrn8GurmTujbBv8fFCY5pFzAJGADwi+WyjzDcaVFE
he++lzYwPwKBgDkqhvKPF6eSu6FNqcpL/OzEWckPU+LN4IhC4UcCaERb6dd1TCCj
lyy0ifrEhfq69ujoz0xZf+KAj8CEPCxru4yOqur+g3IS24z3mcRbiGyXBlpMX/uT
3M3ER9pvpcAOTNjFbuxD75WwfNJdmhpP00U71Fm6ivpCro+XjVaCDqZhAoGAXsAj
HBWG5QYcToW+r3cJsRoKKUvHJL0hjKB5+DoHUUYC474h/Hm3zXx+YifzWrk0FFyU
71+8yAHxnH8vhmYeXYOYSliaSO3Clm2Jy0mVtti0DObY/UrAM3k9eJcdqXXv3J/x
e9gGYlS1TWhnFLTcvi3Sodl8Kain5DEhlRMepusCgYEAs7owU9hBtXM+OZMLmxHC
j6A39Bvg+0xWRL41dS9cP7m0YbJgw4BlVfFFJcA0BHMeSei0NZAfwIKUYFj/auWt
i0abuD2cpzMme7+V880tNBJE36uXhMXjr3Ek1w0GX/VBEhCB5P7rymqvVCq87Qi/
ntaC8EZHwrRz7vRcvtWIs9Y=
-----END PRIVATE KEY-----
`;

let tmpDir: string | undefined;

function writeTemp(name: string, content: string): string {
  if (!tmpDir) {
    tmpDir = mkdtempSync(join(tmpdir(), 'klaro-apm-mtls-'));
  }
  const filePath = join(tmpDir, name);
  writeFileSync(filePath, content);
  return filePath;
}

afterEach(() => {
  if (tmpDir) {
    rmSync(tmpDir, { recursive: true, force: true });
    tmpDir = undefined;
  }
});

describe('buildChannelCredentials', () => {
  it('falls back to insecure credentials without client cert files (default insecure=true)', () => {
    const config = resolveConfig({}, {});

    const credentials = buildChannelCredentials(config);

    expect(credentials._isSecure()).toBe(false);
  });

  it('builds ssl credentials from pem files when client cert + key are present', () => {
    const clientCert = writeTemp('client.crt', TEST_CERT);
    const clientKey = writeTemp('client.key', TEST_KEY);
    const rootCa = writeTemp('ca.crt', TEST_CERT);

    const config = resolveConfig(
      { clientCertificateFile: clientCert, clientKeyFile: clientKey, rootCertificateFile: rootCa },
      {},
    );

    const credentials = buildChannelCredentials(config);

    expect(credentials._isSecure()).toBe(true);
  });
});

describe('buildExporter', () => {
  it('builds without throwing when mTLS files are present', () => {
    const clientCert = writeTemp('client.crt', TEST_CERT);
    const clientKey = writeTemp('client.key', TEST_KEY);

    const config = resolveConfig(
      { endpoint: 'collector.internal:4317', clientCertificateFile: clientCert, clientKeyFile: clientKey },
      {},
    );

    expect(() => buildExporter(config)).not.toThrow();
  });
});
