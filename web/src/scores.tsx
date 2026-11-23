import { useEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import { bucketLabel } from "./Chart";

// Review scores (1 to 100) as the CLI reports them, with the same bands as
// its score labels: 80+ good, 50 to 79 needs work, below 50 poor.

export type Scores = { quality: number; security: number; maintainability: number };
export type ScoreKind = keyof Scores;
export const scoreKinds: [ScoreKind, string, string][] = [
  ["quality", "Quality", "--sc-quality"],
  ["security", "Security", "--sc-security"],
  ["maintainability", "Maintainability", "--sc-maintainability"],
];

// A rollup bucket's score sums (see store.MetricPoint).
export type ScoreSums = { bucket: string; scored?: number; quality_sum?: number; security_sum?: number; maintainability_sum?: number };

export function avg(p: ScoreSums, k: ScoreKind): number | null {
  const n = p.scored || 0;
  return n ? ((p[(k + "_sum") as keyof ScoreSums] as number) || 0) / n : null;
}

export function bandClass(v: number): string {
  if (v >= 80) return "bg-success-soft text-success";
  if (v >= 50) return "bg-warning-soft text-warning";
  return "bg-danger-soft text-destructive";
}

// ScoreChips shows the three scores as small colour-banded chips.
export function ScoreChips({ s, className }: { s?: Scores | null; className?: string }) {
  if (!s) return <span className="text-xs text-muted-foreground">–</span>;
  return (
    <span className={cn("inline-flex gap-1", className)}>
      {scoreKinds.map(([k, label]) => (
        <span key={k} title={`${label} ${s[k]}`} className={cn("inline-flex min-w-9 items-center justify-center rounded px-1.5 py-0.5 text-xs font-medium tabular-nums", bandClass(s[k]))}>
          <span className="mr-0.5 opacity-70">{label[0]}</span>
          {s[k]}
        </span>
      ))}
    </span>
  );
}

function lineFor(points: ScoreSums[], k: ScoreKind, x: (i: number) => number, y: (v: number) => number): string {
  let d = "";
  let pen = false;
  points.forEach((p, i) => {
    const v = avg(p, k);
    if (v === null) {
      pen = false;
      return;
    }
    d += (pen ? "L" : "M") + x(i).toFixed(1) + "," + y(v).toFixed(1);
    pen = true;
  });
  return d;
}

// Sparkline draws the three daily averages in a small box (gaps where a
// day had no scored review).
export function Sparkline({ points, label }: { points: ScoreSums[]; label: string }) {
  const W = 96, H = 24;
  if (points.filter((p) => p.scored).length < 2) return <span className="text-xs text-muted-foreground">–</span>;
  const x = (i: number) => (points.length < 2 ? W / 2 : 2 + ((W - 4) * i) / (points.length - 1));
  const y = (v: number) => 2 + (H - 4) * (1 - v / 100);
  return (
    <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} role="img" aria-label={label} className="block">
      {scoreKinds.map(([k, , c]) => {
        const d = lineFor(points, k, x, y);
        return d.includes("L") ? <path key={k} d={d} fill="none" stroke={`var(${c})`} strokeWidth={1.5} strokeLinejoin="round" /> : null;
      })}
    </svg>
  );
}

// ScoreChart is the average of each score per bucket, on a 0 to 100 axis,
// with a hover/focus tooltip per bucket.
export function ScoreChart({ points, granularity }: { points: ScoreSums[]; granularity: string }) {
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(900);
  const [hover, setHover] = useState<number | null>(null);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(Math.max(320, el.clientWidth || 900)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const W = width, H = 160, L = 34, R = 8, T = 8, B = 24;
  const band = (W - L - R) / Math.max(1, points.length);
  const x = (i: number) => L + band * i + band / 2;
  const y = (v: number) => T + (H - T - B) * (1 - v / 100);
  const every = Math.ceil(points.length / Math.max(2, Math.floor((W - L - R) / 70)));
  const hp = hover !== null ? points[hover] : null;
  const tipLeft = hover !== null ? Math.min(W - 170, Math.max(0, x(hover) + 12)) : 0;
  return (
    <div className="relative w-full" ref={box}>
      <svg className="block h-auto w-full" viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Average review scores per ${granularity}`}>
        {points.map((_, i) => (
          <rect key={"h" + i} x={L + band * i} y={T} width={band} height={H - T - B} fill={hover === i ? "var(--muted)" : "transparent"} fillOpacity={0.5}
            tabIndex={0} onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} onFocus={() => setHover(i)} onBlur={() => setHover(null)} />
        ))}
        {[0, 50, 80, 100].map((v) => (
          <g key={"g" + v} pointerEvents="none">
            <line x1={L} x2={W - R} y1={y(v)} y2={y(v)} stroke="var(--grid)" strokeWidth={1} strokeDasharray={v === 50 || v === 80 ? "3 3" : undefined} />
            <text x={L - 6} y={y(v) + 4} textAnchor="end" fontSize={11} fill="var(--muted-foreground)">
              {v}
            </text>
          </g>
        ))}
        {scoreKinds.map(([k, , c]) => (
          <g key={k} pointerEvents="none">
            <path d={lineFor(points, k, x, y)} fill="none" stroke={`var(${c})`} strokeWidth={2} strokeLinejoin="round" />
            {points.map((p, i) => {
              const v = avg(p, k);
              return v === null ? null : <circle key={i} cx={x(i)} cy={y(v)} r={hover === i ? 3.5 : 2} fill={`var(${c})`} />;
            })}
          </g>
        ))}
        {points.map((p, i) =>
          i % every === 0 ? (
            <text key={"t" + i} x={x(i)} y={H - B + 16} textAnchor="middle" fontSize={11} fill="var(--muted-foreground)" pointerEvents="none">
              {bucketLabel(p.bucket, granularity)}
            </text>
          ) : null,
        )}
      </svg>
      <div className={"pointer-events-none absolute z-10 w-40 rounded-md border bg-popover p-2.5 text-xs text-popover-foreground shadow-md [&>div]:flex [&>div]:justify-between [&>div]:gap-3 [&>div>span:first-child]:text-muted-foreground " + (hp ? "block" : "hidden")} ref={(el) => el && ((el.style.left = tipLeft + "px"), (el.style.top = "20px"))}>
        {hp && (
          <>
            <b className="mb-1 block font-semibold">{bucketLabel(hp.bucket, granularity, true)}</b>
            {scoreKinds.map(([k, label]) => {
              const v = avg(hp, k);
              return (
                <div key={k}>
                  <span>{label}</span>
                  <span>{v === null ? "–" : Math.round(v)}</span>
                </div>
              );
            })}
            <div>
              <span>Scored reviews</span>
              <span>{hp.scored || 0}</span>
            </div>
          </>
        )}
      </div>
    </div>
  );
}

// ScoreLegend names the three lines.
export function ScoreLegend({ values }: { values?: Partial<Record<ScoreKind, number | null>> }) {
  return (
    <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
      {scoreKinds.map(([k, label, c]) => (
        <span key={k} className="flex items-center gap-1.5">
          <span className="h-0.5 w-3 rounded-sm" ref={(el) => el?.style.setProperty("background", `var(${c})`)} />
          {label}
          {values && values[k] != null ? " " + Math.round(values[k] as number) : ""}
        </span>
      ))}
    </div>
  );
}
