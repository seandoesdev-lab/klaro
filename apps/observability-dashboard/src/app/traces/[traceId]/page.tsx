"use client";

/**
 * One trace, three signals [OBS-04, APM-03] (05 section 4.6).
 *
 * The page fetches the correlated endpoint rather than the plain trace, because
 * the spans are the cheap part: the reason to open a trace is to find out what
 * the slow span was doing, and that answer is in its logs and in its host's
 * metrics. One request returns all three over a window derived from the spans
 * server side, so selecting a span is a filter rather than a round trip.
 *
 * Two views of the same spans sit behind a toggle instead of one being chosen
 * for the user, because they answer different questions. The waterfall gives
 * every span its own row and its own offset, which is what you want when reading
 * a short trace line by line; the flame graph puts depth on the y axis, which is
 * the only form that stays legible at two hundred spans.
 */

import Link from "next/link";
import { useParams } from "next/navigation";
import { useCallback, useMemo, useState } from "react";
import { FlameGraph } from "@/components/FlameGraph";
import { SpanCorrelation } from "@/components/SpanCorrelation";
import { Banner, EmptyState, ErrorState, TableSkeleton } from "@/components/States";
import { TraceWaterfall } from "@/components/TraceWaterfall";
import { useAsync } from "@/hooks/useAsync";
import { getCorrelated } from "@/lib/api/endpoints";
import type { Span } from "@/lib/api/types";
import { formatDateTime, formatDuration } from "@/lib/format";

type View = "flame" | "waterfall";

/** One failing operation, however many spans it produced. */
interface ErrorGroup {
  key: string;
  service: string;
  name: string;
  count: number;
  /** Slowest instance: the one worth opening first. */
  worst: Span;
}

/**
 * Error spans, grouped by the operation that failed.
 *
 * Grouped rather than listed because a fan-out failure is the common shape: one
 * broken dependency called forty times is forty error spans and one problem, and
 * a flat list of forty rows hides that it is one problem.
 */
function groupErrors(spans: Span[]): ErrorGroup[] {
  const groups = new Map<string, ErrorGroup>();
  for (const span of spans) {
    if (span.status !== "error") continue;
    const key = span.service + " " + span.name;
    const existing = groups.get(key);
    if (!existing) {
      groups.set(key, { key, service: span.service, name: span.name, count: 1, worst: span });
      continue;
    }
    existing.count += 1;
    if (span.duration_ms > existing.worst.duration_ms) existing.worst = span;
  }
  return [...groups.values()].sort(
    (a, b) => b.count - a.count || b.worst.duration_ms - a.worst.duration_ms,
  );
}

