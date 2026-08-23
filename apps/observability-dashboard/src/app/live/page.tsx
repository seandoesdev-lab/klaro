"use client";

/**
 * Live dashboard [OBS-01/APM-02]: WebSocket KPIs refreshed within 2 seconds.
 *
 * A live view has one failure mode worth designing for above all others: it
 * keeps showing the last number after the stream dies, and the number reads as
 * current. So the tiles dim and a banner says what happened whenever the socket
 * is not open (05 section 7, 스트림 끊김 [EDGE-02]).
 */

import { useCallback, useMemo } from "react";
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { Banner, EmptyState, LineSkeleton } from "@/components/States";
import { useLive } from "@/hooks/useLive";
import { useAsync } from "@/hooks/useAsync";
import { getQuota } from "@/lib/api/endpoints";
import type { LiveFrame } from "@/lib/api/types";
import { formatBytes, formatMetric, formatTime } from "@/lib/format";

/** Average a frame's points per service, so one tile exists per service. */
function byService(frame: LiveFrame | undefined): Array<{ service: string; value: number }> {
  if (!frame) return [];
  const sums = new Map<string, { total: number; n: number }>();
  for (const p of frame.points) {
    const key = p.labels.service ?? p.labels["service.name"] ?? p.labels.__name__ ?? "unknown";
    const cur = sums.get(key) ?? { total: 0, n: 0 };
    cur.total += p.value;
    cur.n += 1;
    sums.set(key, cur);
  }
  return [...sums.entries()]
    .map(([service, agg]) => ({ service, value: agg.total / agg.n }))
    .sort((a, b) => b.value - a.value);
}

const SERIES_COLORS = ["var(--s-p95)", "var(--s-p50)", "var(--s-p99)", "var(--warn)"];

