"use client";

/**
 * Hostmap: one hexagon per host, coloured by how hot it is (P1b).
 *
 * The point of this view is triage at a glance - "which of these ninety
 * machines is in trouble" - so it optimises for one thing: a hot host must be
 * findable without reading a single label. Everything else (names, exact
 * numbers, history) is one click away in the detail panel.
 *
 * Colour is bucketed onto the existing status tokens rather than interpolated
 * along a gradient. Two reasons: an SVG fill can carry `var(--token)`, so the
 * map re-themes with the rest of the app for free, and a continuous ramp makes
 * 71% and 74% look meaningfully different when the only decision the viewer has
 * to make is "is this one warm or is it on fire". Intensity inside a band is
 * carried by opacity, which is what gives the map its heat-map texture.
 *
 * The hexagon geometry is computed here rather than taken from @visx/shape's
 * Polygon: Polygon draws a regular n-gon at its own orientation, and a hostmap
 * needs flat-top hexes that tessellate with a known column offset. visx still
 * does the parts it is better at - responsive measurement, the intensity scale,
 * and tooltip positioning.
 */

import { Group } from "@visx/group";
import { ParentSize } from "@visx/responsive";
import { scaleLinear } from "@visx/scale";
import { useTooltip, useTooltipInPortal } from "@visx/tooltip";
import { useMemo } from "react";
import type { Host } from "@/lib/api/types";
import { formatMetric } from "@/lib/format";

/** Which reading paints the map. */
export type HeatMetric = "cpu_pct" | "mem_pct";

export const HEAT_LABELS: Record<HeatMetric, string> = {
  cpu_pct: "CPU 사용률",
  mem_pct: "메모리 사용률",
};

/**
 * Utilisation bands, hottest last.
 *
 * The thresholds are the ones an operator already reasons in: comfortable below
 * 30, working 30-55, worth watching 55-75, tight 75-90, saturated above that.
 */
const BANDS: Array<{ upTo: number; token: string; label: string }> = [
  { upTo: 30, token: "var(--good)", label: "여유 (<30%)" },
  { upTo: 55, token: "var(--accent)", label: "정상 (30~55%)" },
  { upTo: 75, token: "var(--warn)", label: "주의 (55~75%)" },
  { upTo: 90, token: "var(--sev-high)", label: "높음 (75~90%)" },
  { upTo: Infinity, token: "var(--crit)", label: "포화 (>90%)" },
];

function bandFor(value: number): (typeof BANDS)[number] {
  return BANDS.find((b) => value < b.upTo) ?? BANDS[BANDS.length - 1];
}

const SQRT3 = Math.sqrt(3);

/**
 * Flat-top hexagon outline of circumradius r centred on (cx, cy).
 *
 * Flat-top rather than pointy-top because the columns then interlock on the
 * vertical axis, which packs a wide fleet into a short block - the shape a
 * dashboard panel actually has.
 */
function hexPoints(cx: number, cy: number, r: number): string {
  const pts: string[] = [];
  for (let i = 0; i < 6; i++) {
    const angle = (Math.PI / 3) * i;
    const x = cx + r * Math.cos(angle);
    const y = cy + r * Math.sin(angle);
    pts.push(x.toFixed(2) + "," + y.toFixed(2));
  }
  return pts.join(" ");
}

/** Largest hexagon radius that fits `count` cells inside the given box. */
function fitRadius(width: number, height: number, count: number): { r: number; cols: number } {
  for (let r = 46; r >= 9; r -= 1) {
    const cols = Math.max(1, Math.floor((width - 0.5 * r) / (1.5 * r)));
    const rows = Math.ceil(count / cols);
    // The extra half row is the offset odd columns are pushed down by.
    if (rows * SQRT3 * r + (SQRT3 * r) / 2 <= height) return { r, cols };
  }
  return { r: 9, cols: Math.max(1, Math.floor(width / 13.5)) };
}

interface Cell {
  host: Host;
  value: number | null;
  cx: number;
  cy: number;
}

