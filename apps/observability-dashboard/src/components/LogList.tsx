/**
 * Log line list for a LogsPage.
 *
 * Rendered as a grid rather than a table: the message column has to wrap while
 * the timestamp and level stay aligned, and a table cell that wraps drags the
 * whole row height around unpredictably.
 */

import type { LogEntry } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";

/** Labels every line carries; showing them per row would be pure noise. */
const UNINTERESTING = new Set(["__name__", "klaro_org_id"]);

export function LogList({ entries }: { entries: LogEntry[] }) {
  return (
    <div className="log-view" role="log" aria-label="로그 라인">
      {entries.map((entry, i) => {
        const level = (entry.level || "INFO").toUpperCase();
        const labels = Object.entries(entry.labels ?? {}).filter(([k]) => !UNINTERESTING.has(k));
        return (
          <div className="log-row" key={entry.ts + ":" + i}>
            <span className="log-ts">{formatDateTime(entry.ts)}</span>
            <span className={"log-lvl lvl-" + level}>{level}</span>
            <span>
              <span className="log-msg">{entry.message}</span>
              {labels.length > 0 ? (
                <span className="log-labels">
                  {labels.map(([k, v]) => (
                    <span className="log-label" key={k}>
                      {k}={v}
                    </span>
                  ))}
                </span>
              ) : null}
            </span>
          </div>
        );
      })}
    </div>
  );
}
