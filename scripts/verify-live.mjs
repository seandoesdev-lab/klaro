#!/usr/bin/env node
/**
 * Prove the browser-shaped path end to end: WebSocket auth, ingest, read-back.
 *
 * It runs the checks in the one order that actually tests them, because each
 * depends on the previous one having really happened:
 *
 *   1. open the live socket with the credential in the subprotocol list, the way
 *      a browser must send it, and assert the server echoed the subprotocol. The
 *      echo is what a browser requires of a handshake it started with an offer;
 *      without it a page authenticates and is then disconnected, which reads as
 *      a server fault rather than a missing response header.
 *   2. push one synthetic OTLP metric through the Collector *while that socket
 *      is open*, and wait for it to arrive as a live frame. Injecting first and
 *      subscribing after would prove nothing: the live path is pub/sub, so a
 *      late subscriber sees no history and a pass would only mean the ordering
 *      happened to be lucky.
 *   3. read the same metric back through the Explorer REST route, which is the
 *      other half of the dashboard. It polls, because the sample has to reach
 *      VictoriaMetrics and become visible there, and a single immediate query
 *      would be a race dressed up as a failure.
 *
 * Node's global WebSocket and fetch do the work, so there is nothing to install
 * ([COST-05]: no new dependency, paid or otherwise).
 *
 * Configuration comes from the environment, so the script has no idea which
 * stack it is pointed at:
 *   OBSPLANE_URL  OTLP_URL  KLARO_ORG_ID  KLARO_OBS_TOKEN  KLARO_OBS_KEY
 */

const env = (name, fallback) => process.env[name] ?? fallback;

const OBSPLANE = env("OBSPLANE_URL", "http://localhost:8090").replace(/\/+$/, "");
const OTLP = env("OTLP_URL", "http://localhost:4318").replace(/\/+$/, "");
const ORG = env("KLARO_ORG_ID", "");
const TOKEN = env("KLARO_OBS_TOKEN", "");
const KEY = env("KLARO_OBS_KEY", "");
const BEARER_SUBPROTOCOL = "klaro-bearer";

/** Unique per run, so a re-run cannot pass on samples an earlier run wrote. */
const METRIC = `klaro_e2e_gauge_${process.pid}`;
const VALUE = 42.5;

const WS_OPEN_TIMEOUT_MS = 15_000;
const FRAME_TIMEOUT_MS = 30_000;
const REST_TIMEOUT_MS = 90_000;
const REST_POLL_MS = 3_000;

const results = [];
function record(step, ok, detail) {
  results.push({ step, ok, detail });
  console.log(`${ok ? "PASS" : "FAIL"}  ${step}${detail ? " - " + detail : ""}`);
}

function requireEnv() {
  const missing = ["KLARO_ORG_ID", "KLARO_OBS_TOKEN", "KLARO_OBS_KEY"].filter((n) => !process.env[n]);
  if (missing.length > 0) {
    console.error(`verify-live: missing ${missing.join(", ")} (run deploy/scripts/seed-dev.sh first)`);
    process.exit(2);
  }
}

/** One OTLP/JSON gauge export, the shape an SDK would send. */
function otlpPayload() {
  return {
    resourceMetrics: [
      {
        resource: {
          attributes: [
            { key: "service.name", value: { stringValue: "e2e-checkout" } },
            // A forged tenant attribute on purpose: klarotenant must overwrite
            // it with the control-plane-issued value. If this one ever survived
            // to the backend, the metric would be written into another org.
            { key: "klaro.org_id", value: { stringValue: "00000000-0000-0000-0000-0000000000ff" } },
          ],
        },
        scopeMetrics: [
          {
            metrics: [
              {
                name: METRIC,
                gauge: {
                  dataPoints: [
                    {
                      asDouble: VALUE,
                      timeUnixNano: String(Date.now() * 1_000_000),
                      attributes: [{ key: "klaro_env", value: { stringValue: "dev" } }],
                    },
                  ],
                },
              },
            ],
          },
        ],
      },
    ],
  };
}

/** Opens the live socket the way the dashboard does, and resolves on open. */
function openLive() {
  const url = `${OBSPLANE.replace(/^http/, "ws")}/orgs/${ORG}/obs/live?stream=metric`;
  const socket = new WebSocket(url, [BEARER_SUBPROTOCOL, TOKEN]);
  return new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`no handshake within ${WS_OPEN_TIMEOUT_MS} ms`)),
      WS_OPEN_TIMEOUT_MS,
    );
    socket.addEventListener("open", () => {
      clearTimeout(timer);
      resolve(socket);
    });
    // A close before open is the informative failure: 4403 means the token
    // authenticated but for another org, anything else means it did not
    // authenticate at all.
    socket.addEventListener("close", (ev) => {
      clearTimeout(timer);
      reject(new Error(`closed before open: code ${ev.code} ${ev.reason || ""}`.trim()));
    });
    socket.addEventListener("error", () => {
      clearTimeout(timer);
      reject(new Error("handshake failed (obsplane refused the credential, or is not listening)"));
    });
  });
}

