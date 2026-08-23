"use client";

/**
 * The one data-fetching hook.
 *
 * Every explorer view has the same three-state problem (05 section 7): show a
 * skeleton, show an error card with the obsplane code, or show data - and, on a
 * refetch, keep the previous data on screen so the layout does not collapse
 * while the new query runs. Solving that once here is why no component calls
 * the endpoint module directly.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError } from "@/lib/api/client";

export interface AsyncState<T> {
  data: T | undefined;
  error: ApiError | undefined;
  /** True on the very first load: no data has ever arrived. */
  loading: boolean;
  /** True while a refetch runs on top of data already displayed. */
  refreshing: boolean;
  reload: () => void;
}

/**
 * Run `fn` whenever `deps` change, with the in-flight request aborted on the
 * next change so a slow first query cannot overwrite a fast second one.
 *
 * `fn` must be stable or wrapped by the caller in useCallback - it is not in
 * the dependency array, because a fresh closure on every render would loop.
 */
export function useAsync<T>(
  fn: (signal: AbortSignal) => Promise<T>,
  deps: React.DependencyList,
  options: { enabled?: boolean } = {},
): AsyncState<T> {
  const enabled = options.enabled ?? true;

  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [pending, setPending] = useState(enabled);
  const [nonce, setNonce] = useState(0);

  const fnRef = useRef(fn);
  fnRef.current = fn;
  const hasData = data !== undefined;

  useEffect(() => {
    if (!enabled) {
      setPending(false);
      return;
    }
    const ctrl = new AbortController();
    let live = true;
    setPending(true);

    fnRef
      .current(ctrl.signal)
      .then((value) => {
        if (!live) return;
        setData(value);
        setError(undefined);
      })
      .catch((err: unknown) => {
        if (!live) return;
        // An abort is this hook superseding itself, not a failure to report.
        if (err instanceof DOMException && err.name === "AbortError") return;
        setError(
          err instanceof ApiError
            ? err
            : new ApiError("INTERNAL_ERROR", err instanceof Error ? err.message : String(err), 0),
        );
      })
      .finally(() => {
        if (live) setPending(false);
      });

    return () => {
      live = false;
      ctrl.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, enabled, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  return {
    data,
    error,
    loading: pending && !hasData,
    refreshing: pending && hasData,
    reload,
  };
}
