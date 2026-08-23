"use client";

/**
 * Create/edit form for an alert rule [OBS-06].
 *
 * The condition is built from the same structured pieces the Explorer uses,
 * because obsplane renders the MetricsQL itself and refuses a caller-written
 * expression (internal/alerting/rule.go). The form therefore mirrors
 * alerting.Input field for field, and the preview states what the rule will
 * mean in words rather than pretending to render MetricsQL client side.
 */

import { useState } from "react";
import { FilterEditor } from "@/components/QueryControls";
import {
  COMPARATORS,
  SEVERITIES,
  type AlertRule,
  type AlertRuleInput,
  type Channel,
  type Comparator,
  type Matcher,
  type Severity,
} from "@/lib/api/types";
import { formatSeconds } from "@/lib/format";

const COMPARATOR_LABELS: Record<Comparator, string> = {
  gt: "> 초과",
  gte: ">= 이상",
  lt: "< 미만",
  lte: "<= 이하",
};

const SEVERITY_LABELS: Record<Severity, string> = {
  info: "정보",
  warning: "경고",
  critical: "치명",
};

/** vmalert evaluates metrics only, so the agg list matches the Explorer's. */
const AGGS = ["", "rate", "avg", "sum", "count", "p50", "p95", "p99"];

export interface AlertRuleFormValue {
  name: string;
  metric: string;
  filters: Matcher[];
  agg: string;
  stepSec: number;
  comparator: Comparator;
  threshold: number;
  forDurationSec: number;
  severity: Severity;
  channels: Channel[];
  enabled: boolean;
}

export function initialValue(rule?: AlertRule): AlertRuleFormValue {
  if (!rule) {
    return {
      name: "",
      metric: "",
      filters: [],
      agg: "avg",
      stepSec: 60,
      comparator: "gt",
      threshold: 0,
      forDurationSec: 300,
      severity: "warning",
      channels: [],
      enabled: true,
    };
  }
  return {
    name: rule.name,
    metric: rule.query_spec.metric,
    filters: rule.query_spec.filters ?? [],
    agg: rule.query_spec.agg ?? "",
    stepSec: rule.query_spec.step_sec ?? 60,
    comparator: rule.comparator,
    threshold: rule.threshold,
    forDurationSec: rule.for_duration_sec,
    severity: rule.severity,
    channels: rule.channels ?? [],
    enabled: rule.enabled,
  };
}

/** Shape the form value into the POST/PATCH body obsplane expects. */
export function toInput(v: AlertRuleFormValue): AlertRuleInput {
  return {
    name: v.name.trim(),
    signal: "metric",
    query_spec: {
      metric: v.metric.trim(),
      filters: v.filters.filter((f) => f.label && f.value),
      agg: v.agg || undefined,
      step_sec: v.stepSec > 0 ? v.stepSec : undefined,
    },
    comparator: v.comparator,
    threshold: v.threshold,
    for_duration_sec: v.forDurationSec,
    severity: v.severity,
    channels: v.channels.filter((c) => c.target.trim() !== ""),
    enabled: v.enabled,
  };
}

