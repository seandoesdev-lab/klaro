/**
 * Span waterfall for one trace (05 section 4.6, [APM-03]).
 *
 * Offsets, not durations, are the picture: the point of a waterfall is seeing
 * that the slow child started late, so each bar is positioned by
 * (span.start - traceStart) and sized by duration.
 *
 * Rows are ordered depth-first from the root rather than by start time, so a
 * child always sits under its parent even when a sibling started earlier.
 */

import type { Span } from "@/lib/api/types";
import { formatDuration } from "@/lib/format";

/** A span is "hot" when it dominates the trace and is worth the eye going to. */
const HOT_SHARE = 0.35;

function orderDepthFirst(spans: Span[]): Array<{ span: Span; depth: number }> {
  const byParent = new Map<string, Span[]>();
  const ids = new Set(spans.map((s) => s.span_id));

  for (const s of spans) {
    // A span whose parent is not in this response is a root as far as the
    // waterfall is concerned - dropping it would hide real work.
    const parent = s.parent_span_id && ids.has(s.parent_span_id) ? s.parent_span_id : "";
    const list = byParent.get(parent);
    if (list) list.push(s);
    else byParent.set(parent, [s]);
  }
  for (const list of byParent.values()) list.sort((a, b) => a.start - b.start);

  const out: Array<{ span: Span; depth: number }> = [];
  const walk = (parent: string, depth: number) => {
    for (const s of byParent.get(parent) ?? []) {
      out.push({ span: s, depth });
      walk(s.span_id, depth + 1);
    }
  };
  walk("", 0);
  return out;
}

export function TraceWaterfall({ spans }: { spans: Span[] }) {
  if (spans.length === 0) return null;

  const traceStart = Math.min(...spans.map((s) => s.start));
  const traceEnd = Math.max(...spans.map((s) => s.start + s.duration_ms));
  const total = Math.max(traceEnd - traceStart, 1);
  const ordered = orderDepthFirst(spans);

  return (
    <div className="chart-wrap">
      <div className="wf" role="list" aria-label="스팬 워터폴">
        {ordered.map(({ span, depth }) => {
          const left = ((span.start - traceStart) / total) * 100;
          const width = Math.max((span.duration_ms / total) * 100, 0.4);
          const hot = span.status === "error" || span.duration_ms / total >= HOT_SHARE;
          return (
            <div className="wf-row" key={span.span_id} role="listitem">
              <span className="wf-name" title={span.service + " · " + span.name}>
                <span style={{ paddingLeft: depth * 12 }}>
                  {span.name} <span className="wf-svc">{span.service}</span>
                </span>
              </span>
              <span className="wf-track">
                <span
                  className={hot ? "wf-bar hot" : "wf-bar"}
                  style={{ left: left + "%", width: width + "%" }}
                />
              </span>
              <span className={hot ? "wf-ms hot" : "wf-ms"}>{formatDuration(span.duration_ms)}</span>
            </div>
          );
        })}
      </div>
      <p className="hint" style={{ marginTop: "var(--sp-2)" }}>
        전체 {formatDuration(total)} · 스팬 {spans.length}개 · 빨간 막대는 오류이거나 전체의{" "}
        {Math.round(HOT_SHARE * 100)}% 이상을 차지한 구간입니다.
      </p>
    </div>
  );
}
