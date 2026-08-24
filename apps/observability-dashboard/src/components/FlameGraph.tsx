"use client";

/**
 * Flame graph for one trace (05 section 4.6, [APM-03]).
 *
 * The difference from the waterfall beside it is what the axes mean. The
 * waterfall gives every span its own row, so a 200-span trace is 200 rows of
 * scrolling. Here depth is the y axis and time is the x axis, so the whole trace
 * is one screen and the shape of the latency is the thing you see: a wide bar
 * with one wide child is a call chain, a wide bar with many narrow children is a
 * fan-out, and a wide bar with nothing under it is where the time actually went.
 *
 * It is laid out on real timestamps rather than by packing children left to
 * right. Packing is what a CPU-profile flame graph does, because samples have no
 * clock; spans do, and a child that starts 300ms after its parent is exactly the
 * gap worth seeing. Packing would erase that gap.
 *
 * visx (scaleLinear + Group) rather than a chart component: a flame graph is a
 * hierarchy of positioned rects, so what is needed is the scale and the SVG
 * grouping, not an axis-and-series abstraction. d3-hierarchy is not used either,
 * because the tree here is one pass over parent ids and a d3 layout would only
 * re-derive coordinates this component already has.
 */

import { Group } from "@visx/group";
import { ParentSize } from "@visx/responsive";
import { scaleLinear } from "@visx/scale";
import { useMemo, useState } from "react";
import type { Span } from "@/lib/api/types";
import { formatDuration } from "@/lib/format";

/** A span is hot when it dominates the trace. Matches TraceWaterfall. */
const HOT_SHARE = 0.35;

/** Row geometry. Dense on purpose: a deep trace has to stay on one screen. */
const ROW_H = 18;
const ROW_GAP = 2;
/** Below this a label cannot be read, so drawing one is only noise. */
const MIN_LABEL_PX = 44;
/** Rough advance width of the mono face at this size, for label truncation. */
const CHAR_PX = 6.2;

/**
 * Per-service fill, from the chart series palette.
 *
 * Colour carries service rather than duration because duration is already the
 * width. A trace that hands off between four services reads as four bands, and
 * "the time is all in the purple one" is a conclusion reachable without
 * hovering anything.
 */
const SERVICE_COLORS = [
  "var(--s-p95)",
  "var(--s-p50)",
  "var(--s-p99)",
  "var(--accent)",
  "var(--good)",
];

export interface FlameNode {
  span: Span;
  depth: number;
  /** Share of the whole trace this span occupies, 0..1. */
  share: number;
}

/**
 * Flattens the spans into rows, depth-first from the roots.
 *
 * A span whose parent is missing from the response is treated as a root. The
 * alternative is dropping it, which hides real work whenever a trace is read
 * while part of it is still in flight.
 *
 * Exported so the page can reuse the depth ordering for its span table without
 * the two views disagreeing about the tree.
 */
export function layoutFlame(spans: Span[]): { nodes: FlameNode[]; maxDepth: number } {
  if (spans.length === 0) return { nodes: [], maxDepth: 0 };

  const ids = new Set(spans.map((s) => s.span_id));
  const byParent = new Map<string, Span[]>();
  for (const s of spans) {
    const parent = s.parent_span_id && ids.has(s.parent_span_id) ? s.parent_span_id : "";
    const list = byParent.get(parent);
    if (list) list.push(s);
    else byParent.set(parent, [s]);
  }
  for (const list of byParent.values()) list.sort((a, b) => a.start - b.start);

  const traceStart = Math.min(...spans.map((s) => s.start));
  const traceEnd = Math.max(...spans.map((s) => s.start + s.duration_ms));
  const total = Math.max(traceEnd - traceStart, 1);

  const nodes: FlameNode[] = [];
  let maxDepth = 0;
  // Iterative rather than recursive: a pathological trace - a long chain of
  // single-child spans - could otherwise blow the stack inside a render.
  const stack: Array<{ id: string; depth: number }> = [{ id: "", depth: 0 }];
  const seen = new Set<string>();
  while (stack.length > 0) {
    const frame = stack.pop();
    if (!frame) break;
    const children = byParent.get(frame.id) ?? [];
    for (const span of children) {
      // A cycle in parent ids is corrupt data, not a shape to render forever.
      if (seen.has(span.span_id)) continue;
      seen.add(span.span_id);
      nodes.push({ span, depth: frame.depth, share: span.duration_ms / total });
      if (frame.depth > maxDepth) maxDepth = frame.depth;
      stack.push({ id: span.span_id, depth: frame.depth + 1 });
    }
  }
  // Rows come out interleaved because the walk pushes subtrees; sorting by
  // depth then start gives the reading order back.
  nodes.sort((a, b) => a.depth - b.depth || a.span.start - b.span.start);
  return { nodes, maxDepth };
}

