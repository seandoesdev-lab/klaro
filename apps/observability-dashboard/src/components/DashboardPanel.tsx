"use client";

/**
 * One dashboard tile: runs the panel's stored Explorer query and renders it the
 * way the panel type asks for.
 *
 * Each panel fetches independently. That is deliberate - 05 section 7 requires
 * a partial failure to darken only the card that failed, and a single combined
 * request would let one bad panel take the whole dashboard down.
 */

import Link from "next/link";
import { useCallback } from "react";
import { LogList } from "@/components/LogList";
import { MetricsChart, ResolutionBadge } from "@/components/MetricsChart";
import { ChartSkeleton, EmptyState, ErrorState, TableSkeleton } from "@/components/States";
import { useAsync } from "@/hooks/useAsync";
import { queryLogs, queryMetrics, searchTraces } from "@/lib/api/endpoints";
import type { LogsPage, MetricsResult, Panel, Series, TracesResult } from "@/lib/api/types";
import { formatDateTime, formatDuration, formatMetric } from "@/lib/format";

/**
 * A tagged union rather than the bare responses: PanelQuery.signal decides
 * which endpoint runs, and carrying that decision into the result is what lets
 * the renderer narrow without guessing from the response shape - TracesResult
 * and LogsPage both have a `data` array.
 */
type PanelData =
  | { kind: "metrics"; result: MetricsResult }
  | { kind: "traces"; result: TracesResult }
  | { kind: "logs"; result: LogsPage };

/** Latest value across all series - what a "stat" panel is asking for. */
function latestValue(series: Series[]): number | undefined {
  let best: [number, number] | undefined;
  for (const s of series) {
    const last = s.points[s.points.length - 1];
    if (last && (!best || last[0] > best[0])) best = last;
  }
  return best?.[1];
}

export function DashboardPanel({
  panel,
  rangeSec,
  onRemove,
}: {
  panel: Panel;
  rangeSec: number;
  /**
   * Edit affordance. It lives inside the card rather than beneath it, so it
   * cannot be read as belonging to the panel on the next row.
   */
  onRemove?: () => void;
}) {
  const q = panel.query;

  const fetcher = useCallback(
    async (signal: AbortSignal): Promise<PanelData> => {
      const to = new Date();
      const from = new Date(to.getTime() - rangeSec * 1000);

      if (q.signal === "logs") {
        const result = await queryLogs({
          from,
          to,
          filters: q.filters ?? [],
          contains: q.contains,
          limit: q.limit,
          signal,
        });
        return { kind: "logs", result };
      }
      if (q.signal === "traces") {
        const result = await searchTraces({
          from,
          to,
          service: q.service,
          minDurationMs: q.min_duration_ms,
          limit: q.limit,
          signal,
        });
        return { kind: "traces", result };
      }
      const result = await queryMetrics({
        from,
        to,
        metric: q.metric ?? "",
        filters: q.filters ?? [],
        agg: q.agg ?? "",
        stepSec: q.step_sec ?? 0,
        signal,
      });
      return { kind: "metrics", result };
    },
    [q, rangeSec],
  );

  const { data, error, loading, reload } = useAsync(fetcher, [q, rangeSec]);
  const span = Math.min(Math.max(panel.layout.w || 6, 1), 12);

  return (
    <div className="card dash-panel">
      <div className="card-head">
        <h2>{panel.title}</h2>
        <span className="spacer" />
        {data?.kind === "metrics" ? (
          <ResolutionBadge resolution={data.result.resolution} clamped={data.result.clamped} />
        ) : null}
        <span className="badge badge-mute">{q.signal}</span>
        {onRemove ? (
          <button type="button" className="icon-btn" aria-label={panel.title + " 패널 제거"} onClick={onRemove}>
            ×
          </button>
        ) : null}
      </div>

      {error ? <ErrorState error={error} onRetry={reload} /> : null}
      {!error && loading ? (q.signal === "metrics" ? <ChartSkeleton /> : <TableSkeleton rows={4} />) : null}

      {!error && !loading && data?.kind === "metrics" ? (
        data.result.series.length === 0 ? (
          <EmptyState title="데이터 없음" description="이 구간에 저장된 시계열이 없습니다." />
        ) : panel.type === "stat" ? (
          <div className="kpi-value">{formatMetric(latestValue(data.result.series) ?? NaN)}</div>
        ) : (
          <MetricsChart
            series={data.result.series}
            height={panel.type === "timeseries" ? 220 : 160}
            showLegend={span >= 6}
          />
        )
      ) : null}

      {!error && !loading && data?.kind === "logs" ? (
        data.result.data.length === 0 ? (
          <EmptyState title="로그 없음" description="이 구간·조건에 해당하는 줄이 없습니다." />
        ) : (
          <LogList entries={data.result.data} />
        )
      ) : null}

      {!error && !loading && data?.kind === "traces" ? (
        data.result.data.length === 0 ? (
          <EmptyState title="트레이스 없음" description="이 구간·조건에 해당하는 트레이스가 없습니다." />
        ) : (
          <div className="tbl-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>루트</th>
                  <th className="ta-r">소요</th>
                  <th>시작</th>
                </tr>
              </thead>
              <tbody>
                {data.result.data.map((t) => (
                  <tr key={t.trace_id}>
                    <td>
                      <Link href={"/traces/" + t.trace_id} style={{ textDecoration: "none" }}>
                        {t.root_name || t.root_service}
                      </Link>
                    </td>
                    <td className="ta-r num">{formatDuration(t.duration_ms)}</td>
                    <td className="cell-sub">{formatDateTime(t.start)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      ) : null}
    </div>
  );
}
