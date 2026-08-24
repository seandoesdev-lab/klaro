"use client";

/**
 * Cross-signal panel for one selected span [APM-03].
 *
 * This is the payoff of the correlated endpoint: pick a bar in the flame graph
 * and see, without another query, the log lines that span wrote and the metrics
 * of the process that ran it. Everything here is a filter over one already
 * fetched CorrelatedTrace - clicking a span has to feel instant, and it cannot
 * be instant if every click is a round trip.
 *
 * Two scopes exist because the join keys are different, and collapsing them
 * would be a lie about the data:
 *
 *   - Logs are scoped by span_id, which is exact. "This span's lines" is a real
 *     set.
 *   - Metrics are scoped by service+host, which is not per-span. The chart
 *     covers the whole padded trace window for that process, so the panel says
 *     so rather than implying the line belongs to the span.
 */

import { useMemo, useState } from "react";
import { LogList } from "@/components/LogList";
import { MetricsChart, ResolutionBadge } from "@/components/MetricsChart";
import { QueryPeek } from "@/components/QueryControls";
import { EmptyState } from "@/components/States";
import type { CorrelatedTrace, LogEntry, Span } from "@/lib/api/types";
import { formatDateTime, formatDuration } from "@/lib/format";

type Tab = "logs" | "metrics";

/** Lines this span wrote. */
function logsForSpan(logs: LogEntry[], spanId: string): LogEntry[] {
  return logs.filter((l) => l.span_id === spanId);
}

/**
 * Lines written inside the span's own time window, whatever span they name.
 *
 * The fallback for a service that stamps trace_id but not span_id - a common
 * half-configured state, and the difference between an empty tab and a useful
 * one. It is a separate choice rather than merged in, because a time overlap is
 * not the same claim as a span id match.
 */
function logsInWindow(logs: LogEntry[], span: Span): LogEntry[] {
  const end = span.start + span.duration_ms;
  return logs.filter((l) => l.ts >= span.start && l.ts <= end);
}

export interface SpanCorrelationProps {
  correlated: CorrelatedTrace;
  /** The selected span, or null for the whole trace. */
  span: Span | null;
}

export function SpanCorrelation({ correlated, span }: SpanCorrelationProps) {
  const [tab, setTab] = useState<Tab>("logs");
  /** Widen from "this span" to "this span's time window". */
  const [byWindow, setByWindow] = useState(false);

  const exact = useMemo(
    () => (span ? logsForSpan(correlated.logs, span.span_id) : correlated.logs),
    [correlated.logs, span],
  );
  const windowed = useMemo(
    () => (span ? logsInWindow(correlated.logs, span) : correlated.logs),
    [correlated.logs, span],
  );
  const logs = span && byWindow ? windowed : exact;

  // A span reaches its metrics through service+host, so the charts shown are
  // the ones for the process that ran it.
  const metrics = useMemo(() => {
    if (!span) return correlated.metrics;
    const scoped = correlated.metrics.filter(
      (m) => m.service === span.service && (!span.host || !m.host || m.host === span.host),
    );
    // A service with no series of its own must not blank the tab; the
    // trace-wide set is still an honest answer to "what was this system doing".
    return scoped.length > 0 ? scoped : correlated.metrics;
  }, [correlated.metrics, span]);

  return (
    <div className="card">
      <div className="card-head">
        <h2>연계 분석</h2>
        <span className="page-sub">
          {span
            ? span.service + " · " + span.name + " · " + formatDuration(span.duration_ms)
            : "트레이스 전체"}
        </span>
        <span className="spacer" />
        {span?.host ? <span className="badge badge-mute num">{span.host}</span> : null}
      </div>

      <div className="subtabs">
        <button
          type="button"
          className="subtab"
          aria-current={tab === "logs"}
          onClick={() => setTab("logs")}
        >
          로그 <span className="cnt">{logs.length}</span>
        </button>
        <button
          type="button"
          className="subtab"
          aria-current={tab === "metrics"}
          onClick={() => setTab("metrics")}
        >
          메트릭 <span className="cnt">{metrics.length}</span>
        </button>
      </div>

      {tab === "logs" ? (
        <>
          {span ? (
            <div className="toolbar" style={{ marginBottom: "var(--sp-2)" }}>
              <button
                type="button"
                className={byWindow ? "btn btn-sm" : "btn btn-sm btn-primary"}
                onClick={() => setByWindow(false)}
              >
                이 스팬 ({exact.length})
              </button>
              <button
                type="button"
                className={byWindow ? "btn btn-sm btn-primary" : "btn btn-sm"}
                onClick={() => setByWindow(true)}
                title="span_id가 없는 로그도 포함해, 이 스팬의 시간 구간과 겹치는 줄을 봅니다"
              >
                시간 구간 ({windowed.length})
              </button>
              <span className="hint">
                {formatDateTime(span.start)} ~ {formatDateTime(span.start + span.duration_ms)}
              </span>
            </div>
          ) : null}

          {logs.length > 0 ? (
            <LogList entries={logs} />
          ) : (
            <EmptyState
              title="이 스팬에 연결된 로그가 없습니다"
              description={
                span && exact.length === 0 && windowed.length > 0
                  ? "이 스팬을 가리키는 span_id는 없지만 같은 시간 구간의 로그는 있습니다. 위의 '시간 구간'으로 확인하세요."
                  : "애플리케이션 로그에 trace_id가 실려 있어야 연계됩니다 (SDK_CONTRACT 12절)."
              }
            />
          )}

          {correlated.logs_query ? (
            <QueryPeek query={correlated.logs_query} label="서버가 생성한 LogQL" />
          ) : null}
        </>
      ) : null}

      {tab === "metrics" ? (
        <>
          <p className="hint" style={{ marginBottom: "var(--sp-3)" }}>
            메트릭에는 trace_id가 없습니다 — 카운터는 요청 단위가 아닙니다. 아래 차트는 이 스팬을
            실행한 <strong>서비스·호스트</strong>의 시리즈를 트레이스 구간(양쪽 30초 여유 포함)으로
            조회한 것입니다.
          </p>

          {metrics.length === 0 ? (
            <EmptyState
              title="연계할 메트릭 시리즈가 없습니다"
              description="이 서비스가 메트릭을 보내고 있는지, 또는 조회 구간에 샘플이 있는지 확인하세요."
            />
          ) : null}

          <div className="stack">
            {metrics.map((m) => (
              <div key={m.key}>
                <div className="card-head" style={{ marginBottom: "var(--sp-1)" }}>
                  <span className="microlabel">{m.metric}</span>
                  <span className="cell-sub">
                    {m.service}
                    {m.host ? " · " + m.host : ""}
                  </span>
                  <span className="spacer" />
                  <ResolutionBadge resolution={m.resolution} />
                </div>
                {m.series.length > 0 ? (
                  <MetricsChart series={m.series} height={150} showLegend={false} />
                ) : (
                  <p className="hint">이 구간에 샘플이 없습니다.</p>
                )}
                <QueryPeek query={m.query} label="서버가 생성한 MetricsQL" />
              </div>
            ))}
          </div>
        </>
      ) : null}
    </div>
  );
}
