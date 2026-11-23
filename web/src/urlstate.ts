import { useCallback, useMemo } from "react";
import { useSearchParams } from "react-router-dom";

// useQueryState keeps a page's filters in the query string, so a reload,
// Back from another page, a bookmark or a shared link shows the same view.
// The URL is the only copy: values equal to their default are left out of
// it. Names follow the page's API (score_kind, score_below, ...).
export function useQueryState<T extends Record<string, string>>(defaults: T): [T, (patch: Partial<T>) => void] {
  const [params, setParams] = useSearchParams();
  const key = params.toString();
  const state = useMemo(() => {
    const s = { ...defaults };
    for (const k of Object.keys(defaults) as (keyof T)[]) {
      const v = params.get(k as string);
      if (v !== null) s[k] = v as T[keyof T];
    }
    return s;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  const set = useCallback(
    (patch: Partial<T>) =>
      setParams(
        (cur) => {
          const next = new URLSearchParams(cur);
          for (const [k, v] of Object.entries(patch)) {
            if (v === undefined || v === "" || v === defaults[k]) next.delete(k);
            else next.set(k, v as string);
          }
          return next;
        },
        { replace: true },
      ),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [setParams],
  );
  return [state, set];
}
