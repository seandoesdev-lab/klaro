/**
 * Empty / loading / error surfaces (05 section 7).
 *
 * They live together because the rule that binds them is a single one: a view
 * never shows nothing. An empty result gets an explanation and a next step, a
 * pending one gets a skeleton in the shape of the content it will replace, and
 * a failure gets the obsplane error code so the reason is recoverable rather
 * than "something went wrong".
 */

import type { ReactNode } from "react";
import type { ApiError } from "@/lib/api/client";
import { ErrorCode } from "@/lib/api/types";

function IconInfo() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="9" />
      <path d="M12 16v-5M12 8h.01" />
    </svg>
  );
}

function IconAlert() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
      <path d="M12 9v4M12 17h.01" />
    </svg>
  );
}

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="state-card" role="status">
      <span className="state-icon">
        <IconInfo />
      </span>
      <span className="state-title">{title}</span>
      {description ? <span className="state-desc">{description}</span> : null}
      {action}
    </div>
  );
}

/**
 * Turn an obsplane error code into advice.
 *
 * The envelope message is written for the API caller; these lines say what the
 * person looking at the screen can actually do about it.
 */
function remedyFor(error: ApiError): string | undefined {
  switch (error.code) {
    case ErrorCode.Network:
      return "NEXT_PUBLIC_OBS_API_BASE가 가리키는 obsplane이 실행 중인지, CORS가 허용되어 있는지 확인하세요. 백엔드 없이 둘러보려면 NEXT_PUBLIC_OBS_MOCK=1로 두세요.";
    case ErrorCode.Unauthenticated:
      return "NEXT_PUBLIC_OBS_TOKEN이 비어 있거나 만료되었습니다. 개발 토큰을 다시 주입하세요.";
    case ErrorCode.Forbidden:
      return "토큰이 인증한 조직과 NEXT_PUBLIC_OBS_ORG_ID가 다릅니다.";
    case ErrorCode.Validation:
      return "질의 조건을 좁히거나 값을 확인한 뒤 다시 시도하세요.";
    case ErrorCode.QuotaExceeded:
      return "플랜 한도를 넘었습니다. 수집은 계속되며 초과분은 overage로 과금됩니다.";
    default:
      return undefined;
  }
}

export function ErrorState({ error, onRetry }: { error: ApiError; onRetry?: () => void }) {
  const remedy = remedyFor(error);
  return (
    <div className="state-card is-error" role="alert">
      <span className="state-icon">
        <IconAlert />
      </span>
      <div>
        <span className="state-title">{error.message}</span>
        {remedy ? <span className="state-desc">{remedy}</span> : null}
        <span className="state-code">
          {error.code}
          {error.status > 0 ? " · HTTP " + error.status : ""}
        </span>
        {onRetry && error.retryable ? (
          <div style={{ marginTop: "var(--sp-2)" }}>
            <button type="button" className="btn btn-sm" onClick={onRetry}>
              다시 시도
            </button>
          </div>
        ) : null}
      </div>
    </div>
  );
}

/** Skeleton shaped like a chart, so the card does not resize when data lands. */
export function ChartSkeleton() {
  return (
    <div aria-busy="true" aria-label="차트를 불러오는 중">
      <div className="skel skel-chart" />
    </div>
  );
}

/** Skeleton shaped like a table body. */
export function TableSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <div aria-busy="true" aria-label="목록을 불러오는 중">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="skel skel-row" />
      ))}
    </div>
  );
}

export function LineSkeleton({ lines = 3 }: { lines?: number }) {
  return (
    <div aria-busy="true">
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} className="skel skel-line" style={{ width: i === lines - 1 ? "60%" : "100%" }} />
      ))}
    </div>
  );
}

export function Banner({
  tone = "info",
  title,
  description,
  action,
}: {
  tone?: "info" | "warn" | "crit";
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  const cls = tone === "warn" ? "banner is-warn" : tone === "crit" ? "banner is-crit" : "banner";
  return (
    <div className={cls} role={tone === "crit" ? "alert" : "status"}>
      <span className="b-icon">{tone === "info" ? <IconInfo /> : <IconAlert />}</span>
      <div className="b-text">
        <span className="b-title">{title}</span>
        {description ? <span className="b-desc">{description}</span> : null}
      </div>
      {action}
    </div>
  );
}