export default function TraceDetailPage() {
  const params = useParams<{ traceId: string }>();
  const traceId = params?.traceId ?? "";

  const [view, setView] = useState<View>("flame");
  const [selectedSpanId, setSelectedSpanId] = useState<string | undefined>(undefined);

  const fetcher = useCallback((signal: AbortSignal) => getCorrelated(traceId, { signal }), [traceId]);
  const { data, error, loading, refreshing, reload } = useAsync(fetcher, [traceId], {
    enabled: traceId !== "",
  });

  const spans = data?.spans ?? [];
  const selected = useMemo(
    () => spans.find((s) => s.span_id === selectedSpanId) ?? null,
    [spans, selectedSpanId],
  );
  const errorGroups = useMemo(() => groupErrors(spans), [spans]);

  const total =
    spans.length > 0
      ? Math.max(...spans.map((s) => s.start + s.duration_ms)) -
        Math.min(...spans.map((s) => s.start))
      : 0;
  const errored = spans.filter((s) => s.status === "error").length;
  const services = new Set(spans.map((s) => s.service)).size;

  return (
    <div className="view view-wide">
      <div className="crumbs">
        <Link href="/traces">트레이스</Link>
        <span className="sep">/</span>
        <span className="num">{traceId.slice(0, 24)}…</span>
      </div>

      <div className="page-head">
        <h1>트레이스 연계 분석</h1>
        <span className="page-sub num">{traceId}</span>
        <div className="page-actions">
          <button type="button" className="btn btn-sm" onClick={reload} disabled={loading}>
            {refreshing ? "새로고침 중…" : "새로고침"}
          </button>
        </div>
      </div>

      {error ? <ErrorState error={error} onRetry={reload} /> : null}

      {/*
        Notes are not decoration. obsplane answers with an empty log list plus a
        note both when the logs backend is down and when nothing carries the
        trace id, so a page that dropped them would render "no logs" for both.
      */}
      {(data?.notes ?? []).map((note) => (
        <Banner key={note} tone="warn" title="일부 신호를 읽지 못했습니다" description={note} />
      ))}

      {!error && data ? (
        <div className="kpi-grid">
          <div className="kpi is-accent">
            <div className="kpi-top">
              <span className="microlabel">전체 소요</span>
            </div>
            <div className="kpi-value">{formatDuration(total)}</div>
          </div>
          <div className="kpi">
            <div className="kpi-top">
              <span className="microlabel">스팬 · 서비스</span>
            </div>
            <div className="kpi-value">
              {spans.length}
              <span className="kpi-unit">/ {services}</span>
            </div>
          </div>
          <div className="kpi">
            <div className="kpi-top">
              <span className="microlabel">연계 로그</span>
            </div>
            <div className="kpi-value">{data.logs.length}</div>
            <div className="kpi-trend">{data.metrics.length}개 메트릭 시리즈</div>
          </div>
          <div className={errored > 0 ? "kpi is-crit" : "kpi is-good"}>
            <div className="kpi-top">
              <span className="microlabel">오류 스팬</span>
              <span className={"badge " + (errored > 0 ? "badge-crit" : "badge-good")}>
                {errored > 0 ? "✕ 오류" : "✓ 정상"}
              </span>
            </div>
            <div className="kpi-value">{errored}</div>
            {errorGroups.length > 0 ? (
              <div className="kpi-trend">{errorGroups.length}개 지점</div>
            ) : null}
          </div>
        </div>
      ) : null}

      {errorGroups.length > 0 ? (
        <div className="card mb">
          <div className="card-head">
            <h2>오류 지점</h2>
            <span className="page-sub">같은 작업의 반복 실패는 하나로 묶었습니다</span>
          </div>
          <div className="err-groups">
            {errorGroups.map((g) => (
              <button
                type="button"
                key={g.key}
                className={g.worst.span_id === selectedSpanId ? "err-group is-selected" : "err-group"}
                onClick={() => setSelectedSpanId(g.worst.span_id)}
              >
                <span className="badge badge-crit">✕ {g.count}회</span>
                <span className="cell-main">{g.name}</span>
                <span className="cell-sub">{g.service}</span>
                <span className="num" style={{ marginLeft: "auto" }}>
                  {formatDuration(g.worst.duration_ms)}
                </span>
              </button>
            ))}
          </div>
        </div>
      ) : null}

      <div className="card mb">
        <div className="card-head">
          <h2>스팬</h2>
          <span className="spacer" />
          <div className="subtabs" style={{ margin: 0, borderBottom: 0 }}>
            <button
              type="button"
              className="subtab"
              aria-current={view === "flame"}
              onClick={() => setView("flame")}
            >
              플레임그래프
            </button>
            <button
              type="button"
              className="subtab"
              aria-current={view === "waterfall"}
              onClick={() => setView("waterfall")}
            >
              워터폴
            </button>
          </div>
        </div>

        {loading ? <TableSkeleton rows={6} /> : null}

        {!loading && !error && spans.length === 0 ? (
          <EmptyState
            title="이 트레이스에 스팬이 없습니다"
            description="보존 기간이 지났거나, 다른 조직의 트레이스일 수 있습니다."
            action={
              <Link href="/traces" className="btn">
                목록으로
              </Link>
            }
          />
        ) : null}

        {spans.length > 0 && view === "flame" ? (
          <FlameGraph
            spans={spans}
            selectedSpanId={selectedSpanId}
            onSelect={(s) => setSelectedSpanId(s.span_id)}
          />
        ) : null}

        {spans.length > 0 && view === "waterfall" ? (
          <TraceWaterfall
            spans={spans}
            selectedSpanId={selectedSpanId}
            onSelect={(s) => setSelectedSpanId(s.span_id)}
          />
        ) : null}
      </div>

      {data && spans.length > 0 ? (
        <div className="grid-32">
          <SpanCorrelation correlated={data} span={selected} />

          <div className="stack">
            <div className="card">
              <div className="card-head">
                <h2>서비스·호스트</h2>
                <span className="page-sub">메트릭 연계에 쓰인 범위</span>
              </div>
              <div className="tbl-wrap">
                <table className="tbl">
                  <thead>
                    <tr>
                      <th>서비스</th>
                      <th>호스트</th>
                      <th className="ta-r">스팬</th>
                      <th className="ta-r">소요 합</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.scopes.map((scope) => (
                      <tr key={scope.service + (scope.host ?? "")}>
                        <td className="cell-main">{scope.service}</td>
                        <td className="cell-sub">{scope.host ?? "-"}</td>
                        <td className="ta-r num">
                          {scope.spans}
                          {scope.errors > 0 ? (
                            <span className="badge badge-crit" style={{ marginLeft: 6 }}>
                              {scope.errors}
                            </span>
                          ) : null}
                        </td>
                        <td className="ta-r num">{formatDuration(scope.duration_ms)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <p className="hint" style={{ marginTop: "var(--sp-2)" }}>
                조회 구간 {formatDateTime(data.from)} ~ {formatDateTime(data.to)}
              </p>
            </div>

            <div className="card">
              <div className="card-head">
                <h2>스팬 목록</h2>
                <span className="page-sub">시작 시각 순</span>
              </div>
              <div className="tbl-wrap">
                <table className="tbl">
                  <thead>
                    <tr>
                      <th>이름</th>
                      <th>서비스</th>
                      <th className="ta-r">소요</th>
                    </tr>
                  </thead>
                  <tbody>
                    {spans
                      .slice()
                      .sort((a, b) => a.start - b.start)
                      .map((s) => (
                        <tr
                          key={s.span_id}
                          className="rowlink"
                          aria-selected={s.span_id === selectedSpanId}
                          onClick={() => setSelectedSpanId(s.span_id)}
                        >
                          <td className="cell-main">
                            {s.status === "error" ? (
                              <span className="badge badge-crit" style={{ marginRight: 6 }}>
                                ✕
                              </span>
                            ) : null}
                            {s.name}
                          </td>
                          <td className="cell-sub">{s.service}</td>
                          <td className="ta-r num">{formatDuration(s.duration_ms)}</td>
                        </tr>
                      ))}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
