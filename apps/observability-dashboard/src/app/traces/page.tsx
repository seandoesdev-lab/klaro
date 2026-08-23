"use client";

/**
 * Traces Explorer [OBS-04].
 *
 * The list is sorted by duration rather than recency by default: the reason to
 * open this screen is a slow request, and 05 section 4.6 makes the >=3s
 * transaction the thing worth finding ([APM-03]).
 */

import Link from "next/link";
import { useCallback, useState } from "react";
import { RangeSelect, QueryPeek, windowFor } from "@/components/QueryControls";
import { EmptyState, ErrorState, TableSkeleton } from "@/components/States";
import { useAsync } from "@/hooks/useAsync";
import { searchTraces } from "@/lib/api/endpoints";
import { formatDateTime, formatDuration } from "@/lib/format";
import { MOCK_SERVICES } from "@/lib/mock/fixtures";

/** The latency band the report treats as a bottleneck ([APM-03]). */
const SLOW_MS = 3000;
const WARN_MS = 1000;

type SortKey = "duration" | "start";

export default function TracesPage() {
  const [service, setService] = useState("");
  const [minDurationMs, setMinDurationMs] = useState(0);
  const [rangeSec, setRangeSec] = useState(3600);
  const [sort, setSort] = useState<SortKey>("duration");
  const [applied, setApplied] = useState({ service, minDurationMs, rangeSec, nonce: 0 });

  const fetcher = useCallback(
    (signal: AbortSignal) => {
      const { from, to } = windowFor(applied.rangeSec);
      return searchTraces({
        from,
        to,
        service: applied.service || undefined,
        minDurationMs: applied.minDurationMs,
        limit: 100,
        signal,
      });
    },
    [applied],
  );

  const { data, error, loading, refreshing, reload } = useAsync(fetcher, [applied]);

  const rows = (data?.data ?? [])
    .slice()
    .sort((a, b) => (sort === "duration" ? b.duration_ms - a.duration_ms : b.start - a.start));

  return (
    <div className="view">
      <div className="page-head">
        <h1>트레이스 탐색</h1>
        <span className="page-sub">느린 트랜잭션을 찾아 스팬 워터폴로 병목을 확인합니다 · OBS-04</span>
        <div className="page-actions">
          <button type="button" className="btn btn-sm" onClick={reload} disabled={loading}>
            {refreshing ? "새로고침 중…" : "새로고침"}
          </button>
        </div>
      </div>

      <div className="card mb">
        <div className="toolbar" style={{ marginBottom: 0 }}>
          <div className="field" style={{ flex: "1 1 220px" }}>
            <label htmlFor="service">서비스</label>
            <input
              id="service"
              className="input mono"
              list="service-suggestions"
              value={service}
              onChange={(e) => setService(e.target.value)}
              placeholder="전체"
            />
            <datalist id="service-suggestions">
              {MOCK_SERVICES.map((s) => (
                <option key={s} value={s} />
              ))}
            </datalist>
          </div>
          <div className="field">
            <label htmlFor="minDur">최소 소요(ms)</label>
            <input
              id="minDur"
              className="input mono"
              type="number"
              min={0}
              step={100}
              style={{ width: 130 }}
              value={minDurationMs}
              onChange={(e) => setMinDurationMs(Math.max(0, Number(e.target.value)))}
            />
          </div>
          <RangeSelect value={rangeSec} onChange={setRangeSec} />
          <div className="field">
            <label htmlFor="sort">정렬</label>
            <select id="sort" className="input" value={sort} onChange={(e) => setSort(e.target.value as SortKey)}>
              <option value="duration">소요 시간 긴 순</option>
              <option value="start">최근 순</option>
            </select>
          </div>
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => setApplied((a) => ({ service, minDurationMs, rangeSec, nonce: a.nonce + 1 }))}
          >
            조회
          </button>
          <button type="button" className="btn" onClick={() => setMinDurationMs(SLOW_MS)}>
            3초 이상만
          </button>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <h2>트레이스</h2>
          <span className="spacer" />
          {data ? <span className="badge badge-mute">{rows.length}건</span> : null}
        </div>

        {error ? <ErrorState error={error} onRetry={reload} /> : null}
        {!error && loading ? <TableSkeleton rows={8} /> : null}

        {!error && !loading && rows.length === 0 ? (
          <EmptyState
            title="조건에 맞는 트레이스가 없습니다"
            description="구간을 넓히거나 최소 소요 시간을 낮춰보세요. 서비스에 SDK가 설치되어 있어야 트레이스가 수집됩니다."
          />
        ) : null}

        {!error && rows.length > 0 ? (
          <div className="tbl-wrap" style={{ opacity: refreshing ? 0.6 : 1, transition: "opacity .2s" }}>
            <table className="tbl">
              <thead>
                <tr>
                  <th>루트 스팬</th>
                  <th>서비스</th>
                  <th className="ta-r">소요</th>
                  <th>시작</th>
                  <th>trace id</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((t) => {
                  const tone = t.duration_ms >= SLOW_MS ? "crit" : t.duration_ms >= WARN_MS ? "warn" : "good";
                  return (
                    <tr key={t.trace_id} className="rowlink">
                      <td>
                        <Link href={"/traces/" + t.trace_id} className="cell-main" style={{ textDecoration: "none" }}>
                          {t.root_name || "(이름 없음)"}
                        </Link>
                      </td>
                      <td className="cell-sub">{t.root_service}</td>
                      <td className="ta-r">
                        <span className={"badge badge-" + tone}>
                          <span className={"dot dot-" + tone} aria-hidden="true" />
                          {formatDuration(t.duration_ms)}
                        </span>
                      </td>
                      <td className="cell-sub">{formatDateTime(t.start)}</td>
                      <td className="cell-sub">{t.trace_id.slice(0, 16)}…</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : null}

        {data ? <QueryPeek query={data.query} label="서버가 생성한 TraceQL" /> : null}
      </div>
    </div>
  );
}
