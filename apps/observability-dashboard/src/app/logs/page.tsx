"use client";

/**
 * Logs Explorer [OBS-05].
 *
 * The search box is a literal substring, not a regex and not LogQL: obsplane
 * refuses a caller-written pipeline because a pipeline stage can carry a label
 * matcher (internal/explorer/logs.go). The placeholder says so, rather than
 * letting someone discover it through a 422.
 */

import { useCallback, useMemo, useState } from "react";
import { LogList } from "@/components/LogList";
import { FilterEditor, QueryPeek, RangeSelect, windowFor } from "@/components/QueryControls";
import { EmptyState, ErrorState, TableSkeleton } from "@/components/States";
import { useAsync } from "@/hooks/useAsync";
import { queryLogs } from "@/lib/api/endpoints";
import type { Matcher } from "@/lib/api/types";

const LEVELS = ["", "ERROR", "WARN", "INFO", "DEBUG"];

export default function LogsPage() {
  const [contains, setContains] = useState("");
  const [level, setLevel] = useState("");
  const [filters, setFilters] = useState<Matcher[]>([{ label: "service", value: "" }]);
  const [rangeSec, setRangeSec] = useState(3600);
  const [limit, setLimit] = useState(100);
  const [applied, setApplied] = useState({ contains, level, filters, rangeSec, limit, nonce: 0 });

  const fetcher = useCallback(
    (signal: AbortSignal) => {
      const { from, to } = windowFor(applied.rangeSec);
      // The level dropdown is just another label matcher; keeping it out of the
      // filter editor is a convenience, not a different mechanism.
      const all = applied.level
        ? [...applied.filters, { label: "level", value: applied.level }]
        : applied.filters;
      return queryLogs({
        from,
        to,
        filters: all,
        contains: applied.contains || undefined,
        limit: applied.limit,
        signal,
      });
    },
    [applied],
  );

  const { data, error, loading, refreshing, reload } = useAsync(fetcher, [applied]);
  const entries = data?.data ?? [];

  const counts = useMemo(() => {
    const out = { ERROR: 0, WARN: 0, INFO: 0 } as Record<string, number>;
    for (const e of entries) {
      const l = (e.level || "INFO").toUpperCase();
      if (l in out) out[l] += 1;
    }
    return out;
  }, [entries]);

  const apply = () => setApplied((a) => ({ contains, level, filters, rangeSec, limit, nonce: a.nonce + 1 }));

  return (
    <div className="view">
      <div className="page-head">
        <h1>로그 탐색</h1>
        <span className="page-sub">라벨과 문자열 포함 조건으로 로그를 조회합니다 · OBS-05</span>
        <div className="page-actions">
          <button type="button" className="btn btn-sm" onClick={reload} disabled={loading}>
            {refreshing ? "새로고침 중…" : "새로고침"}
          </button>
        </div>
      </div>

      <div className="card mb">
        {/* Not a <form>: every other view applies with an explicit button, and
            a submit-driven one did not reliably commit state under the App
            Router. Enter still searches, via the field's own key handler. */}
        <div className="toolbar">
          <div className="field" style={{ flex: "1 1 300px" }}>
            <label htmlFor="contains">문자열 포함</label>
            <input
              id="contains"
              className="input mono"
              type="search"
              value={contains}
              onChange={(e) => setContains(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") apply();
              }}
              placeholder="timeout (정규식·LogQL 아님, 단순 포함)"
            />
          </div>
          <div className="field">
            <label htmlFor="level">레벨</label>
            <select id="level" className="input" value={level} onChange={(e) => setLevel(e.target.value)}>
              {LEVELS.map((l) => (
                <option key={l || "all"} value={l}>
                  {l || "전체"}
                </option>
              ))}
            </select>
          </div>
          <RangeSelect value={rangeSec} onChange={setRangeSec} />
          <div className="field">
            <label htmlFor="limit">최대 줄 수</label>
            <input
              id="limit"
              className="input mono"
              type="number"
              min={1}
              max={1000}
              step={50}
              style={{ width: 110 }}
              value={limit}
              onChange={(e) => setLimit(Math.max(1, Number(e.target.value)))}
            />
          </div>
          <button type="button" className="btn btn-primary" onClick={apply}>
            조회
          </button>
        </div>

        <FilterEditor
          filters={filters}
          onChange={setFilters}
          labelSuggestions={["service", "klaro_env", "host", "level"]}
        />
      </div>

      <div className="card">
        <div className="card-head">
          <h2>로그 라인</h2>
          <span className="spacer" />
          {entries.length > 0 ? (
            <>
              <span className="badge badge-crit">ERROR {counts.ERROR}</span>
              <span className="badge badge-warn">WARN {counts.WARN}</span>
              <span className="badge badge-mute">INFO {counts.INFO}</span>
            </>
          ) : null}
        </div>

        {error ? <ErrorState error={error} onRetry={reload} /> : null}
        {!error && loading ? <TableSkeleton rows={10} /> : null}

        {!error && !loading && entries.length === 0 ? (
          <EmptyState
            title="이 조건에 해당하는 로그가 없습니다"
            description="구간을 넓히거나 포함 문자열을 지워보세요. 로그가 하나도 없다면 SDK의 로그 브리지가 켜져 있는지 확인하세요."
          />
        ) : null}

        {!error && entries.length > 0 ? (
          <div style={{ opacity: refreshing ? 0.6 : 1, transition: "opacity .2s" }}>
            <LogList entries={entries} />
            {data?.next ? (
              <p className="hint" style={{ marginTop: "var(--sp-2)" }}>
                더 오래된 줄이 남아 있습니다. 구간을 좁혀서 다시 조회하세요.
              </p>
            ) : null}
          </div>
        ) : null}

        {data ? <QueryPeek query={data.query} label="서버가 생성한 LogQL" /> : null}
      </div>
    </div>
  );
}
