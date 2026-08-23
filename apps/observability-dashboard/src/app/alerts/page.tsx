"use client";

/**
 * Alert rules [OBS-06] and the history of what they fired [OBS-07].
 *
 * One screen with two tabs rather than two routes: a rule and its events are
 * the same question asked forwards and backwards, and the event list is only
 * readable next to the rule names it references.
 */

import { useCallback, useMemo, useState } from "react";
import { AlertRuleForm, initialValue, toInput, type AlertRuleFormValue } from "@/components/AlertRuleForm";
import { EmptyState, ErrorState, TableSkeleton } from "@/components/States";
import { useAsync } from "@/hooks/useAsync";
import { ApiError } from "@/lib/api/client";
import {
  createAlertRule,
  deleteAlertRule,
  listAlertEvents,
  listAlertRules,
  updateAlertRule,
} from "@/lib/api/endpoints";
import type { AlertRule, Severity } from "@/lib/api/types";
import { formatAgo, formatDateTime, formatMetric, formatSeconds } from "@/lib/format";

const SEVERITY_BADGE: Record<Severity, string> = {
  info: "badge-mute",
  warning: "badge-warn",
  critical: "badge-crit",
};

const COMPARATOR_SIGN = { gt: ">", gte: ">=", lt: "<", lte: "<=" } as const;

type Tab = "rules" | "events";

