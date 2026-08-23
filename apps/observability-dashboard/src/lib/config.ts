/**
 * Runtime configuration, read once from NEXT_PUBLIC_* so the same build can be
 * pointed at a local obsplane, a staging one, or the mock transport.
 *
 * These are inlined at build time by Next, so `process.env.X` must be written
 * out literally - a computed lookup would be undefined in the browser bundle.
 */

/**
 * How the live WebSocket carries its credential. See .env.example.
 *
 * There is no "query" mode. A token in the URL is written to the access log of
 * every proxy on the path, kept in browser history, and handed out in a Referer
 * if the page ever links away - and a leaked token here is a leaked org. The
 * subprotocol list carries the same string on the handshake only, which is what
 * obsplane reads (services/observability internal/tenancy/wscredential.go).
 */
export type WsAuthMode = "subprotocol" | "none";

function wsAuthMode(raw: string | undefined): WsAuthMode {
  if (raw === "none") return "none";
  if (raw && raw !== "subprotocol") {
    // Named explicitly and ignored, rather than ignored silently: an operator
    // who set "query" needs to know that mode no longer exists, and the symptom
    // otherwise is a live view that works while the config reads as wrong.
    console.warn(
      `NEXT_PUBLIC_OBS_WS_AUTH_MODE=${raw} is not supported; using "subprotocol". ` +
        "A token in the query string leaks into proxy logs and browser history.",
    );
  }
  return "subprotocol";
}

export const config = {
  /** obsplane REST base, no trailing slash. 8090 is the compose default. */
  apiBase: (process.env.NEXT_PUBLIC_OBS_API_BASE ?? "http://localhost:8090").replace(/\/+$/, ""),
  /** Org every route is scoped to. */
  orgId: process.env.NEXT_PUBLIC_OBS_ORG_ID ?? "00000000-0000-0000-0000-000000000001",
  /** Development bearer token; empty means "send no Authorization header". */
  token: process.env.NEXT_PUBLIC_OBS_TOKEN ?? "",
  wsAuthMode: wsAuthMode(process.env.NEXT_PUBLIC_OBS_WS_AUTH_MODE),
  /**
   * Mock mode. Default on: obsplane is not always running next to the
   * dashboard, and a blank screen is a worse default than obviously-fake data.
   */
  mock: (process.env.NEXT_PUBLIC_OBS_MOCK ?? "1") !== "0",
} as const;

/** Absolute URL for an org-scoped REST path, e.g. "/obs/metrics/query". */
export function orgUrl(path: string, params?: URLSearchParams): string {
  const qs = params && [...params.keys()].length > 0 ? `?${params.toString()}` : "";
  return `${config.apiBase}/orgs/${config.orgId}${path}${qs}`;
}

/**
 * ws:// or wss:// URL for the live stream.
 *
 * The credential is never in here - it travels in the subprotocol list from
 * liveProtocols(), which is the whole reason that mode exists.
 */
export function liveUrl(stream: string): string {
  const base = config.apiBase.replace(/^http/, "ws");
  const params = new URLSearchParams({ stream });
  return `${base}/orgs/${config.orgId}/obs/live?${params.toString()}`;
}

/** Must equal tenancy.BearerSubprotocol in services/observability. */
export const BEARER_SUBPROTOCOL = "klaro-bearer";

/**
 * Subprotocols to request on the live handshake.
 *
 * `klaro-bearer` marks the credential and the token follows it; obsplane reads
 * the pair in tenancy.SubprotocolToken and echoes back `klaro-bearer` alone, as
 * RFC 6455 requires of a server that was offered a subprotocol.
 *
 * Empty when there is no token, or when the mode is "none": offering the marker
 * with nothing after it is a handshake obsplane has to reject.
 */
export function liveProtocols(): string[] {
  if (config.wsAuthMode === "subprotocol" && config.token) {
    return [BEARER_SUBPROTOCOL, config.token];
  }
  return [];
}
