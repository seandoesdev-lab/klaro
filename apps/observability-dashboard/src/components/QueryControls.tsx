"use client";

/**
 * Shared query controls: time range, label filters, and numeric inputs.
 *
 * The filter editor produces explorer.Matcher values rather than free text
 * because that is all obsplane accepts - a caller never supplies raw MetricsQL,
 * TraceQL or LogQL (internal/explorer package doc). Making the UI structured is
 * therefore not a simplification, it is the contract.
 */

import type { Matcher } from "@/lib/api/types";

/** Presets, in seconds. Chosen so each one crosses a resolution boundary. */
export const RANGE_PRESETS = [
  { label: "15분", sec: 900 },
  { label: "1시간", sec: 3600 },
  { label: "6시간", sec: 21600 },
  { label: "24시간", sec: 86400 },
  { label: "7일", sec: 604800 },
  { label: "30일", sec: 2592000 },
] as const;

/** Step presets, in seconds; obsplane defaults to 30s when none is given. */
export const STEP_PRESETS = [
  { label: "자동", sec: 0 },
  { label: "15초", sec: 15 },
  { label: "30초", sec: 30 },
  { label: "1분", sec: 60 },
  { label: "5분", sec: 300 },
  { label: "1시간", sec: 3600 },
] as const;

export function RangeSelect({
  value,
  onChange,
  id = "range",
}: {
  value: number;
  onChange: (sec: number) => void;
  id?: string;
}) {
  return (
    <div className="field">
      <label htmlFor={id}>시간 구간</label>
      <select
        id={id}
        className="input"
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
      >
        {RANGE_PRESETS.map((p) => (
          <option key={p.sec} value={p.sec}>
            최근 {p.label}
          </option>
        ))}
      </select>
    </div>
  );
}

export function StepSelect({ value, onChange }: { value: number; onChange: (sec: number) => void }) {
  return (
    <div className="field">
      <label htmlFor="step">스텝</label>
      <select id="step" className="input" value={value} onChange={(e) => onChange(Number(e.target.value))}>
        {STEP_PRESETS.map((p) => (
          <option key={p.sec} value={p.sec}>
            {p.label}
          </option>
        ))}
      </select>
    </div>
  );
}

/**
 * Label matcher editor.
 *
 * The `=` / `!=` toggle maps onto Matcher.negate. An empty row is kept in the
 * list so there is always somewhere to type, and dropped on submit by
 * appendFilters, which skips matchers missing a label or a value.
 */
export function FilterEditor({
  filters,
  onChange,
  labelSuggestions = [],
}: {
  filters: Matcher[];
  onChange: (next: Matcher[]) => void;
  labelSuggestions?: string[];
}) {
  const rows = filters.length > 0 ? filters : [{ label: "", value: "" }];

  const update = (i: number, patch: Partial<Matcher>) => {
    const next = rows.map((f, idx) => (idx === i ? { ...f, ...patch } : f));
    onChange(next);
  };

  return (
    <div>
      <label className="microlabel" style={{ display: "block", marginBottom: "var(--sp-1)" }}>
        라벨 필터
      </label>
      {rows.map((f, i) => (
        <div className="filter-row" key={i}>
          <input
            className="input mono"
            placeholder="라벨 (예: service)"
            list={labelSuggestions.length > 0 ? "label-suggestions" : undefined}
            value={f.label}
            onChange={(e) => update(i, { label: e.target.value })}
            aria-label={"필터 " + (i + 1) + " 라벨"}
          />
          <button
            type="button"
            className="filter-op"
            aria-pressed={Boolean(f.negate)}
            title={f.negate ? "같지 않음" : "같음"}
            onClick={() => update(i, { negate: !f.negate })}
          >
            {f.negate ? "!=" : "="}
          </button>
          <input
            className="input mono"
            placeholder="값"
            value={f.value}
            onChange={(e) => update(i, { value: e.target.value })}
            aria-label={"필터 " + (i + 1) + " 값"}
          />
          <button
            type="button"
            className="icon-btn"
            aria-label={"필터 " + (i + 1) + " 삭제"}
            onClick={() => onChange(rows.filter((_, idx) => idx !== i))}
          >
            ×
          </button>
        </div>
      ))}
      {labelSuggestions.length > 0 ? (
        <datalist id="label-suggestions">
          {labelSuggestions.map((l) => (
            <option key={l} value={l} />
          ))}
        </datalist>
      ) : null}
      <button
        type="button"
        className="link"
        onClick={() => onChange([...rows, { label: "", value: "" }])}
      >
        + 필터 추가
      </button>
    </div>
  );
}

/** Read-only view of the query obsplane actually ran. */
export function QueryPeek({ query, label = "실행된 질의" }: { query: string; label?: string }) {
  if (!query) return null;
  return (
    <div>
      <span className="microlabel">{label}</span>
      <pre className="query-peek">{query}</pre>
    </div>
  );
}

/** from/to pair for a "last N seconds" window, computed at call time. */
export function windowFor(rangeSec: number): { from: Date; to: Date } {
  const to = new Date();
  return { from: new Date(to.getTime() - rangeSec * 1000), to };
}
