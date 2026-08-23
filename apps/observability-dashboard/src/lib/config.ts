/**
 * Runtime configuration, read once from NEXT_PUBLIC_* so the same build can be
 * pointed at a local obsplane, a staging one, or the mock transport.
 *
 * These are inlined at build time by Next, so `process.env.X` must be written
 * out literally - a computed lookup would be undefined in the browser bundle.
 */

/** How the live WebSocket carries its credential. See .env.example. */
export type WsAuthMode = "query" | "subprotocol" | "none";

function wsAuthMode(raw: string | undefined): WsAuthMode {
  return raw === "subprotocol" || raw === "none" ? raw : "query";
}

export const config = {
  /** obsplane REST base, no trailing slash. */
  apiBase: (process.env.NEXT_PUBLIC_OBS_API_BASE ?? "http://localhost:8081").replace(/\/+$/, ""),
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

/** ws:// or wss:// URL for the live stream, with the credential attached. */
export function liveUrl(stream: string): string {
  const base = config.apiBase.replace(/^http/, "ws");
  const params = new URLSearchParams({ stream });
  if (config.wsAuthMode === "query" && config.token) {
    params.set("access_token", config.token);
  }
  return `${base}/orgs/${config.orgId}/obs/live?${params.toString()}`;
}

/** Subprotocols to request, empty unless the subprotocol auth mode is chosen. */
export function liveProtocols(): string[] {
  if (config.wsAuthMode === "subprotocol" && config.token) {
    return ["klaro-bearer", config.token];
  }
  return [];
}