/** Fill for one span. Error and hot override the service band. */
function fillFor(node: FlameNode, services: string[]): string {
  if (node.span.status === "error") return "var(--crit)";
  if (node.share >= HOT_SHARE) return "var(--warn)";
  const idx = Math.max(services.indexOf(node.span.service), 0);
  return SERVICE_COLORS[idx % SERVICE_COLORS.length];
}

export interface FlameGraphProps {
  spans: Span[];
  selectedSpanId?: string;
  onSelect?: (span: Span) => void;
}

function FlameGraphInner({
  spans,
  selectedSpanId,
  onSelect,
  width,
}: FlameGraphProps & { width: number }) {
  const { nodes, maxDepth } = useMemo(() => layoutFlame(spans), [spans]);
  const [hovered, setHovered] = useState<FlameNode | null>(null);

  const services = useMemo(() => [...new Set(spans.map((s) => s.service))], [spans]);
  const traceStart = Math.min(...spans.map((s) => s.start));
  const traceEnd = Math.max(...spans.map((s) => s.start + s.duration_ms));
  const total = Math.max(traceEnd - traceStart, 1);

  const height = (maxDepth + 1) * (ROW_H + ROW_GAP);
  const x = scaleLinear<number>({ domain: [0, total], range: [0, Math.max(width, 1)] });

  return (
    <div className="flame">
      <svg width={width} height={height} role="img" aria-label="스팬 플레임그래프">
        {nodes.map((node) => {
          const left = x(node.span.start - traceStart);
          // A zero-duration span is a real event and must stay clickable.
          const w = Math.max(x(node.span.duration_ms), 2);
          const selected = node.span.span_id === selectedSpanId;
          const maxChars = Math.floor(w / CHAR_PX);
          const label =
            node.span.name.length > maxChars ? node.span.name.slice(0, maxChars) : node.span.name;
          return (
            <Group key={node.span.span_id} left={left} top={node.depth * (ROW_H + ROW_GAP)}>
              <rect
                className={selected ? "flame-rect is-selected" : "flame-rect"}
                width={w}
                height={ROW_H}
                rx={2}
                fill={fillFor(node, services)}
                onClick={() => onSelect?.(node.span)}
                onMouseEnter={() => setHovered(node)}
                onMouseLeave={() => setHovered(null)}
              >
                <title>
                  {node.span.service +
                    " / " +
                    node.span.name +
                    " / " +
                    formatDuration(node.span.duration_ms)}
                </title>
              </rect>
              {w >= MIN_LABEL_PX ? (
                <text
                  className="flame-label"
                  x={5}
                  y={ROW_H / 2 + 3.5}
                  // The rect is the hit target; the label must not swallow the
                  // click that selects the span.
                  pointerEvents="none"
                >
                  {label}
                </text>
              ) : null}
            </Group>
          );
        })}
      </svg>

      <div className="flame-foot">
        <span className="hint">
          {hovered
            ? hovered.span.service +
              " · " +
              hovered.span.name +
              " · " +
              formatDuration(hovered.span.duration_ms) +
              " (" +
              Math.round(hovered.share * 100) +
              "%)"
            : "전체 " +
              formatDuration(total) +
              " · 깊이 " +
              (maxDepth + 1) +
              " · 막대를 클릭하면 그 스팬의 로그·메트릭을 봅니다"}
        </span>
        <span className="flame-legend">
          {services.slice(0, SERVICE_COLORS.length).map((s, i) => (
            <span key={s}>
              <i
                className="swatch"
                style={{ background: SERVICE_COLORS[i % SERVICE_COLORS.length] }}
              />
              {s}
            </span>
          ))}
          <span>
            <i className="swatch" style={{ background: "var(--crit)" }} />
            오류
          </span>
          <span>
            <i className="swatch" style={{ background: "var(--warn)" }} />
            전체의 {Math.round(HOT_SHARE * 100)}% 이상
          </span>
        </span>
      </div>
    </div>
  );
}

/**
 * Flame graph, sized to its container.
 *
 * ParentSize rather than a fixed width because the x scale is the whole point:
 * a graph drawn 420px wide inside a 900px card would push every span into the
 * left half and make the short ones invisible.
 */
export function FlameGraph(props: FlameGraphProps) {
  if (props.spans.length === 0) return null;
  return (
    <ParentSize debounceTime={16}>
      {({ width }: { width: number }) =>
        width > 0 ? <FlameGraphInner {...props} width={width} /> : null
      }
    </ParentSize>
  );
}