export default function LivePage() {
  const { status, latest, history, reason } = useLive("metric", { windowSize: 90 });
  const quota = useAsync(useCallback((signal: AbortSignal) => getQuota(signal), []), []);

  const tiles = byService(latest);
  const streaming = status === "open";

  // One row per frame, one column per service: the same pivot the explorer
  // chart does, but over the rolling live window instead of a query result.
  const chart = useMemo(() => {
    const services = [
      ...new Set(history.flatMap((f) => f.points.map((p) => p.labels.service ?? "unknown"))),
    ];
    const rows = history.map((f) => {
      const row: Record<string, number> = { ts: f.ts };
      for (const svc of services) {
        const pts = f.points.filter((p) => (p.labels.service ?? "unknown") === svc);
        if (pts.length > 0) row[svc] = pts.reduce((a, p) => a + p.value, 0) / pts.length;
      }
      return row;
    });
    return { rows, services };
  }, [history]);

  return (
    <div className="view">
      <div className="page-head">
        <h1>라이브 대시보드</h1>
        <span className="page-sub">Collector 스트림을 2초 이내 주기로 반영합니다 · OBS-01 / APM-02</span>
      </div>

      {status === "error" ? (
        <Banner tone="crit" title="실시간 스트림에 연결할 수 없습니다" description={reason} />
      ) : null}
      {status === "reconnecting" ? (
        <Banner
          tone="warn"
          title="대상 응답 없음, 재연결 시도 중"
          description="표시된 값은 마지막으로 받은 프레임입니다."
        />
      ) : null}
      {quota.data?.overage ? (
        <Banner
          tone="warn"
          title="플랜 한도를 초과했습니다"
          description="수집은 계속되며 초과분은 overage로 과금됩니다."
        />
      ) : null}

      <div className="kpi-grid">
        <div className={streaming ? "kpi is-good" : "kpi"}>
          <div className="kpi-top">
            <span className="microlabel">스트림</span>
            <span
              className={
                "badge " + (streaming ? "badge-good" : status === "error" ? "badge-crit" : "badge-warn")
              }
            >
              <span className={"dot " + (streaming ? "dot-good blink" : "dot-warn")} aria-hidden="true" />
              {streaming
                ? "LIVE"
                : status === "connecting"
                  ? "연결 중"
                  : status === "reconnecting"
                    ? "재연결"
                    : "중지"}
            </span>
          </div>
          <div className="kpi-value" aria-live="polite">
            {latest ? latest.points.length : "—"}
            <span className="kpi-unit">시계열</span>
          </div>
          <div className="kpi-trend">{latest ? formatTime(latest.ts) + " 수신" : "프레임 대기 중"}</div>
        </div>

        <div className="kpi is-accent">
          <div className="kpi-top">
            <span className="microlabel">활성 호스트</span>
          </div>
          <div className="kpi-value">
            {quota.loading ? "—" : (quota.data?.active_hosts ?? "—")}
            {quota.data?.host_limit ? <span className="kpi-unit">/ {quota.data.host_limit}</span> : null}
          </div>
          <div className="kpi-trend">
            {quota.data?.plan_code ? quota.data.plan_code + " 플랜" : "플랜 정보 없음"}
          </div>
        </div>

        <div className={quota.data?.ingest_exceeded ? "kpi is-warn" : "kpi"}>
          <div className="kpi-top">
            <span className="microlabel">이번 주기 수집량</span>
          </div>
          <div className="kpi-value">
            {quota.data ? formatBytes(quota.data.ingest_bytes) : "—"}
            {quota.data?.ingest_limit_gb ? (
              <span className="kpi-unit">/ {quota.data.ingest_limit_gb}GB</span>
            ) : null}
          </div>
          <div className="kpi-trend">{quota.data?.overage ? "초과 과금 적용 중" : "한도 내"}</div>
        </div>

        <div className="kpi">
          <div className="kpi-top">
            <span className="microlabel">누적 프레임</span>
          </div>
          <div className="kpi-value">{history.length}</div>
          <div className="kpi-trend">최근 {history.length * 2}초 창</div>
        </div>
      </div>

      <div className="card mb" style={{ opacity: streaming ? 1 : 0.55, transition: "opacity .3s" }}>
        <div className="card-head">
          <h2>서비스별 실시간 값</h2>
          <span className="spacer" />
          <span className="badge badge-mute">{tiles.length}개 서비스</span>
        </div>

        {tiles.length === 0 ? (
          status === "connecting" ? (
            <LineSkeleton lines={3} />
          ) : (
            <EmptyState
              title="아직 수신된 프레임이 없습니다"
              description="서비스에 klaro-apm SDK를 설치하고 KLARO_OBS_KEY를 설정하면 여기에 값이 나타납니다."
            />
          )
        ) : (
          <div className="kpi-grid" style={{ marginBottom: 0 }}>
            {tiles.map((t, i) => (
              <div className="kpi" key={t.service}>
                <div className="kpi-top">
                  <span className="microlabel">{t.service}</span>
                  <span className="swatch" style={{ background: SERIES_COLORS[i % SERIES_COLORS.length] }} />
                </div>
                <div className="kpi-value" aria-live="polite">
                  {formatMetric(t.value)}
                </div>
                <div className="kpi-trend">{streaming ? "실시간" : "마지막 수신값"}</div>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-head">
          <h2>슬라이딩 윈도우</h2>
          <span className="spacer" />
          <span className="hint">최근 {chart.rows.length}프레임</span>
        </div>
        {chart.rows.length < 2 ? (
          <LineSkeleton lines={4} />
        ) : (
          <div className="chart-wrap">
            <div className="chart-inner">
              <ResponsiveContainer width="100%" height={260}>
                <LineChart data={chart.rows} margin={{ top: 6, right: 12, bottom: 4, left: 4 }}>
                  <CartesianGrid strokeDasharray="3 3" vertical={false} />
                  <XAxis
                    dataKey="ts"
                    type="number"
                    scale="time"
                    domain={["dataMin", "dataMax"]}
                    tickFormatter={formatTime}
                    minTickGap={48}
                    stroke="var(--border-strong)"
                  />
                  <YAxis tickFormatter={formatMetric} width={62} stroke="var(--border-strong)" />
                  <Tooltip
                    labelFormatter={(v) => formatTime(Number(v))}
                    formatter={(v) => formatMetric(Number(v))}
                    contentStyle={{
                      background: "var(--surface)",
                      border: "1px solid var(--border-strong)",
                      borderRadius: 8,
                      fontSize: 12,
                    }}
                  />
                  {chart.services.map((svc, i) => (
                    <Line
                      key={svc}
                      type="monotone"
                      dataKey={svc}
                      stroke={SERIES_COLORS[i % SERIES_COLORS.length]}
                      strokeWidth={1.8}
                      dot={false}
                      isAnimationActive={false}
                    />
                  ))}
                </LineChart>
              </ResponsiveContainer>
            </div>
            <div className="chart-legend">
              {chart.services.map((svc, i) => (
                <span key={svc}>
                  <i className="swatch" style={{ background: SERIES_COLORS[i % SERIES_COLORS.length] }} />
                  {svc}
                </span>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
