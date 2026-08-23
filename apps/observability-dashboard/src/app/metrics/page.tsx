"use client";

/**
 * Metrics Explorer [OBS-03].
 *
 * The query is structured because obsplane only accepts structured input: a
 * metric name, label matchers, an aggregation from a whitelist, and a step.
 * The rendered MetricsQL comes back in the response and is shown read-only, so
 * the org matcher obsplane injects is visible rather than merely promised.
 */

import { useCallback, useMemo, useState } from "react";
import { MetricsChart, ResolutionBadge } from "@/components/MetricsChart";
import { FilterEditor, QueryPeek, RangeSelect, StepSelect, windowFor } from "@/components/QueryControls";
import { ChartSkeleton, EmptyState, ErrorState } from "@/components/States";
import { useAsync } from "@/hooks/useAsync";
import { queryMetrics } from "@/lib/api/endpoints";
import { AGGREGATIONS, type Matcher } from "@/lib/api/types";
import { MOCK_METRICS } from "@/lib/mock/fixtures";
import { formatDateTime } from "@/lib/format";

const AGG_LABELS: Record<string, string> = {
  "": "없음 (원본 시계열)",
  rate: "rate (초당 증가율)",
  avg: "avg (구간 평균)",
  sum: "sum (구간 합)",
  count: "count (구간 개수)",
  p50: "p50 (중앙값)",
  p95: "p95",
  p99: "p99",
};

export default function MetricsPage() {
  const [metric, setMetric] = useState(MOCK_METRICS[0]);
  const [filters, setFilters] = useState<Matcher[]>([{ label: "service", value: "checkout-api" }]);
  const [agg, setAgg] = useState("p95");
  const [stepSec, setStepSec] = useState(60);
  const [rangeSec, setRangeSec] = useState(3600);

  // Applied state is separate from form state so the chart does not re-query on
  // every keystroke - obsplane range queries are not free.
  const [applied, setApplied] = useState({ metric, filters, agg, stepSec, rangeSec, nonce: 0 });

  const fetcher = useCallback(
    (signal: AbortSignal) => {
      const { from, to } = windowFor(applied.rangeSec);
      return queryMetrics({
        from,
        to,
        metric: applied.metric,
        filters: applied.filters,
        agg: applied.agg,
        stepSec: applied.stepSec,
        signal,
      });
    },
    [applied],
  );

  const { data, error, loading, refreshing, reload } = useAsync(fetcher, [applied]);

  const apply = () => setApplied((a) => ({ metric, filters, agg, stepSec, rangeSec, nonce: a.nonce + 1 }));

  const seriesCount = data?.series.length ?? 0;
  const pointCount = useMemo(
    () => (data?.series ?? []).reduce((n, s) => n + s.points.length, 0),
    [data],
  );

  return (
    <div className="view">
      <div className="page-head">
        <h1>메트릭 탐색</h1>
        <span className="page-sub">라벨 필터와 집계로 시계열을 조회합니다 · OBS-03</span>
        <div className="page-actions">
          <button type="button" className="btn btn-sm" onClick={reload} disabled={loading}>
            {refreshing ? "새로고침 중…" : "새로고침"}
          </button>
        </div>
      </div>

      <div className="card mb">
        <div className="toolbar">
          <div className="field" style={{ flex: "1 1 260px" }}>
            <label htmlFor="metric">메트릭</label>
            <input
              id="metric"
              className="input mono"
              list="metric-suggestions"
              value={metric}
              onChange={(e) => setMetric(e.target.value)}
              placeholder="http_server_duration_seconds"
            />
            <datalist id="metric-suggestions">
              {MOCK_METRICS.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
          </div>
          <div className="field">
            <label htmlFor="agg">집계</label>
            <select id="agg" className="input" value={agg} onChange={(e) => setAgg(e.target.value)}>
              {AGGREGATIONS.map((a) => (
                <option key={a || "none"} value={a}>
                  {AGG_LABELS[a]}
                </option>
              ))}
            </select>
          </div>
          <StepSelect value={stepSec} onChange={setStepSec} />
          <RangeSelect value={rangeSec} onChange={setRangeSec} />
          <button type="button" className="btn btn-primary" onClick={apply}>
            조회
          </button>
        </div>

        <FilterEditor
          filters={filters}
          onChange={setFilters}
          labelSuggestions={["service", "klaro_env", "host", "http_route", "status_code"]}
        />
        <p className="hint" style={{ marginTop: "var(--sp-2)" }}>
          집계를 고르면 스텝이 롤업 창이 됩니다. 원시 MetricsQL은 서버가 조직 라벨을 강제로 주입하기 위해
          받지 않습니다.
        </p>
      </div>

      <div className="card">
        <div className="card-head">
          <h2>{metric || "시계열"}</h2>
          <span className="spacer" />
          {data ? (
            <>
              <ResolutionBadge resolution={data.resolution} clamped={data.clamped} />
              <span className="badge badge-mute">
                시계열 {seriesCount}개 · 포인트 {pointCount}개
              </span>
            </>
          ) : null}
        </div>

        {error ? <ErrorState error={error} onRetry={reload} /> : null}

        {!error && loading ? <ChartSkeleton /> : null}

        {!error && !loading && data && seriesCount === 0 ? (
          <EmptyState
            title="이 조건에 해당하는 시계열이 없습니다"
            description="0이라는 뜻이 아니라, 이 구간·라벨 조합으로 저장된 데이터가 없다는 뜻입니다. 구간을 넓히거나 필터를 줄여보세요."
          />
        ) : null}

        {!error && data && seriesCount > 0 ? (
          <>
            <div style={{ opacity: refreshing ? 0.6 : 1, transition: "opacity .2s" }}>
              <MetricsChart series={data.series} />
            </div>
            <p className="hint" style={{ marginTop: "var(--sp-2)" }}>
              조회 구간 {formatDateTime(data.from)} ~ {formatDateTime(data.to)}
            </p>
            <QueryPeek query={data.query} label="서버가 생성한 MetricsQL" />
          </>
        ) : null}
      </div>
    </div>
  );
}
