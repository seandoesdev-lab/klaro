"use client";

/**
 * Generic multi-series time chart on uPlot's canvas renderer.
 *
 * uPlot draws to a single <canvas> instead of one SVG node per sample, which
 * is what lets a live/high-resolution metric view hold thousands of points
 * without the frame drops Recharts hit there. This is the P1 replacement for
 * that Recharts line chart - MetricsChart now just pivots its wire series into
 * `lines` and renders this. P1a/P1b can reuse it directly for any other
 * time-aligned overlay (e.g. a flamegraph's time ruler, a hostmap tile's
 * sparkline) without re-deriving the canvas/tooltip/resize plumbing below.
 *
 * A <canvas> cannot reference a CSS custom property - `ctx.strokeStyle` needs
 * a resolved color string. So every draw color is read from
 * getComputedStyle(document.documentElement) at build time, and a
 * MutationObserver on the `data-theme` attribute rebuilds the plot whenever
 * the theme toggle flips it (see AppShell's ThemeToggle).
 */

import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import { useLayoutEffect, useRef } from "react";
import type { Sample } from "@/lib/api/types";
import { formatMetric, formatTime } from "@/lib/format";

export interface TimeSeriesLine {
  key: string;
  label: string;
  /** A literal color or a `var(--token)` reference - resolved against the live theme. */
  color: string;
  points: Sample[];
}

function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

const VAR_REF = /^var\((--[\w-]+)\)$/;

/** Resolves a `var(--x)` reference against the live theme; passes through anything else. */
function resolveColor(color: string): string {
  const m = VAR_REF.exec(color.trim());
  return m ? cssVar(m[1], color) : color;
}

/**
 * Aligns per-series samples onto one shared, sorted timestamp axis.
 *
 * uPlot data is `[xs[], ...ys[][]]` with every array the same length; the wire
 * format is one sparse (ts, value) list per series, so series can disagree on
 * which timestamps they have (exactly the gap case MetricsChart's old
 * `pivotSeries` handled by joining on `ts` rather than zipping by index).
 */
function toAlignedData(lines: TimeSeriesLine[]): uPlot.AlignedData {
  const tsSet = new Set<number>();
  for (const line of lines) for (const [ts] of line.points) tsSet.add(ts);
  const xs = [...tsSet].sort((a, b) => a - b);
  const index = new Map(xs.map((ts, i) => [ts, i]));

  const ys: Array<Array<number | null>> = lines.map(() => new Array(xs.length).fill(null));
  lines.forEach((line, si) => {
    for (const [ts, v] of line.points) {
      const i = index.get(ts);
      if (i !== undefined) ys[si][i] = v;
    }
  });
  return [xs, ...ys] as uPlot.AlignedData;
}

export function TimeSeriesChart({
  lines,
  height = 260,
  showLegend = true,
}: {
  lines: TimeSeriesLine[];
  height?: number;
  showLegend?: boolean;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const tooltipRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

  useLayoutEffect(() => {
    const container = containerRef.current;
    const tooltip = tooltipRef.current;
    if (!container || !tooltip) return;
    // Re-bound to a non-nullable const: closures below don't retain the
    // narrowing on `tooltip` itself since its declared type stays nullable.
    const tooltipEl = tooltip;

    function build() {
      if (!container) return;
      plotRef.current?.destroy();

      const axisStroke = cssVar("--border-strong", "#888");
      const gridStroke = cssVar("--grid", "#88888833");
      const font = "10.5px " + cssVar("--mono", "monospace");

      const opts: uPlot.Options = {
        width: container.clientWidth || 420,
        height,
        padding: [8, 8, 0, 0],
        cursor: { points: { show: false } },
        legend: { show: false },
        scales: { x: { time: false } },
        axes: [
          {
            stroke: axisStroke,
            grid: { show: false },
            ticks: { stroke: axisStroke },
            font,
            values: (_u, vals) => vals.map((v) => formatTime(v)),
          },
          {
            stroke: axisStroke,
            grid: { stroke: gridStroke, width: 1 },
            ticks: { stroke: axisStroke },
            font,
            size: 56,
            values: (_u, vals) => vals.map((v) => formatMetric(v)),
          },
        ],
        series: [
          {},
          ...lines.map((line) => ({
            label: line.label,
            stroke: resolveColor(line.color),
            width: 1.8,
            points: { show: false },
            spanGaps: false,
          })),
        ],
        hooks: {
          setCursor: [
            (u) => {
              const idx = u.cursor.idx;
              const ts = idx != null ? (u.data[0][idx] as number | null) : null;
              if (idx == null || ts == null) {
                tooltipEl.style.display = "none";
                return;
              }
              const rows = lines
                .map((line, i) => ({ line, v: u.data[i + 1][idx] as number | null }))
                .filter((r): r is { line: TimeSeriesLine; v: number } => r.v != null);
              if (rows.length === 0) {
                tooltipEl.style.display = "none";
                return;
              }
              tooltipEl.style.display = "block";
              tooltipEl.innerHTML =
                `<div class="tip-ts">${formatTime(ts)}</div>` +
                rows
                  .map(
                    (r) =>
                      `<div class="tip-row"><span class="swatch" style="background:${resolveColor(r.line.color)}"></span>` +
                      `<span>${r.line.label}</span><span style="margin-left:auto">${formatMetric(r.v)}</span></div>`,
                  )
                  .join("");
              const left = u.cursor.left ?? 0;
              const top = u.cursor.top ?? 0;
              const maxLeft = Math.max(0, u.over.clientWidth - tooltipEl.offsetWidth - 8);
              tooltipEl.style.left = Math.min(left + 12, maxLeft) + "px";
              tooltipEl.style.top = Math.max(0, top - 8) + "px";
            },
          ],
        },
      };

      plotRef.current = new uPlot(opts, toAlignedData(lines), container);
    }

    build();

    const resize = new ResizeObserver(() => {
      plotRef.current?.setSize({ width: container.clientWidth || 420, height });
    });
    resize.observe(container);

    const themeObserver = new MutationObserver(build);
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });

    return () => {
      resize.disconnect();
      themeObserver.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
    // A full rebuild per data/height change (rather than an incremental
    // setData) keeps series identity/order reconciliation out of scope here -
    // these panels redraw at most a few times a second.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lines, height]);

  return (
    <div className="chart-wrap">
      <div className="chart-inner" style={{ position: "relative" }} ref={containerRef}>
        <div
          className="chart-tip"
          ref={tooltipRef}
          style={{ position: "absolute", display: "none", pointerEvents: "none", zIndex: 5 }}
        />
      </div>
      {showLegend ? (
        <div className="chart-legend">
          {lines.map((line) => (
            <span key={line.key}>
              <i className="swatch" style={{ background: line.color }} />
              {line.label}
            </span>
          ))}
        </div>
      ) : null}
    </div>
  );
}
