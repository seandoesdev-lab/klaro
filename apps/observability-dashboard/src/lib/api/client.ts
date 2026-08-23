/**
 * The one place that talks HTTP to obsplane.
 *
 * Two things live here rather than in every caller: the `Authorization: Bearer`
 * header, and the decode of the shared error envelope
 * (services/observability internal/platform/httpx). A component that catches an
 * ApiError therefore always has a code it can branch on - including for a
 * transport failure, which gets a synthetic NETWORK_ERROR rather than an
 * exception of a different shape.
 */

import { config, orgUrl } from "@/lib/config";
import { ErrorCode, type ApiErrorBody, type ErrorCodeValue } from "./types";

/** A failed obsplane call, carrying the envelope's code so callers can branch. */
export class ApiError extends Error {
  readonly code: ErrorCodeValue | string;
  readonly status: number;
  readonly details: unknown;

  constructor(code: string, message: string, status: number, details?: unknown) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.details = details;
  }

  /** True when re-running the same request could plausibly succeed. */
  get retryable(): boolean {
    return this.code === ErrorCode.Network || this.status >= 500;
  }
}

function authHeaders(): Record<string, string> {
  return config.token ? { Authorization: `Bearer ${config.token}` } : {};
}

async function decodeError(res: Response): Promise<ApiError> {
  let body: ApiErrorBody | undefined;
  try {
    body = (await res.json()) as ApiErrorBody;
  } catch {
    // A non-JSON body is a proxy or a crash, not obsplane answering.
  }
  const payload = body?.error;
  return new ApiError(
    payload?.code ?? ErrorCode.Internal,
    payload?.message ?? `요청이 실패했습니다 (HTTP ${res.status})`,
    res.status,
    payload?.details,
  );
}

interface RequestOptions {
  method?: "GET" | "POST" | "PATCH" | "DELETE";
  params?: URLSearchParams;
  body?: unknown;
  signal?: AbortSignal;
}

/**
 * Perform one org-scoped request and decode it.
 *
 * `path` is relative to /orgs/:orgId, e.g. "/obs/logs".
 */
export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = "GET", params, body, signal } = opts;

  let res: Response;
  try {
    res = await fetch(orgUrl(path, params), {
      method,
      signal,
      headers: {
        Accept: "application/json",
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
        ...authHeaders(),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      cache: "no-store",
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") throw err;
    throw new ApiError(
      ErrorCode.Network,
      "관측 API에 연결할 수 없습니다. 서비스가 실행 중인지 확인하세요.",
      0,
      err instanceof Error ? err.message : String(err),
    );
  }

  if (!res.ok) throw await decodeError(res);
  // 204 from DELETE: nothing to decode, and callers type it as void.
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}
