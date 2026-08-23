"use client";

/**
 * Subscription to GET /orgs/:orgId/obs/live (OBS-01/APM-02, design 4.3).
 *
 * Three things the naive version gets wrong and this one does not:
 *
 *  - The close code carries the reason. obsplane answers 4403 for an org scope
 *    mismatch and 4400 for an unknown stream, precisely because a browser
 *    cannot read the handshake status. Reconnecting on those would hammer the
 *    server forever with a request that can never succeed, so they are fatal.
 *  - A dropped stream must look dropped. Per 05 section 7 the view greys out
 *    and says it is reconnecting, rather than leaving a stale value on screen
 *    that reads as a live one.
 *  - Frames arrive about every 2 seconds and each carries every series, so the
 *    kept history is a fixed window rather than everything ever received.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import type { LiveFrame, LiveStream } from "@/lib/api/types";
import { config, liveProtocols, liveUrl } from "@/lib/config";
import { MOCK_SERVICES } from "@/lib/mock/fixtures";

export type LiveStatus = "connecting" | "open" | "reconnecting" | "closed" | "error";

export interface LiveState {
  status: LiveStatus;
  /** Most recent frame, or undefined before the first one arrives. */
  latest: LiveFrame | undefined;
  /** Rolling window, oldest first, capped at windowSize. */
  history: LiveFrame[];
  /** Human-readable reason when status is "error" or "reconnecting". */
  reason: string | undefined;
}

/** Close codes obsplane defines (internal/api/live_ws.go). Never retried. */
const CLOSE_FORBIDDEN = 4403;
const CLOSE_BAD_REQUEST = 4400;

const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 15000;

/** The design's freshness ceiling for the live view ([APM-02]). */
export const LIVE_FLUSH_MS = 2000;

export function useLive(
  stream: LiveStream = "metric",
  options: { enabled?: boolean; windowSize?: number } = {},
): LiveState {
  const enabled = options.enabled ?? true;
  const windowSize = options.windowSize ?? 60;

  const [status, setStatus] = useState<LiveStatus>(enabled ? "connecting" : "closed");
  const [history, setHistory] = useState<LiveFrame[]>([]);
  const [reason, setReason] = useState<string | undefined>(undefined);

  const push = useCallback(
    (frame: LiveFrame) => {
      setHistory((prev) => {
        const next = prev.length >= windowSize ? prev.slice(prev.length - windowSize + 1) : prev.slice();
        next.push(frame);
        return next;
      });
    },
    [windowSize],
  );

  /* -------------------------------------------------- mock live stream --- */
  const mockTick = useRef(0);
  useEffect(() => {
    if (!enabled || !config.mock) return;
    setStatus("open");
    setReason(undefined);

    const emit = () => {
      const t = mockTick.current++;
      const points = MOCK_SERVICES.map((service, i) => ({
        labels: { __name__: "http_server_duration_seconds", service, klaro_env: "prod" },
        // A smooth walk per service: the live tiles should look like telemetry,
        // not like a random number generator.
        value: Number((0.18 + i * 0.05 + Math.sin(t / 7 + i) * 0.06 + (t % 5) * 0.004).toFixed(4)),
      }));
      push({ ts: Date.now(), stream, points });
    };

    emit();
    const id = window.setInterval(emit, LIVE_FLUSH_MS);
    return () => window.clearInterval(id);
  }, [enabled, stream, push]);

  /* -------------------------------------------------- real WebSocket ----- */
  useEffect(() => {
    if (!enabled || config.mock) return;

    let socket: WebSocket | undefined;
    let retryTimer: number | undefined;
    let attempt = 0;
    let disposed = false;

    const connect = () => {
      if (disposed) return;
      setStatus(attempt === 0 ? "connecting" : "reconnecting");

      const protocols = liveProtocols();
      socket =
        protocols.length > 0
          ? new WebSocket(liveUrl(stream), protocols)
          : new WebSocket(liveUrl(stream));

      socket.onopen = () => {
        attempt = 0;
        setStatus("open");
        setReason(undefined);
      };

      socket.onmessage = (ev) => {
        try {
          push(JSON.parse(ev.data as string) as LiveFrame);
        } catch {
          // An unparseable frame is version skew, not a reason to drop the
          // connection - the next frame is 2 seconds away.
        }
      };

      socket.onclose = (ev) => {
        if (disposed) return;
        if (ev.code === CLOSE_FORBIDDEN) {
          setStatus("error");
          setReason("이 조직의 실시간 스트림에 접근할 수 없습니다(4403). 토큰과 조직 ID를 확인하세요.");
          return;
        }
        if (ev.code === CLOSE_BAD_REQUEST) {
          setStatus("error");
          setReason("알 수 없는 스트림입니다: " + stream);
          return;
        }
        setStatus("reconnecting");
        setReason("대상 응답 없음, 재연결 시도 중");
        const backoff = Math.min(RECONNECT_BASE_MS * Math.pow(2, attempt), RECONNECT_MAX_MS);
        attempt += 1;
        retryTimer = window.setTimeout(connect, backoff);
      };

      // onerror always precedes onclose in browsers; the close handler owns the
      // retry so the two cannot both schedule one.
      socket.onerror = () => undefined;
    };

    connect();

    return () => {
      disposed = true;
      if (retryTimer) window.clearTimeout(retryTimer);
      socket?.close();
    };
  }, [enabled, stream, push]);

  useEffect(() => {
    if (!enabled) setStatus("closed");
  }, [enabled]);

  return {
    status,
    latest: history.length > 0 ? history[history.length - 1] : undefined,
    history,
    reason,
  };
}