export default function AlertsPage() {
  const [tab, setTab] = useState<Tab>("rules");
  const [editing, setEditing] = useState<{ rule?: AlertRule; value: AlertRuleFormValue } | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<ApiError | null>(null);
  const [stateFilter, setStateFilter] = useState("");

  const rules = useAsync(useCallback((s: AbortSignal) => listAlertRules(s), []), []);
  const events = useAsync(
    useCallback((s: AbortSignal) => listAlertEvents({ state: stateFilter || undefined, limit: 200 }, s), [stateFilter]),
    [stateFilter],
  );

  const ruleNames = useMemo(() => {
    const m = new Map<string, string>();
    for (const r of rules.data ?? []) m.set(r.id, r.name);
    return m;
  }, [rules.data]);

  const firing = (events.data ?? []).filter((e) => e.state === "firing").length;

  const save = async () => {
    if (!editing) return;
    setSaving(true);
    setSaveError(null);
    try {
      const body = toInput(editing.value);
      if (editing.rule) await updateAlertRule(editing.rule.id, body);
      else await createAlertRule(body);
      setEditing(null);
      rules.reload();
    } catch (err) {
      setSaveError(err instanceof ApiError ? err : new ApiError("INTERNAL_ERROR", String(err), 0));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (rule: AlertRule) => {
    // A rule delete also drops its event history server side, so it is worth a
    // confirm even though the list is short.
    if (!window.confirm(rule.name + " 룰과 그 이벤트 기록을 삭제할까요?")) return;
    try {
      await deleteAlertRule(rule.id);
      rules.reload();
      events.reload();
    } catch (err) {
      setSaveError(err instanceof ApiError ? err : new ApiError("INTERNAL_ERROR", String(err), 0));
    }
  };

  const toggle = async (rule: AlertRule) => {
    try {
      await updateAlertRule(rule.id, { enabled: !rule.enabled });
      rules.reload();
    } catch (err) {
      setSaveError(err instanceof ApiError ? err : new ApiError("INTERNAL_ERROR", String(err), 0));
    }
  };

  return (
    <div className="view">
      <div className="page-head">
        <h1>알림</h1>
        <span className="page-sub">메트릭 임계 룰과 발생 이력 · OBS-06 / OBS-07</span>
        <div className="page-actions">
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => {
              setSaveError(null);
              setEditing({ value: initialValue() });
            }}
          >
            + 새 알림 룰
          </button>
        </div>
      </div>

      <div className="subtabs" role="tablist">
        <button
          type="button"
          className="subtab"
          role="tab"
          aria-current={tab === "rules"}
          onClick={() => setTab("rules")}
        >
          룰 <span className="cnt">{rules.data?.length ?? 0}</span>
        </button>
        <button
          type="button"
          className="subtab"
          role="tab"
          aria-current={tab === "events"}
          onClick={() => setTab("events")}
        >
          이벤트 <span className="cnt">{events.data?.length ?? 0}</span>
          {firing > 0 ? <span className="badge badge-crit">발생 중 {firing}</span> : null}
        </button>
      </div>

      {saveError ? <ErrorState error={saveError} /> : null}

      {tab === "rules" ? (
        <div className="card">
          <div className="card-head">
            <h2>알림 룰</h2>
            <span className="spacer" />
            <button type="button" className="btn btn-sm" onClick={rules.reload} disabled={rules.loading}>
              새로고침
            </button>
          </div>

          {rules.error ? <ErrorState error={rules.error} onRetry={rules.reload} /> : null}
          {!rules.error && rules.loading ? <TableSkeleton rows={4} /> : null}

          {!rules.error && !rules.loading && (rules.data?.length ?? 0) === 0 ? (
            <EmptyState
              title="아직 알림 룰이 없습니다"
              description="메트릭이 임계값을 지속적으로 넘을 때 이메일이나 Slack으로 알립니다. 첫 룰을 만들어보세요."
              action={
                <button type="button" className="btn btn-primary" onClick={() => setEditing({ value: initialValue() })}>
                  첫 알림 룰 만들기
                </button>
              }
            />
          ) : null}

          {(rules.data?.length ?? 0) > 0 ? (
            <div className="tbl-wrap">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>이름</th>
                    <th>조건</th>
                    <th>지속</th>
                    <th>심각도</th>
                    <th>채널</th>
                    <th>상태</th>
                    <th className="ta-r">작업</th>
                  </tr>
                </thead>
                <tbody>
                  {(rules.data ?? []).map((r) => (
                    <tr key={r.id}>
                      <td>
                        <span className="cell-main">{r.name}</span>
                        <span className="cell-sub">{r.query_spec.metric}</span>
                      </td>
                      <td className="cell-sub">
                        {(r.query_spec.agg || "raw") + " " + COMPARATOR_SIGN[r.comparator] + " " + r.threshold}
                      </td>
                      <td className="num">{formatSeconds(r.for_duration_sec)}</td>
                      <td>
                        <span className={"badge " + SEVERITY_BADGE[r.severity]}>{r.severity}</span>
                      </td>
                      <td className="cell-sub">
                        {r.channels.length === 0 ? "—" : r.channels.map((c) => c.target).join(", ")}
                      </td>
                      <td>
                        <span className={"badge " + (r.enabled ? "badge-good" : "badge-mute")}>
                          <span className={"dot " + (r.enabled ? "dot-good" : "dot-mute")} aria-hidden="true" />
                          {r.enabled ? "활성" : "비활성"}
                        </span>
                      </td>
                      <td className="ta-r">
                        <button type="button" className="btn btn-sm" onClick={() => toggle(r)}>
                          {r.enabled ? "끄기" : "켜기"}
                        </button>{" "}
                        <button
                          type="button"
                          className="btn btn-sm"
                          onClick={() => {
                            setSaveError(null);
                            setEditing({ rule: r, value: initialValue(r) });
                          }}
                        >
                          편집
                        </button>{" "}
                        <button type="button" className="btn btn-sm btn-danger" onClick={() => remove(r)}>
                          삭제
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}
        </div>
      ) : null}

      {tab === "events" ? (
        <div className="card">
          <div className="card-head">
            <h2>알림 이벤트</h2>
            <span className="spacer" />
            <div className="field">
              <label htmlFor="ev-state" className="sr-only">
                상태 필터
              </label>
              <select
                id="ev-state"
                className="input"
                value={stateFilter}
                onChange={(e) => setStateFilter(e.target.value)}
              >
                <option value="">상태: 전체</option>
                <option value="firing">발생 중</option>
                <option value="resolved">해소됨</option>
              </select>
            </div>
            <button type="button" className="btn btn-sm" onClick={events.reload} disabled={events.loading}>
              새로고침
            </button>
          </div>

          {events.error ? <ErrorState error={events.error} onRetry={events.reload} /> : null}
          {!events.error && events.loading ? <TableSkeleton rows={5} /> : null}

          {!events.error && !events.loading && (events.data?.length ?? 0) === 0 ? (
            <EmptyState
              title="발생한 알림이 없습니다"
              description="룰이 임계값을 넘긴 적이 없거나, 선택한 상태 필터에 해당하는 기록이 없습니다."
            />
          ) : null}

          {(events.data?.length ?? 0) > 0 ? (
            <div className="tbl-wrap">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>룰</th>
                    <th>상태</th>
                    <th className="ta-r">값</th>
                    <th>시작</th>
                    <th>해소</th>
                    <th>라벨</th>
                  </tr>
                </thead>
                <tbody>
                  {(events.data ?? []).map((e) => {
                    const active = e.state === "firing";
                    return (
                      <tr key={e.id}>
                        <td className="cell-main">{ruleNames.get(e.rule_id) ?? e.rule_id.slice(0, 8)}</td>
                        <td>
                          <span className={"badge " + (active ? "badge-crit" : "badge-good")}>
                            <span
                              className={"dot " + (active ? "dot-crit blink" : "dot-good")}
                              aria-hidden="true"
                            />
                            {active ? "발생 중" : "해소됨"}
                          </span>
                        </td>
                        <td className="ta-r num">{e.value === null ? "—" : formatMetric(e.value)}</td>
                        <td className="cell-sub">
                          {formatDateTime(e.started_at)}
                          <br />
                          {formatAgo(e.started_at)}
                        </td>
                        <td className="cell-sub">{e.resolved_at ? formatDateTime(e.resolved_at) : "—"}</td>
                        <td className="cell-sub">
                          {Object.entries(e.labels ?? {})
                            .map(([k, v]) => k + "=" + v)
                            .join(" · ") || "—"}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          ) : null}
        </div>
      ) : null}

      {editing ? (
        <div className="overlay" role="dialog" aria-modal="true" aria-label="알림 룰 편집">
          <div className="modal-card">
            <h2>{editing.rule ? "알림 룰 편집" : "새 알림 룰"}</h2>
            <p className="card-sub">
              vmalert가 평가하므로 신호는 metric만 지원합니다. 저장 즉시 룰 파일이 동기화됩니다.
            </p>
            <AlertRuleForm value={editing.value} onChange={(v) => setEditing({ ...editing, value: v })} />
            <div className="modal-actions">
              <button type="button" className="btn" onClick={() => setEditing(null)} disabled={saving}>
                취소
              </button>
              <button
                type="button"
                className="btn btn-primary"
                onClick={save}
                disabled={saving || editing.value.name.trim() === "" || editing.value.metric.trim() === ""}
              >
                {saving ? "저장 중…" : "저장"}
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
