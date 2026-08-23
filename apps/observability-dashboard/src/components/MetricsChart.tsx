"use client";

/**
 * Multi-series line chart for a MetricsResult.
 *
 * Recharts wants one row per x value with a column per series, while obsplane
 * returns one array of points per series, so the pivot happens here rather than
 * in every caller. Timestamps are the join key: series can have holes, and
 * zipping by index would silently shift a gapped series sideways in time.
 *
 * Series colours come from the prototype's --s-p50/95/99 ramp plus the accent,
 * so a chart in this app looks like a chart in the load-test dashboard.
 */

import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { Series } from "@/lib/api/types";
import { formatMetric, formatTime, seriesLabel } from "@/lib/format";

const SERIES_COLORS = ["var(--s-p95)", "var(--s-p50)", "var(--s-p99)", "var(--warn)", "var(--good)"];

export interface ChartSeries {
  key: string;
  label: string;
  color: string;
}

interface PivotResult {
  rows: Array<Record<string, number>>;
  series: ChartSeries[];
}

/** Pivot obsplane series into Recharts rows keyed by timestamp. */
export function pivotSeries(input: Series[]): PivotResult {
  const byTs = new Map<number, Record<string, number>>();
  const series: ChartSeries[] = [];

  input.forEach((s, i) => {
    const key = "s" + i;
    series.push({ key, label: seriesLabel(s.labels), color: SERIES_COLORS[i % SERIES_COLORS.length] });
    for (const [ts, value] of s.points) {
      let row = byTs.get(ts);
      if (!row) {
        row = { ts };
        byTs.set(ts, row);
      }
      row[key] = value;
    }
  });

  const rows = [...byTs.values()].sort((a, b) => a.ts - b.ts);
  return { rows, series };
}

/**
 * Recharts types the content component with its own internal props type, whose
 * shape has changed across majors. Declaring only the three fields this tooltip
 * actually reads keeps it from breaking on the next one.
 */
interface TooltipEntry {
  dataKey?: string | number;
  name?: string | number;
  value?: number;
  color?: string;
}

function ChartTooltip({
  active,
  payload,
  label,
}: {
  active?: boolean;
  payload?: TooltipEntry[];
  label?: string | number;
}) {
  if (!active || !payload || payload.length === 0) return null;
  return (
    <div className="chart-tip">
      <div className="tip-ts">{formatTime(Number(label))}</div>
      {payload.map((entry) => (
        <div className="tip-row" key={String(entry.dataKey)}>
          <span className="swatch" style={{ background: entry.color }} />
          <span>{entry.name}</span>
          <span style={{ marginLeft: "auto" }}>{formatMetric(Number(entry.value))}</span>
        </div>
      ))}
    </div>
  );
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
  const { rows, series: keys } = pivotSeries(series);

  return (
    <div className="chart-wrap">
      <div className="chart-inner">
        <ResponsiveContainer width="100%" height={height}>
          <LineChart data={rows} margin={{ top: 6, right: 12, bottom: 4, left: 4 }}>
            <CartesianGrid strokeDasharray="3 3" vertical={false} />
            <XAxis
              dataKey="ts"
              type="number"
              scale="time"
              domain={["dataMin", "dataMax"]}
              tickFormatter={formatTime}
              minTickGap={48}
              stroke="var(--border-strong)"
            />
            <YAxis tickFormatter={formatMetric} width={62} stroke="var(--border-strong)" />
            <Tooltip content={<ChartTooltip />} />
            {keys.map((k) => (
              <Line
                key={k.key}
                type="monotone"
                dataKey={k.key}
                name={k.label}
                stroke={k.color}
                strokeWidth={1.8}
                dot={false}
                isAnimationActive={false}
                connectNulls={false}
              />
            ))}
          </LineChart>
        </ResponsiveContainer>
      </div>
      {showLegend ? (
        <div className="chart-legend">
          {keys.map((k) => (
            <span key={k.key}>
              <i className="swatch" style={{ background: k.color }} />
              {k.label}
            </span>
          ))}
        </div>
      ) : null}
    </div>
  );
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