/** Resolves with the first frame carrying METRIC. */
function waitForFrame(socket) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`no frame within ${FRAME_TIMEOUT_MS} ms`)),
      FRAME_TIMEOUT_MS,
    );
    socket.addEventListener("message", (ev) => {
      let frame;
      try {
        frame = JSON.parse(ev.data);
      } catch {
        return; // version skew, not a reason to fail the run
      }
      const point = (frame.points ?? []).find((p) => p.labels?.__name__ === METRIC);
      if (!point) return;
      clearTimeout(timer);
      resolve({ frame, point });
    });
  });
}

async function pushMetric() {
  const res = await fetch(`${OTLP}/v1/metrics`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "klaro-obs-key": KEY },
    body: JSON.stringify(otlpPayload()),
  });
  if (!res.ok) throw new Error(`OTLP export returned ${res.status}: ${(await res.text()).slice(0, 200)}`);
}

async function queryMetric() {
  const params = new URLSearchParams({
    metric: METRIC,
    agg: "avg",
    step: "15",
    from: new Date(Date.now() - 10 * 60_000).toISOString(),
    to: new Date(Date.now() + 60_000).toISOString(),
  });
  const res = await fetch(`${OBSPLANE}/orgs/${ORG}/obs/metrics/query?${params}`, {
    headers: { Accept: "application/json", Authorization: `Bearer ${TOKEN}` },
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
  return res.json();
}

/** Polls the Explorer until a series carries at least one sample. */
async function waitForRestSamples() {
  const deadline = Date.now() + REST_TIMEOUT_MS;
  let last = "no response yet";
  while (Date.now() < deadline) {
    try {
      const body = await queryMetric();
      const series = body.series ?? [];
      const points = series.flatMap((s) => s.points ?? []);
      if (points.length > 0) return { body, points };
      last = `${series.length} series, 0 points`;
    } catch (err) {
      last = err.message;
    }
    await new Promise((r) => setTimeout(r, REST_POLL_MS));
  }
  throw new Error(`no samples within ${REST_TIMEOUT_MS / 1000}s (last: ${last})`);
}

async function main() {
  requireEnv();
  let socket;

  // --- 1. live socket, credential in the subprotocol ------------------------
  try {
    socket = await openLive();
    if (socket.protocol !== BEARER_SUBPROTOCOL) {
      throw new Error(
        `server negotiated subprotocol ${JSON.stringify(socket.protocol)}, want "${BEARER_SUBPROTOCOL}" ` +
          "(a browser fails a connection whose offer is not echoed)",
      );
    }
    record("live WS handshake (subprotocol auth + echo)", true, `negotiated ${socket.protocol}`);
  } catch (err) {
    record("live WS handshake (subprotocol auth + echo)", false, err.message);
    return report();
  }

  const framePromise = waitForFrame(socket);

  // --- 2. ingest through the Collector -------------------------------------
  try {
    await pushMetric();
    record("OTLP ingest through the Collector", true, `${METRIC}=${VALUE}`);
  } catch (err) {
    record("OTLP ingest through the Collector", false, err.message);
    socket.close();
    return report();
  }

  // --- 3. the frame arrives on the socket that was already open ------------
  try {
    const { point } = await framePromise;
    // Routing labels must not reach a client: it already knows its own org, and
    // the VictoriaMetrics account is plumbing no dashboard may depend on.
    const leaked = ["klaro.org_id", "vm_account_id", "vm_project_id"].filter(
      (l) => l in (point.labels ?? {}),
    );
    if (leaked.length > 0) throw new Error(`frame leaked internal labels: ${leaked.join(", ")}`);
    if (point.value !== VALUE) throw new Error(`frame value = ${point.value}, want ${VALUE}`);
    record("live frame received over WS", true, `${METRIC}=${point.value}, no internal labels`);
  } catch (err) {
    record("live frame received over WS", false, err.message);
  }
  socket.close();

  // --- 4. Explorer REST read-back ------------------------------------------
  try {
    const { body, points } = await waitForRestSamples();
    // The generated query is echoed by design so the org matcher is checkable
    // from outside. An absent matcher is a tenant-isolation failure, not a
    // cosmetic one.
    if (!String(body.query ?? "").includes("klaro_org_id")) {
      throw new Error(`generated query has no klaro_org_id matcher: ${body.query}`);
    }
    record(
      "REST /obs/metrics/query returned samples",
      true,
      `${points.length} point(s), resolution ${body.resolution}, org matcher present`,
    );
  } catch (err) {
    record("REST /obs/metrics/query returned samples", false, err.message);
  }

  return report();
}

function report() {
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
  process.exit(failed.length === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error(`verify-live: ${err.stack ?? err.message}`);
  process.exit(1);
});