function HostMapCanvas({
  hosts,
  metric,
  selected,
  onSelect,
  width,
  height,
}: {
  hosts: Host[];
  metric: HeatMetric;
  selected?: string;
  onSelect: (hostIdent: string) => void;
  width: number;
  height: number;
}) {
  const { showTooltip, hideTooltip, tooltipData, tooltipLeft, tooltipTop, tooltipOpen } =
    useTooltip<Cell>();
  // A portal keeps the tooltip out of the SVG, where it would be clipped and
  // could not reuse the app's card styling.
  const { containerRef, TooltipInPortal } = useTooltipInPortal({
    scroll: true,
    detectBounds: true,
  });

  const { r, cols } = useMemo(
    () => fitRadius(width, height, Math.max(hosts.length, 1)),
    [width, height, hosts.length],
  );

  // Opacity carries intensity inside a band. The floor is deliberately not 0:
  // an idle host is still a host, and a cell nobody can see is a cell nobody
  // can click.
  const intensity = useMemo(
    () => scaleLinear<number>({ domain: [0, 100], range: [0.35, 1], clamp: true }),
    [],
  );

  const cells: Cell[] = useMemo(
    () =>
      hosts.map((host, i) => {
        const col = i % cols;
        const row = Math.floor(i / cols);
        return {
          host,
          value: host[metric],
          cx: r + col * 1.5 * r,
          cy: SQRT3 * r * (row + 0.5) + (col % 2 === 1 ? (SQRT3 * r) / 2 : 0),
        };
      }),
    [hosts, cols, r, metric],
  );

  // Trim the canvas to the rows actually used, so a four-host org does not get
  // a panel of empty space under its fleet.
  const usedHeight = cells.reduce((h, c) => Math.max(h, c.cy + SQRT3 * r * 0.5), SQRT3 * r);

  return (
    <div ref={containerRef} style={{ position: "relative" }}>
      <svg
        width={width}
        height={usedHeight}
        role="img"
        aria-label={HEAT_LABELS[metric] + " 호스트맵"}
      >
        <Group>
          {cells.map((cell) => {
            const reported = cell.value != null;
            const band = reported ? bandFor(cell.value as number) : null;
            const isSelected = cell.host.host_ident === selected;
            return (
              <polygon
                key={cell.host.host_ident}
                className="hexcell"
                points={hexPoints(cell.cx, cell.cy, r * 0.94)}
                fill={band ? band.token : "var(--surface-2)"}
                fillOpacity={reported ? intensity(cell.value as number) : 1}
                stroke={isSelected ? "var(--ink)" : "var(--bg)"}
                strokeWidth={isSelected ? 2.5 : 1}
                strokeDasharray={reported ? undefined : "3 3"}
                tabIndex={0}
                role="button"
                aria-label={
                  cell.host.host_ident +
                  ": " +
                  (reported ? Math.round(cell.value as number) + "%" : "보고 없음")
                }
                aria-pressed={isSelected}
                onClick={() => onSelect(cell.host.host_ident)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    onSelect(cell.host.host_ident);
                  }
                }}
                onMouseMove={(e) => {
                  const box = e.currentTarget.ownerSVGElement?.getBoundingClientRect();
                  showTooltip({
                    tooltipData: cell,
                    tooltipLeft: e.clientX - (box?.left ?? 0),
                    tooltipTop: e.clientY - (box?.top ?? 0),
                  });
                }}
                onMouseLeave={hideTooltip}
              />
            );
          })}
        </Group>
      </svg>

      {tooltipOpen && tooltipData ? (
        <TooltipInPortal top={tooltipTop} left={tooltipLeft} className="chart-tip">
          <div className="tip-ts">{tooltipData.host.host_ident}</div>
          <div className="tip-row">
            <span>{HEAT_LABELS[metric]}</span>
            <span style={{ marginLeft: "auto" }}>
              {tooltipData.value != null ? tooltipData.value.toFixed(1) + "%" : "보고 없음"}
            </span>
          </div>
          <div className="tip-row">
            <span>load1</span>
            <span style={{ marginLeft: "auto" }}>
              {tooltipData.host.load1 != null ? formatMetric(tooltipData.host.load1) : "-"}
            </span>
          </div>
          {tooltipData.host.service ? (
            <div className="tip-row">
              <span>service</span>
              <span style={{ marginLeft: "auto" }}>{tooltipData.host.service}</span>
            </div>
          ) : null}
        </TooltipInPortal>
      ) : null}
    </div>
  );
}

export function HostMapLegend({ metric }: { metric: HeatMetric }) {
  return (
    <div className="hexlegend">
      <span className="microlabel">{HEAT_LABELS[metric]}</span>
      {BANDS.map((b) => (
        <span key={b.label}>
          <i className="hexswatch" style={{ background: b.token }} />
          {b.label}
        </span>
      ))}
      <span>
        <i className="hexswatch is-empty" />
        보고 없음
      </span>
    </div>
  );
}

/**
 * Responsive wrapper.
 *
 * maxHeight is a budget, not a size: fitRadius shrinks the cells until the
 * whole fleet fits inside it, and the canvas is then trimmed to what was used.
 */
export function HostMap({
  hosts,
  metric,
  selected,
  onSelect,
  maxHeight = 340,
}: {
  hosts: Host[];
  metric: HeatMetric;
  selected?: string;
  onSelect: (hostIdent: string) => void;
  maxHeight?: number;
}) {
  return (
    <ParentSize debounceTime={16}>
      {({ width }: { width: number }) =>
        width < 40 ? null : (
          <HostMapCanvas
            hosts={hosts}
            metric={metric}
            selected={selected}
            onSelect={onSelect}
            width={width}
            height={maxHeight}
          />
        )
      }
    </ParentSize>
  );
}
