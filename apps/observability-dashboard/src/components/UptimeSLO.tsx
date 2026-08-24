"use client";

/**
 * Uptime / availability SLO widget (P1b).
 *
 * It answers one question - "did the fleet meet its objective over this
 * window" - and then makes the answer actionable by showing the error budget
 * rather than only the achievement. 99.2% against a 99% target sounds fine
 * until you see that four fifths of the month's budget is already spent.
 *
 * The honesty rules the design asks for (05 section 7) are load-bearing here:
 *
 *   - availability is reporting coverage, not reachability. obsplane cannot
 *     observe a host it never hears from, so the label says "보고 커버리지"
 *     rather than implying a synthetic uptime check that does not exist.
 *   - an org with no host metrics gets an empty state, never 100%. A ratio
 *     computed from zero buckets would be a perfect score for a fleet nobody
 *     is watching, which is the most dangerous number this screen could show.
 */

import type { UptimeResult } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { EmptyState } from "./States";

/** Achievement as a percentage with enough digits to see the budget move. */
function pct(ratio: number): string {
  return (ratio * 100).toFixed(ratio >= 0.999 ? 3 : 2) + "%";
}

/**
 * Share of the error budget already consumed, 0..1.
 *
 * budget = 1 - target; spent = (1 - availability) / budget. A target of 1
 * leaves no budget at all, so any gap is 100% spent rather than a divide by
 * zero.
 */
function budgetSpent(availability: number, target: number): number {
  const budget = 1 - target;
  if (budget <= 0) return availability >= 1 ? 0 : 1;
  return Math.min(1, Math.max(0, (1 - availability) / budget));
}

function toneFor(spent: number): "good" | "warn" | "crit" {
  if (spent >= 1) return "crit";
  if (spent >= 0.75) return "warn";
  return "good";
}

export function UptimeSLOWidget({ data }: { data: UptimeResult }) {
  const hasData = data.expected_buckets > 0 && data.hosts.length > 0;

  if (!hasData) {
    return (
      <EmptyState
        title="가용률을 계산할 호스트 메트릭이 없습니다"
        description="이 구간에 호스트가 보고한 샘플이 없습니다. 0%가 아니라 '측정할 데이터가 없음'입니다 — 호스트 에이전트가 수집 키로 메트릭을 보내고 있는지 확인하세요."
      />
    );
  }

  const spent = budgetSpent(data.availability, data.target);
  const tone = toneFor(spent);
  const met = data.availability >= data.target;
  const missed = data.hosts.filter((h) => !h.met);

  return (
    <div className="slo">
      <div className="slo-head">
        <div>
          <span className="microlabel">보고 커버리지 · 목표 {pct(data.target)}</span>
          <div className={"slo-value is-" + tone}>{pct(data.availability)}</div>
          <span className="hint">
            {formatDateTime(data.from)} ~ {formatDateTime(data.to)} · {data.step_sec}초 버킷
          </span>
        </div>
        <span className={met ? "badge badge-good" : "badge badge-crit"}>
          {met ? "목표 달성" : "목표 미달"}
        </span>
      </div>

      {/*
        The bar shows the budget, not the achievement: a 99.9% bar is visually
        indistinguishable from a 100% one, while "83% of the budget is gone" is
        a shape you can read across the room.
      */}
      <div
        className="slo-bar"
        role="img"
        aria-label={"오류 예산 소진 " + Math.round(spent * 100) + "%"}
      >
        <span className={"slo-fill is-" + tone} style={{ width: (spent * 100).toFixed(1) + "%" }} />
      </div>
      <div className="slo-foot">
        <span>오류 예산 소진 {(spent * 100).toFixed(1)}%</span>
        <span className="num">
          {data.observed_buckets.toLocaleString("ko-KR")} /{" "}
          {data.expected_buckets.toLocaleString("ko-KR")} 버킷 보고
        </span>
      </div>

      {data.clamped ? (
        <p className="hint">
          플랜 보존 기간을 넘는 구간은 잘려서 조회되었습니다. 위 달성률은 실제 조회된 구간
          기준입니다.
        </p>
      ) : null}

      {missed.length > 0 ? (
        <div className="slo-misses">
          <span className="microlabel">목표 미달 호스트 {missed.length}대</span>
          <ul>
            {missed.slice(0, 5).map((h) => (
              <li key={h.host_ident}>
                <span className="mono">{h.host_ident}</span>
                <span className="num">{pct(h.availability)}</span>
              </li>
            ))}
          </ul>
          {missed.length > 5 ? <span className="hint">외 {missed.length - 5}대</span> : null}
        </div>
      ) : null}
    </div>
  );
}
