import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";

// Unsaved changes, page-wide (issue #110). Each form with its own Save
// registers what it has changed; one bar at the bottom of the window says
// so and saves or discards it, and leaving the page asks first.

export type Unsaved = { label: string; count: number; save: () => unknown; discard: () => void };

const entries = new Map<string, Unsaved>();
const listeners = new Set<() => void>();
const notify = () => listeners.forEach((f) => f());

export const hasUnsaved = () => entries.size > 0;

// useUnsaved registers a form's unsaved changes under key while u is set
// (count above 0), and removes them when it isn't or the form goes away.
export function useUnsaved(key: string, u: Unsaved | null) {
  const latest = useRef(u);
  latest.current = u;
  const dirty = !!u && u.count > 0;
  useEffect(() => {
    if (!dirty) return;
    // Save and discard read the form's latest state, not this render's.
    entries.set(key, {
      get label() { return latest.current?.label ?? ""; },
      get count() { return latest.current?.count ?? 0; },
      save: () => latest.current?.save(),
      discard: () => latest.current?.discard(),
    });
    notify();
    return () => {
      entries.delete(key);
      notify();
    };
  }, [key, dirty]);
  useEffect(() => {
    if (dirty) notify(); // the count changed
  }, [dirty, u?.count]);
}

const leaveMessage = "You have unsaved changes. Leave this page and lose them?";

// UnsavedBar is the bar at the bottom of the window, with the guard that
// keeps the changes from being lost by leaving the page.
export function UnsavedBar() {
  const [, render] = useState(0);
  useEffect(() => {
    const f = () => render((n) => n + 1);
    listeners.add(f);
    return () => {
      listeners.delete(f);
    };
  }, []);
  useEffect(() => {
    const beforeUnload = (e: BeforeUnloadEvent) => {
      if (!hasUnsaved()) return;
      e.preventDefault();
      e.returnValue = leaveMessage;
    };
    // A link in the panel (the sidebar, a link on the page) changes the
    // page without unloading it: ask first.
    const click = (e: MouseEvent) => {
      if (!hasUnsaved() || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const a = (e.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
      if (!a || a.target === "_blank" || a.hasAttribute("download")) return;
      const to = new URL(a.href, location.href);
      if (to.origin !== location.origin || (to.pathname === location.pathname && to.search === location.search)) return;
      if (!window.confirm(leaveMessage)) {
        e.preventDefault();
        e.stopPropagation();
      }
    };
    window.addEventListener("beforeunload", beforeUnload);
    document.addEventListener("click", click, true);
    return () => {
      window.removeEventListener("beforeunload", beforeUnload);
      document.removeEventListener("click", click, true);
    };
  }, []);
  const list = [...entries.entries()];
  if (!list.length) return null;
  return (
    <div role="region" aria-label="Unsaved changes" className="sticky bottom-0 z-20 border-t bg-background/95 px-4 py-3 shadow-[0_-4px_12px_rgb(0_0_0/0.06)] backdrop-blur md:px-6">
      <div className="mx-auto grid w-full max-w-7xl gap-2">
        {list.map(([key, u]) => (
          <div key={key} className="flex flex-wrap items-center gap-3 text-sm">
            <span className="size-2 shrink-0 rounded-full bg-warning" aria-hidden />
            <span className="font-medium">{u.label}:</span>
            <span className="text-muted-foreground">
              {u.count} unsaved change{u.count === 1 ? "" : "s"}
            </span>
            <span className="flex-1" />
            <Button variant="ghost" size="sm" onClick={() => u.discard()}>
              Discard
            </Button>
            <Button size="sm" onClick={() => u.save()}>
              Save
            </Button>
          </div>
        ))}
      </div>
    </div>
  );
}

// UnsavedBadge marks a card whose controls have unsaved changes.
export function UnsavedBadge({ show }: { show: boolean }) {
  if (!show) return null;
  return <span className="ml-2 rounded-md bg-warning/15 px-1.5 py-0.5 align-middle text-xs font-medium text-warning">Unsaved</span>;
}

// diffCount counts the fields of a that differ from b (compared as JSON).
export function diffCount<T extends object>(a: T, b: T, keys?: (keyof T)[]): number {
  const ks = keys ?? (Array.from(new Set([...Object.keys(a), ...Object.keys(b)])) as (keyof T)[]);
  return ks.filter((k) => JSON.stringify(a[k] ?? null) !== JSON.stringify(b[k] ?? null)).length;
}
