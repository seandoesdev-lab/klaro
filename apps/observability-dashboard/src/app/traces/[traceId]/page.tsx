"use client";

/**
 * One trace as a span waterfall [OBS-04] (05 section 4.6).
 */

import Link from "next/link";
import { useParams } from "next/navigation";
import { useCallback } from "react";
import { TraceWaterfall } from "@/components/TraceWaterfall";
import { EmptyState, ErrorState, TableSkeleton } from "@/components/States";
import { useAsync } from "@/hooks/useAsync";
import { getTrace } from "@/lib/api/endpoints";
import { formatDateTime, formatDuration } from "@/lib/format";

export default function TraceDetailPage() {
  const params = useParams<{ traceId: string }>();
  const traceId = params?.traceId ?? "";

  const fetcher = useCallback((signal: AbortSignal) => getTrace(traceId, signal), [traceId]);
  const { data, error, loading, reload } = useAsync(fetcher, [traceId], { enabled: traceId !== "" });

  const spans = data?.spans ?? [];
  const total =
    spans.length > 0
      ? Math.max(...spans.map((s) => s.start + s.duration_ms)) - Math.min(...spans.map((s) => s.start))
      : 0;
  const errored = spans.filter((s) => s.status === "error").length;
  const services = new Set(spans.map((s) => s.service)).size;

  return (
    <div className="view">
      <div className="crumbs">
        <Link href="/traces">트레이스</Link>
        <span className="sep">/</span>
        <span className="num">{traceId.slice(0, 24)}…</span>
      </div>

      <div className="page-head">
        <h1>트레이스 워터폴</h1>
        <span className="page-sub num">{traceId}</span>
        <div className="page-actions">
          <button type="button" className="btn btn-sm" onClick={reload} disabled={loading}>
            새로고침
          </button>
        </div>
      </div>

      {error ? <ErrorState error={error} onRetry={reload} /> : null}

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
              <span className="microlabel">스팬</span>
            </div>
            <div className="kpi-value">{spans.length}</div>
          </div>
          <div className="kpi">
            <div className="kpi-top">
              <span className="microlabel">서비스</span>
            </div>
            <div className="kpi-value">{services}</div>
          </div>
          <div className={errored > 0 ? "kpi is-crit" : "kpi is-good"}>
            <div className="kpi-top">
              <span className="microlabel">오류 스팬</span>
              <span className={"badge " + (errored > 0 ? "badge-crit" : "badge-good")}>
                {errored > 0 ? "✕ 오류" : "✓ 정상"}
              </span>
            </div>
            <div className="kpi-value">{errored}</div>
          </div>
        </div>
      ) : null}

      <div className="card">
        <div className="card-head">
          <h2>스팬</h2>
          <span className="spacer" />
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

        {spans.length > 0 ? <TraceWaterfall spans={spans} /> : null}
      </div>

      {spans.length > 0 ? (
        <div className="card mt">
          <h2>스팬 상세</h2>
          <p className="card-sub">시작 시각 순서입니다.</p>
          <div className="tbl-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>이름</th>
                  <th>서비스</th>
                  <th>상태</th>
                  <th className="ta-r">소요</th>
                  <th>시작</th>
                </tr>
              </thead>
              <tbody>
                {spans
                  .slice()
                  .sort((a, b) => a.start - b.start)
                  .map((s) => (
                    <tr key={s.span_id}>
                      <td className="cell-main">{s.name}</td>
                      <td className="cell-sub">{s.service}</td>
                      <td>
                        <span className={"badge " + (s.status === "error" ? "badge-crit" : "badge-good")}>
                          {s.status === "error" ? "✕ error" : "✓ " + (s.status || "ok")}
                        </span>
                      </td>
                      <td className="ta-r num">{formatDuration(s.duration_ms)}</td>
                      <td className="cell-sub">{formatDateTime(s.start)}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        </div>
      ) : null}
    </div>
  );
}
