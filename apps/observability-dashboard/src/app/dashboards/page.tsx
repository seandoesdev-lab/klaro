"use client";

/**
 * Saved dashboards [OBS-09].
 *
 * The design defers the full panel editor to M2+, so this is the list plus the
 * minimum that makes a dashboard usable: create with a name, rename, delete,
 * and open. Panel layout editing lives in the detail view.
 */

import Link from "next/link";
import { useCallback, useState } from "react";
import { EmptyState, ErrorState, TableSkeleton } from "@/components/States";
import { useAsync } from "@/hooks/useAsync";
import { ApiError } from "@/lib/api/client";
import { createDashboard, deleteDashboard, listDashboards } from "@/lib/api/endpoints";
import type { Dashboard } from "@/lib/api/types";
import { formatAgo } from "@/lib/format";

export default function DashboardsPage() {
  const { data, error, loading, reload } = useAsync(useCallback((s: AbortSignal) => listDashboards(s), []), []);

  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [opError, setOpError] = useState<ApiError | null>(null);

  const create = async () => {
    setBusy(true);
    setOpError(null);
    try {
      await createDashboard({ name, description, spec: { panels: [], range_sec: 3600, refresh_sec: 30 } });
      setCreating(false);
      setName("");
      setDescription("");
      reload();
    } catch (err) {
      setOpError(err instanceof ApiError ? err : new ApiError("INTERNAL_ERROR", String(err), 0));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (d: Dashboard) => {
    if (!window.confirm(d.name + " 대시보드를 삭제할까요?")) return;
    try {
      await deleteDashboard(d.id);
      reload();
    } catch (err) {
      setOpError(err instanceof ApiError ? err : new ApiError("INTERNAL_ERROR", String(err), 0));
    }
  };

  return (
    <div className="view">
      <div className="page-head">
        <h1>대시보드</h1>
        <span className="page-sub">저장된 패널 레이아웃 · OBS-09</span>
        <div className="page-actions">
          <button type="button" className="btn btn-primary" onClick={() => setCreating(true)}>
            + 새 대시보드
          </button>
        </div>
      </div>

      {opError ? <ErrorState error={opError} /> : null}

      <div className="card">
        <div className="card-head">
          <h2>목록</h2>
          <span className="spacer" />
          <button type="button" className="btn btn-sm" onClick={reload} disabled={loading}>
            새로고침
          </button>
        </div>

        {error ? <ErrorState error={error} onRetry={reload} /> : null}
        {!error && loading ? <TableSkeleton rows={3} /> : null}

        {!error && !loading && (data?.length ?? 0) === 0 ? (
          <EmptyState
            title="저장된 대시보드가 없습니다"
            description="자주 보는 패널을 모아두면 매번 조건을 다시 입력하지 않아도 됩니다."
            action={
              <button type="button" className="btn btn-primary" onClick={() => setCreating(true)}>
                첫 대시보드 만들기
              </button>
            }
          />
        ) : null}

        {(data?.length ?? 0) > 0 ? (
          <div className="tbl-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>이름</th>
                  <th>패널</th>
                  <th>기본 구간</th>
                  <th>자동 갱신</th>
                  <th>수정</th>
                  <th className="ta-r">작업</th>
                </tr>
              </thead>
              <tbody>
                {(data ?? []).map((d) => (
                  <tr key={d.id}>
                    <td>
                      <Link href={"/dashboards/" + d.id} className="cell-main" style={{ textDecoration: "none" }}>
                        {d.name}
                      </Link>
                      {d.description ? <span className="cell-sub">{d.description}</span> : null}
                    </td>
                    <td className="num">{d.spec.panels.length}</td>
                    <td className="num">{d.spec.range_sec ? Math.round(d.spec.range_sec / 60) + "분" : "—"}</td>
                    <td className="num">{d.spec.refresh_sec ? d.spec.refresh_sec + "초" : "수동"}</td>
                    <td className="cell-sub">{formatAgo(d.updated_at)}</td>
                    <td className="ta-r">
                      <Link href={"/dashboards/" + d.id} className="btn btn-sm">
                        열기
                      </Link>{" "}
                      <button type="button" className="btn btn-sm btn-danger" onClick={() => remove(d)}>
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

      {creating ? (
        <div className="overlay" role="dialog" aria-modal="true" aria-label="새 대시보드">
          <div className="modal-card">
            <h2>새 대시보드</h2>
            <p className="card-sub">패널은 만든 뒤 상세 화면에서 추가합니다.</p>
            <div className="stack">
              <div className="field">
                <label htmlFor="d-name">이름</label>
                <input
                  id="d-name"
                  className="input"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="체크아웃 서비스 개요"
                />
              </div>
              <div className="field">
                <label htmlFor="d-desc">설명</label>
                <textarea
                  id="d-desc"
                  className="input"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                  placeholder="이 대시보드가 답하는 질문"
                />
              </div>
            </div>
            <div className="modal-actions">
              <button type="button" className="btn" onClick={() => setCreating(false)} disabled={busy}>
                취소
              </button>
              <button
                type="button"
                className="btn btn-primary"
                onClick={create}
                disabled={busy || name.trim() === ""}
              >
                {busy ? "만드는 중…" : "만들기"}
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
