"use client";

/**
 * One dashboard [OBS-09]: render its panels, and offer the minimum edit the
 * design commits to at this milestone - add or remove a panel, and change the
 * default range and refresh interval.
 *
 * The full drag-and-drop panel editor is deferred to M2+ (design HOW-3), so the
 * add form asks for the same fields dashboards.PanelQuery validates and nothing
 * more; anything richer would be inventing a contract obsplane has not agreed.
 */

import Link from "next/link";
import { useParams } from "next/navigation";
import { useCallback, useEffect, useState } from "react";
import { DashboardPanel } from "@/components/DashboardPanel";
import { EmptyState, ErrorState, LineSkeleton } from "@/components/States";
import { RANGE_PRESETS } from "@/components/QueryControls";
import { useAsync } from "@/hooks/useAsync";
import { ApiError } from "@/lib/api/client";
import { getDashboard, updateDashboard } from "@/lib/api/endpoints";
import { PANEL_TYPES, type DashboardSpec, type Panel, type PanelType } from "@/lib/api/types";

const REFRESH_PRESETS = [0, 5, 15, 30, 60, 300];

/** dashboards.Panel.ID is caller-supplied and must be unique within the spec. */
function nextPanelId(spec: DashboardSpec): string {
  let n = spec.panels.length + 1;
  const taken = new Set(spec.panels.map((p) => p.id));
  while (taken.has("p-" + n)) n += 1;
  return "p-" + n;
}

