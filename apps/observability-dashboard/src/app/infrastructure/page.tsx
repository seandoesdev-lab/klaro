"use client";

/**
 * Infrastructure [OBS-01] - the fleet view.
 *
 * Three panels answering three different questions from the same data:
 *
 *   the SLO widget   did we meet the objective over this window?
 *   the hostmap      which machine is hot right now?
 *   the table        what exactly is that machine doing?
 *
 * Selecting a cell or a row pins one host and swaps the table for its detail
 * charts. Selection is shared between the map and the table rather than
 * duplicated: they are two renderings of one list, and a map highlighting a
 * different host than the table would be worse than having only one of them.
 */

import { useCallback, useState } from "react";
import { HostMap, HostMapLegend, HEAT_LABELS, type HeatMetric } from "@/components/HostMap";
import { QueryPeek, RangeSelect, windowFor } from "@/components/QueryControls";
import { Banner, ChartSkeleton, EmptyState, ErrorState, TableSkeleton } from "@/components/States";
import { TimeSeriesChart, type TimeSeriesLine } from "@/components/TimeSeriesChart";
import { UptimeSLOWidget } from "@/components/UptimeSLO";
import { useAsync } from "@/hooks/useAsync";
import { getUptime, listHosts, queryHostSeries } from "@/lib/api/endpoints";
import type { Host, HostSeriesKey } from "@/lib/api/types";
import { formatAgo, formatBytes, formatMetric } from "@/lib/format";

/** SLO objectives an operator would actually pick. */
const TARGETS = [
  { label: "99%", value: 0.99 },
  { label: "99.5%", value: 0.995 },
  { label: "99.9%", value: 0.999 },
];

/** Detail charts, grouped so series that share a unit share an axis. */
const DETAIL_GROUPS: Array<{ title: string; bytes?: boolean; keys: HostSeriesKey[] }> = [
  { title: "CPU · 메모리 · 디스크 사용률", keys: ["cpu_pct", "mem_pct", "disk_pct"] },
  { title: "로드 애버리지 (1m)", keys: ["load1"] },
  { title: "네트워크 처리량", bytes: true, keys: ["net_rx_bps", "net_tx_bps"] },
  { title: "디스크 처리량", bytes: true, keys: ["disk_read_bps", "disk_write_bps"] },
];

const SERIES_LABELS: Record<HostSeriesKey, string> = {
  cpu_pct: "CPU %",
  mem_pct: "메모리 %",
  disk_pct: "디스크 %",
  load1: "load1",
  net_rx_bps: "수신 B/s",
  net_tx_bps: "송신 B/s",
  disk_read_bps: "읽기 B/s",
  disk_write_bps: "쓰기 B/s",
};

const SERIES_COLORS: Record<HostSeriesKey, string> = {
  cpu_pct: "var(--s-p95)",
  mem_pct: "var(--s-p50)",
  disk_pct: "var(--s-p99)",
  load1: "var(--warn)",
  net_rx_bps: "var(--s-p50)",
  net_tx_bps: "var(--s-p99)",
  disk_read_bps: "var(--s-p95)",
  disk_write_bps: "var(--sev-high)",
};

/** Mean of the readings that exist, or null when none do. */
function meanOf(hosts: Host[], key: "cpu_pct" | "mem_pct"): number | null {
  const values = hosts.map((h) => h[key]).filter((v): v is number => v != null);
  if (values.length === 0) return null;
  return values.reduce((a, b) => a + b, 0) / values.length;
}

/**
 * Inline utilisation bar for a table cell.
 *
 * A number and a bar together, not one or the other: the bar makes the column
 * scannable, the number keeps it precise. A null reading renders as a dash with
 * no bar - an empty bar would read as zero.
 */
function UsageCell({ value }: { value: number | null }) {
  if (value == null) return <span className="cell-sub">-</span>;
  const tone = value >= 90 ? "crit" : value >= 75 ? "warn" : value >= 55 ? "accent" : "good";
  return (
    <span className="usage">
      <span className="usage-bar">
        <span className={"usage-fill is-" + tone} style={{ width: Math.min(100, value) + "%" }} />
      </span>
      <span className="num usage-num">{value.toFixed(1)}%</span>
    </span>
  );
}

