"use client";

/**
 * Multi-series line chart for a MetricsResult.
 *
 * The actual canvas rendering lives in TimeSeriesChart (uPlot-backed, P1
 * foundation); this component's job is just turning obsplane's per-series
 * wire shape into that component's `lines` prop and picking colours from the
 * prototype's --s-p50/95/99 ramp plus the accent, so a chart in this app looks
 * like a chart in the load-test dashboard.
 */

import type { Series } from "@/lib/api/types";
import { seriesLabel } from "@/lib/format";
import { TimeSeriesChart, type TimeSeriesLine } from "@/components/TimeSeriesChart";

const SERIES_COLORS = ["var(--s-p95)", "var(--s-p50)", "var(--s-p99)", "var(--warn)", "var(--good)"];

function toLines(series: Series[]): TimeSeriesLine[] {
  return series.map((s, i) => ({
    key: "s" + i,
    label: seriesLabel(s.labels),
    color: SERIES_COLORS[i % SERIES_COLORS.length],
    points: s.points,
  }));
}

export function MetricsChart({
  series,
  height = 260,
  showLegend = true,
}: {
  series: Series[];
  height?: number;
  showLegend?: boolean;
}) {
  return <TimeSeriesChart lines={toLines(series)} height={height} showLegend={showLegend} />;
}

/**
 * Resolution badge.
 *
 * obsplane answers a wide window from a rollup rather than raw samples, and it
 * says so in the response. Not surfacing that would let a flat 1h-bucket line
 * read as a calm service (internal/explorer/metrics.go).
 */
export function ResolutionBadge({ resolution, clamped }: { resolution: string; clamped?: boolean }) {
  const tone = resolution === "raw" ? "badge-good" : "badge-warn";
  const text =
    resolution === "raw" ? "원본 해상도" : resolution === "5m" ? "5분 롤업" : resolution === "1h" ? "1시간 롤업" : resolution;
  return (
    <>
      <span className={"badge " + tone} title="응답이 원본 샘플인지 다운샘플 롤업인지">
        {resolution === "raw" ? "●" : "▪"} {text}
      </span>
      {clamped ? (
        <span className="badge badge-warn" title="플랜 보존 기간을 넘는 구간이 잘렸습니다">
          ⚠ 보존 한도로 구간 축소
        </span>
      ) : null}
    </>
  );
}
