/**
 * Formatting helpers shared by every view.
 *
 * Locale is pinned to ko-KR rather than left to the browser: the design is
 * Korean-first (05 section 8) and a chart axis that switches format with the
 * viewer locale makes two screenshots incomparable.
 */

const LOCALE = "ko-KR";

/** Clock time for a chart axis or a log row. */
export function formatTime(ms: number): string {
  return new Date(ms).toLocaleTimeString(LOCALE, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

/** Date + time, for anything that can be older than today. */
export function formatDateTime(value: number | string): string {
  const d = typeof value === "number" ? new Date(value) : new Date(value);
  return d.toLocaleString(LOCALE, {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

/** "3분 전" style relative age, for alert events and dashboard rows. */
export function formatAgo(value: number | string): string {
  const then = typeof value === "number" ? value : new Date(value).getTime();
  const sec = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (sec < 60) return sec + "초 전";
  if (sec < 3600) return Math.floor(sec / 60) + "분 전";
  if (sec < 86400) return Math.floor(sec / 3600) + "시간 전";
  return Math.floor(sec / 86400) + "일 전";
}

/** A duration in milliseconds, rendered at a readable magnitude. */
export function formatDuration(ms: number): string {
  if (ms < 1) return ms.toFixed(2) + "ms";
  if (ms < 1000) return Math.round(ms) + "ms";
  return (ms / 1000).toFixed(2) + "s";
}

/** A seconds count as the "for" window an alert rule sustains over. */
export function formatSeconds(sec: number): string {
  if (sec < 60) return sec + "초";
  if (sec < 3600) return Math.round(sec / 60) + "분";
  return (sec / 3600).toFixed(sec % 3600 === 0 ? 0 : 1) + "시간";
}

/**
 * A metric value at a sane precision.
 *
 * Metrics span many orders of magnitude (a ratio of 0.0032, a byte count of
 * 6.1e10), so a fixed decimal count is wrong for one end or the other.
 */
export function formatMetric(v: number): string {
  if (!Number.isFinite(v)) return "-";
  const abs = Math.abs(v);
  if (abs === 0) return "0";
  if (abs >= 1e9) return (v / 1e9).toFixed(2) + "G";
  if (abs >= 1e6) return (v / 1e6).toFixed(2) + "M";
  if (abs >= 1e3) return (v / 1e3).toFixed(2) + "k";
  if (abs >= 1) return v.toFixed(2);
  if (abs >= 0.001) return v.toFixed(4);
  return v.toExponential(2);
}

export function formatBytes(bytes: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return v.toFixed(i === 0 ? 0 : 1) + units[i];
}

/**
 * A stable, readable name for one series.
 *
 * `__name__` plus the org label carry no information a user did not already
 * supply in the query, so the legend shows what actually distinguishes the
 * series - and falls back to the metric name when nothing does.
 */
export function seriesLabel(labels: Record<string, string>): string {
  const parts = Object.entries(labels)
    .filter(([k]) => k !== "__name__" && k !== "klaro_org_id")
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => k + "=" + v);
  if (parts.length === 0) return labels.__name__ ?? "series";
  return parts.join(" · ");
}