export default function DashboardDetailPage() {
  const params = useParams<{ dashId: string }>();
  const dashId = params?.dashId ?? "";

  const fetcher = useCallback((s: AbortSignal) => getDashboard(dashId, s), [dashId]);
  const { data, error, loading, reload } = useAsync(fetcher, [dashId], { enabled: dashId !== "" });

  const [spec, setSpec] = useState<DashboardSpec | null>(null);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [opError, setOpError] = useState<ApiError | null>(null);
  const [adding, setAdding] = useState(false);

  // Local spec is seeded from the server copy and only replaced when a fresh
  // one arrives, so a reload does not silently discard an unsaved edit.
  useEffect(() => {
    if (data && !dirty) setSpec(data.spec);
  }, [data, dirty]);

  const rangeSec = spec?.range_sec && spec.range_sec > 0 ? spec.range_sec : 3600;
  const refreshSec = spec?.refresh_sec ?? 0;

  // Auto-refresh re-mounts the panels by bumping a key, which is cheaper than
  // threading a reload callback into every tile.
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (refreshSec <= 0) return;
    const id = window.setInterval(() => setTick((t) => t + 1), refreshSec * 1000);
    return () => window.clearInterval(id);
  }, [refreshSec]);

  const patch = (next: DashboardSpec) => {
    setSpec(next);
    setDirty(true);
  };

  const save = async () => {
    if (!spec) return;
    setSaving(true);
    setOpError(null);
    try {
      await updateDashboard(dashId, { spec });
      setDirty(false);
      reload();
    } catch (err) {
      setOpError(err instanceof ApiError ? err : new ApiError("INTERNAL_ERROR", String(err), 0));
    } finally {
      setSaving(false);
    }
  };

  const addPanel = (draft: { title: string; type: PanelType; metric: string; width: number }) => {
    if (!spec) return;
    const panel: Panel = {
      id: nextPanelId(spec),
      title: draft.title.trim() || draft.metric,
      type: draft.type,
      layout: { x: 0, y: spec.panels.length, w: draft.width, h: 6 },
      query: { signal: "metrics", metric: draft.metric.trim(), agg: "avg", step_sec: 60 },
    };
    patch({ ...spec, panels: [...spec.panels, panel] });
    setAdding(false);
  };

  return (
    <div className="view">
      <div className="crumbs">
        <Link href="/dashboards">대시보드</Link>
        <span className="sep">/</span>
        <span>{data?.name ?? "…"}</span>
      </div>

      <div className="page-head">
        <h1>{data?.name ?? "대시보드"}</h1>
        {data?.description ? <span className="page-sub">{data.description}</span> : null}
        <div className="page-actions">
          {dirty ? (
            <button type="button" className="btn btn-primary" onClick={save} disabled={saving}>
              {saving ? "저장 중…" : "변경 저장"}
            </button>
          ) : null}
          <button type="button" className="btn btn-sm" onClick={() => setTick((t) => t + 1)}>
            새로고침
          </button>
        </div>
      </div>

      {error ? <ErrorState error={error} onRetry={reload} /> : null}
      {opError ? <ErrorState error={opError} /> : null}
      {loading || !spec ? <LineSkeleton lines={4} /> : null}

      {spec ? (
        <>
          <div className="card mb">
            <div className="toolbar" style={{ marginBottom: 0 }}>
              <div className="field">
                <label htmlFor="d-range">기본 구간</label>
                <select
                  id="d-range"
                  className="input"
                  value={rangeSec}
                  onChange={(e) => patch({ ...spec, range_sec: Number(e.target.value) })}
                >
                  {RANGE_PRESETS.map((p) => (
                    <option key={p.sec} value={p.sec}>
                      최근 {p.label}
                    </option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label htmlFor="d-refresh">자동 갱신</label>
                <select
                  id="d-refresh"
                  className="input"
                  value={refreshSec}
                  onChange={(e) => patch({ ...spec, refresh_sec: Number(e.target.value) })}
                >
                  {REFRESH_PRESETS.map((s) => (
                    <option key={s} value={s}>
                      {s === 0 ? "수동" : s + "초"}
                    </option>
                  ))}
                </select>
              </div>
              <span style={{ flex: 1 }} />
              <button type="button" className="btn" onClick={() => setAdding(true)}>
                + 패널 추가
              </button>
            </div>
            {dirty ? (
              <p className="hint" style={{ marginTop: "var(--sp-2)" }}>
                저장하지 않은 변경이 있습니다.
              </p>
            ) : null}
          </div>

          {spec.panels.length === 0 ? (
            <EmptyState
              title="패널이 없습니다"
              description="메트릭 패널을 추가하면 저장된 조건으로 매번 같은 화면을 볼 수 있습니다."
              action={
                <button type="button" className="btn btn-primary" onClick={() => setAdding(true)}>
                  첫 패널 추가
                </button>
              }
            />
          ) : (
            <div className="dash-grid">
              {spec.panels.map((p) => (
                <div key={p.id} style={{ gridColumn: "span " + Math.min(Math.max(p.layout.w || 6, 1), 12) }}>
                  <DashboardPanel
                    key={p.id + ":" + tick}
                    panel={p}
                    rangeSec={rangeSec}
                    onRemove={() => patch({ ...spec, panels: spec.panels.filter((x) => x.id !== p.id) })}
                  />
                </div>
              ))}
            </div>
          )}
        </>
      ) : null}

      {adding && spec ? <AddPanelDialog onCancel={() => setAdding(false)} onAdd={addPanel} /> : null}
    </div>
  );
}

/** Minimal add-panel dialog: metric panels only, matching the deferred scope. */
function AddPanelDialog({
  onCancel,
  onAdd,
}: {
  onCancel: () => void;
  onAdd: (draft: { title: string; type: PanelType; metric: string; width: number }) => void;
}) {
  const [title, setTitle] = useState("");
  const [type, setType] = useState<PanelType>("timeseries");
  const [metric, setMetric] = useState("");
  const [width, setWidth] = useState(6);

  return (
    <div className="overlay" role="dialog" aria-modal="true" aria-label="패널 추가">
      <div className="modal-card">
        <h2>패널 추가</h2>
        <p className="card-sub">이 마일스톤에서는 메트릭 패널만 추가할 수 있습니다.</p>
        <div className="stack">
          <div className="field">
            <label htmlFor="p-title">제목</label>
            <input id="p-title" className="input" value={title} onChange={(e) => setTitle(e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="p-metric">메트릭</label>
            <input
              id="p-metric"
              className="input mono"
              value={metric}
              onChange={(e) => setMetric(e.target.value)}
              placeholder="http_server_duration_seconds"
            />
          </div>
          <div className="kv-form">
            <div className="field">
              <label htmlFor="p-type">패널 유형</label>
              <select
                id="p-type"
                className="input"
                value={type}
                onChange={(e) => setType(e.target.value as PanelType)}
              >
                {PANEL_TYPES.filter((t) => t === "timeseries" || t === "stat").map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="p-width">폭 (12칸 기준)</label>
              <input
                id="p-width"
                className="input mono"
                type="number"
                min={1}
                max={12}
                value={width}
                onChange={(e) => setWidth(Math.min(12, Math.max(1, Number(e.target.value))))}
              />
            </div>
          </div>
        </div>
        <div className="modal-actions">
          <button type="button" className="btn" onClick={onCancel}>
            취소
          </button>
          <button
            type="button"
            className="btn btn-primary"
            disabled={metric.trim() === ""}
            onClick={() => onAdd({ title, type, metric, width })}
          >
            추가
          </button>
        </div>
      </div>
    </div>
  );
}