function HostTable({
  hosts,
  selected,
  onSelect,
}: {
  hosts: Host[];
  selected?: string;
  onSelect: (ident: string) => void;
}) {
  return (
    <div className="tbl-wrap">
      <table className="tbl">
        <thead>
          <tr>
            <th>호스트</th>
            <th>서비스 / 환경</th>
            <th>상태</th>
            <th style={{ width: 168 }}>CPU</th>
            <th style={{ width: 168 }}>메모리</th>
            <th className="ta-r">load1</th>
            <th className="ta-r">마지막 보고</th>
          </tr>
        </thead>
        <tbody>
          {hosts.map((h) => (
            <tr
              key={h.host_ident}
              className="rowlink"
              aria-selected={h.host_ident === selected}
              onClick={() => onSelect(h.host_ident)}
            >
              <td>
                <span className="cell-main mono">{h.host_ident}</span>
              </td>
              <td>
                <span className="cell-sub">
                  {h.service || "-"}
                  {h.env ? " · " + h.env : ""}
                </span>
              </td>
              <td>
                <span className={h.status === "up" ? "badge badge-good" : "badge badge-mute"}>
                  <i className={h.status === "up" ? "dot dot-good" : "dot dot-mute"} />
                  {h.status === "up" ? "보고 중" : "보고 끊김"}
                </span>
              </td>
              <td>
                <UsageCell value={h.cpu_pct} />
              </td>
              <td>
                <UsageCell value={h.mem_pct} />
              </td>
              <td className="ta-r num">{h.load1 != null ? formatMetric(h.load1) : "-"}</td>
              <td className="ta-r cell-sub">{formatAgo(h.last_seen_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function HostDetail({
  host,
  rangeSec,
  onClose,
}: {
  host: Host;
  rangeSec: number;
  onClose: () => void;
}) {
  // Step follows the window so a day-long view does not ask for 2880 points per
  // series just to draw a 600px chart.
  const stepSec = rangeSec <= 3600 ? 30 : rangeSec <= 86400 ? 300 : 3600;

  const fetcher = useCallback(
    (signal: AbortSignal) => {
      const { from, to } = windowFor(rangeSec);
      return queryHostSeries({ hostIdent: host.host_ident, from, to, stepSec, signal });
    },
    [host.host_ident, rangeSec, stepSec],
  );
  const { data, error, loading, reload } = useAsync(fetcher, [host.host_ident, rangeSec, stepSec]);

  return (
    <div className="card">
      <div className="card-head">
        <h2 className="mono">{host.host_ident}</h2>
        <span className="badge badge-mute">{host.service || "service 미상"}</span>
        {host.env ? <span className="badge badge-accent">{host.env}</span> : null}
        <span className="spacer" />
        <button type="button" className="btn btn-sm" onClick={onClose}>
          목록으로
        </button>
      </div>

      {error ? <ErrorState error={error} onRetry={reload} /> : null}
      {!error && loading ? <ChartSkeleton /> : null}

      {!error && data
        ? DETAIL_GROUPS.map((group) => {
            const lines: TimeSeriesLine[] = group.keys
              .map((key) => ({
                key,
                label: SERIES_LABELS[key],
                color: SERIES_COLORS[key],
                points: data.series[key]?.points ?? [],
              }))
              .filter((l) => l.points.length > 0);

            const peak = Math.max(0, ...lines.flatMap((l) => l.points.map(([, v]) => v)));

            return (
              <section key={group.title} className="mt">
                <p className="microlabel">{group.title}</p>
                {lines.length === 0 ? (
                  <EmptyState
                    title="이 지표를 보고하지 않는 호스트입니다"
                    description="0이라는 뜻이 아니라, 해당 스크레이퍼가 이 호스트에서 값을 내지 않았다는 뜻입니다. 컨테이너 안에서 도는 에이전트는 파일시스템 사용률을 보고하지 못할 수 있습니다."
                  />
                ) : (
                  <>
                    <TimeSeriesChart lines={lines} height={190} />
                    {group.bytes ? <p className="hint">최대 {formatBytes(peak)}/s</p> : null}
                  </>
                )}
              </section>
            );
          })
        : null}

      {data?.queries?.cpu_pct ? (
        <QueryPeek query={data.queries.cpu_pct} label="서버가 생성한 MetricsQL (CPU)" />
      ) : null}
    </div>
  );
}

export default function InfrastructurePage() {
  const [rangeSec, setRangeSec] = useState(86400);
  const [target, setTarget] = useState(0.99);
  const [heat, setHeat] = useState<HeatMetric>("cpu_pct");
  const [selected, setSelected] = useState<string | undefined>(undefined);
  const [nonce, setNonce] = useState(0);

  const hostsFetch = useCallback((signal: AbortSignal) => listHosts(signal), []);
  const hosts = useAsync(hostsFetch, [nonce]);

  const uptimeFetch = useCallback(
    (signal: AbortSignal) => {
      const { from, to } = windowFor(rangeSec);
      // A coarse bucket so one missed scrape is not an outage: at 15s scrapes,
      // a 60s bucket tolerates three drops before the host is counted down.
      const stepSec = rangeSec <= 86400 ? 60 : 300;
      return getUptime({ from, to, stepSec, target, signal });
    },
    [rangeSec, target],
  );
  const uptime = useAsync(uptimeFetch, [rangeSec, target, nonce]);

  const list = hosts.data?.data ?? [];
  const selectedHost = list.find((h) => h.host_ident === selected);
  const avgCPU = meanOf(list, "cpu_pct");
  const avgMem = meanOf(list, "mem_pct");

  return (
    <div className="view">
      <div className="page-head">
        <h1>인프라</h1>
        <span className="page-sub">호스트 메트릭으로 본 활성 호스트와 사용률 · OBS-01</span>
        <div className="page-actions">
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => setNonce((n) => n + 1)}
            disabled={hosts.loading}
          >
            {hosts.refreshing ? "새로고침 중…" : "새로고침"}
          </button>
        </div>
      </div>

      <div className="toolbar">
        <RangeSelect value={rangeSec} onChange={setRangeSec} id="infra-range" />
        <div className="field">
          <label htmlFor="slo-target">SLO 목표</label>
          <select
            id="slo-target"
            className="input"
            value={target}
            onChange={(e) => setTarget(Number(e.target.value))}
          >
            {TARGETS.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="heat-metric">호스트맵 색상</label>
          <select
            id="heat-metric"
            className="input"
            value={heat}
            onChange={(e) => setHeat(e.target.value as HeatMetric)}
          >
            {(Object.keys(HEAT_LABELS) as HeatMetric[]).map((m) => (
              <option key={m} value={m}>
                {HEAT_LABELS[m]}
              </option>
            ))}
          </select>
        </div>
      </div>

      {hosts.data && !hosts.data.metrics_available ? (
        <Banner
          tone="warn"
          title="호스트 목록은 읽었지만 메트릭 백엔드에서 값을 가져오지 못했습니다"
          description="레지스트리(마지막 보고 시각)는 정상이고 사용률만 비어 있습니다. VictoriaMetrics(vmselect)가 떠 있는지 확인하세요."
        />
      ) : null}

      <div className="kpi-grid infra-kpis">
        <div className="kpi is-accent">
          <div className="kpi-top">
            <span className="microlabel">활성 호스트</span>
          </div>
          <div className="kpi-value">
            {hosts.data ? hosts.data.active : "–"}
            <span className="kpi-unit">/ {hosts.data ? hosts.data.total : "–"}대 등록</span>
          </div>
          <div className="kpi-trend">
            최근 {hosts.data ? Math.round(hosts.data.active_window_sec / 60) : "–"}분 내 보고 기준
          </div>
        </div>
        <div className="kpi">
          <div className="kpi-top">
            <span className="microlabel">평균 CPU</span>
          </div>
          <div className="kpi-value">
            {avgCPU != null ? avgCPU.toFixed(1) : "–"}
            <span className="kpi-unit">%</span>
          </div>
          <div className="kpi-trend">사용률을 보고한 호스트 기준</div>
        </div>
        <div className="kpi">
          <div className="kpi-top">
            <span className="microlabel">평균 메모리</span>
          </div>
          <div className="kpi-value">
            {avgMem != null ? avgMem.toFixed(1) : "–"}
            <span className="kpi-unit">%</span>
          </div>
          <div className="kpi-trend">사용률을 보고한 호스트 기준</div>
        </div>
      </div>

      <div className="grid-32 mb">
        <div className="card">
          <div className="card-head">
            <h2>호스트맵</h2>
            <span className="spacer" />
            <span className="badge badge-mute">클릭하면 상세</span>
          </div>
          {hosts.error ? <ErrorState error={hosts.error} onRetry={hosts.reload} /> : null}
          {!hosts.error && hosts.loading ? <ChartSkeleton /> : null}
          {!hosts.error && hosts.data && list.length === 0 ? (
            <EmptyState
              title="등록된 호스트가 없습니다"
              description="호스트 에이전트(otel-hostagent)가 수집 키로 메트릭을 보내기 시작하면 여기에 셀이 나타납니다. seed-dev.sh가 키를 발급하고 에이전트를 다시 띄웁니다."
            />
          ) : null}
          {!hosts.error && list.length > 0 ? (
            <>
              <HostMap
                hosts={list}
                metric={heat}
                selected={selected}
                onSelect={(ident) => setSelected((cur) => (cur === ident ? undefined : ident))}
              />
              <HostMapLegend metric={heat} />
            </>
          ) : null}
        </div>

        <div className="card">
          <div className="card-head">
            <h2>업타임 SLO</h2>
          </div>
          {uptime.error ? <ErrorState error={uptime.error} onRetry={uptime.reload} /> : null}
          {!uptime.error && uptime.loading ? <TableSkeleton rows={4} /> : null}
          {!uptime.error && uptime.data ? <UptimeSLOWidget data={uptime.data} /> : null}
        </div>
      </div>

      {selectedHost ? (
        <HostDetail host={selectedHost} rangeSec={rangeSec} onClose={() => setSelected(undefined)} />
      ) : (
        <div className="card">
          <div className="card-head">
            <h2>호스트 목록</h2>
            <span className="spacer" />
            {hosts.data ? (
              <span className="badge badge-mute">
                {hosts.data.total}대 · 활성 {hosts.data.active}대
              </span>
            ) : null}
          </div>
          {hosts.error ? <ErrorState error={hosts.error} onRetry={hosts.reload} /> : null}
          {!hosts.error && hosts.loading ? <TableSkeleton rows={6} /> : null}
          {!hosts.error && hosts.data && list.length === 0 ? (
            <EmptyState
              title="아직 보고한 호스트가 없습니다"
              description="observability_hosts 레지스트리가 비어 있습니다. 수집 게이트웨이가 호스트를 한 번이라도 보고하면 여기에 행이 생깁니다."
            />
          ) : null}
          {!hosts.error && list.length > 0 ? (
            <HostTable hosts={list} selected={selected} onSelect={setSelected} />
          ) : null}
        </div>
      )}
    </div>
  );
}