export function AlertRuleForm({
  value,
  onChange,
}: {
  value: AlertRuleFormValue;
  onChange: (next: AlertRuleFormValue) => void;
}) {
  const set = <K extends keyof AlertRuleFormValue>(key: K, v: AlertRuleFormValue[K]) =>
    onChange({ ...value, [key]: v });

  const [draft, setDraft] = useState<Channel>({ type: "email", target: "" });

  return (
    <div className="stack">
      <div className="field">
        <label htmlFor="rule-name">룰 이름</label>
        <input
          id="rule-name"
          className="input"
          value={value.name}
          onChange={(e) => set("name", e.target.value)}
          placeholder="checkout P95 지연 3초 초과"
        />
      </div>

      <div className="kv-form">
        <div className="field">
          <label htmlFor="rule-metric">메트릭</label>
          <input
            id="rule-metric"
            className="input mono"
            value={value.metric}
            onChange={(e) => set("metric", e.target.value)}
            placeholder="http_server_duration_seconds"
          />
        </div>
        <div className="field">
          <label htmlFor="rule-agg">집계</label>
          <select id="rule-agg" className="input" value={value.agg} onChange={(e) => set("agg", e.target.value)}>
            {AGGS.map((a) => (
              <option key={a || "none"} value={a}>
                {a || "없음"}
              </option>
            ))}
          </select>
        </div>
      </div>

      <FilterEditor
        filters={value.filters}
        onChange={(f) => set("filters", f)}
        labelSuggestions={["service", "klaro_env", "host"]}
      />

      <div className="kv-form">
        <div className="field">
          <label htmlFor="rule-cmp">조건</label>
          <select
            id="rule-cmp"
            className="input"
            value={value.comparator}
            onChange={(e) => set("comparator", e.target.value as Comparator)}
          >
            {COMPARATORS.map((c) => (
              <option key={c} value={c}>
                {COMPARATOR_LABELS[c]}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="rule-threshold">임계값</label>
          <input
            id="rule-threshold"
            className="input mono"
            type="number"
            step="any"
            value={value.threshold}
            onChange={(e) => set("threshold", Number(e.target.value))}
          />
        </div>
        <div className="field">
          <label htmlFor="rule-step">롤업 창(초)</label>
          <input
            id="rule-step"
            className="input mono"
            type="number"
            min={1}
            value={value.stepSec}
            onChange={(e) => set("stepSec", Math.max(1, Number(e.target.value)))}
          />
        </div>
        <div className="field">
          <label htmlFor="rule-for">지속 시간(초)</label>
          <input
            id="rule-for"
            className="input mono"
            type="number"
            min={0}
            value={value.forDurationSec}
            onChange={(e) => set("forDurationSec", Math.max(0, Number(e.target.value)))}
          />
        </div>
      </div>

      <div className="form-row">
        <div className="field">
          <label htmlFor="rule-sev">심각도</label>
          <select
            id="rule-sev"
            className="input"
            value={value.severity}
            onChange={(e) => set("severity", e.target.value as Severity)}
          >
            {SEVERITIES.map((s) => (
              <option key={s} value={s}>
                {SEVERITY_LABELS[s]}
              </option>
            ))}
          </select>
        </div>
        <label style={{ display: "inline-flex", alignItems: "center", gap: 8, fontSize: 13 }}>
          <span className="switch">
            <input
              type="checkbox"
              checked={value.enabled}
              onChange={(e) => set("enabled", e.target.checked)}
              aria-label="룰 활성화"
            />
            <span className="track" />
            <span className="knob" />
          </span>
          활성화
        </label>
      </div>

      <div>
        <label className="microlabel" style={{ display: "block", marginBottom: "var(--sp-1)" }}>
          알림 채널
        </label>
        {value.channels.map((c, i) => (
          <div className="filter-row" key={c.type + c.target + i}>
            <span className="input mono" style={{ background: "var(--surface-2)" }}>
              {c.type}
            </span>
            <span />
            <span className="input mono" style={{ background: "var(--surface-2)" }}>
              {c.target}
            </span>
            <button
              type="button"
              className="icon-btn"
              aria-label="채널 삭제"
              onClick={() =>
                set(
                  "channels",
                  value.channels.filter((_, idx) => idx !== i),
                )
              }
            >
              ×
            </button>
          </div>
        ))}
        <div className="filter-row">
          <select
            className="input"
            value={draft.type}
            onChange={(e) => setDraft({ ...draft, type: e.target.value as Channel["type"] })}
            aria-label="채널 종류"
          >
            <option value="email">email</option>
            <option value="slack">slack</option>
          </select>
          <span />
          <input
            className="input mono"
            value={draft.target}
            placeholder={draft.type === "email" ? "sre@example.com" : "#alerts-prod"}
            onChange={(e) => setDraft({ ...draft, target: e.target.value })}
            aria-label="채널 대상"
          />
          <button
            type="button"
            className="icon-btn"
            aria-label="채널 추가"
            style={{ color: "var(--accent)" }}
            disabled={draft.target.trim() === ""}
            onClick={() => {
              set("channels", [...value.channels, { ...draft, target: draft.target.trim() }]);
              setDraft({ type: draft.type, target: "" });
            }}
          >
            +
          </button>
        </div>
      </div>

      <p className="hint">
        {value.metric || "(메트릭)"} 의 {value.agg || "원본"} 값이 {COMPARATOR_LABELS[value.comparator]}{" "}
        {value.threshold} 인 상태가 {formatSeconds(value.forDurationSec)} 이상 지속되면 발생합니다.
        최종 MetricsQL은 서버가 조직 라벨을 주입해 생성합니다.
      </p>
    </div>
  );
}
