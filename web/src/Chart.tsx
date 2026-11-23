import { useEffect, useRef, useState } from "react";
import { fmtNum } from "./format";

// JobsChart: jobs per bucket stacked as reviewed / skipped / failed (status
// colors), one y axis, a legend, and a hover/focus tooltip per bucket.

export type Point = {
  bucket: string;
  jobs?: number;
  done?: number;
  skipped?: number;
  failed?: number;
  comments?: number;
  tokens_in?: number;
  tokens_out?: number;
  duration_ms?: number;
  scored?: number;
  quality_sum?: number;
  security_sum?: number;
  maintainability_sum?: number;
};

export const series: [keyof Point, string, string][] = [
  ["done", "Reviewed", "--st-done"],
  ["skipped", "Skipped", "--st-skip"],
  ["failed", "Failed", "--st-fail"],
];

export type Metrics = { granularity: string; from: string; to: string; points: Point[]; today_tokens: number; daily_token_budget: number };

// Fill empty buckets so the time axis is continuous.
export function fillBuckets(d: Metrics): Point[] {
  const out: Point[] = [];
  const by: Record<string, Point> = {};
  d.points.forEach((p) => (by[new Date(p.bucket).toISOString()] = p));
  const end = new Date(d.to);
  const start = new Date(d.from);
  if (d.granularity === "month") {
    let t = new Date(Date.UTC(start.getUTCFullYear(), start.getUTCMonth(), 1));
    while (t < end) {
      out.push(by[t.toISOString()] || { bucket: t.toISOString() });
      t = new Date(Date.UTC(t.getUTCFullYear(), t.getUTCMonth() + 1, 1));
    }
    return out;
  }
  const step = d.granularity === "hour" ? 3600e3 : 86400e3;
  let t = new Date(Math.floor(start.getTime() / step) * step);
  while (t < end) {
    out.push(by[t.toISOString()] || { bucket: t.toISOString() });
    t = new Date(t.getTime() + step);
  }
  return out;
}

export function bucketLabel(iso: string, g: string, long = false): string {
  const d = new Date(iso);
  if (g === "hour") return long ? d.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) : d.toLocaleTimeString([], { hour: "2-digit" });
  if (g === "month") return d.toLocaleDateString([], { month: "short", year: long ? "numeric" : "2-digit" });
  return d.toLocaleDateString([], { month: "short", day: "numeric" });
}

const n = (p: Point, k: keyof Point) => (typeof p[k] === "number" ? (p[k] as number) : 0);

export default function JobsChart({ points, granularity }: { points: Point[]; granularity: string }) {
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
  const W = width, H = 200, L = 34, R = 8, T = 8, B = 24;
  let max = 0;
  points.forEach((p) => (max = Math.max(max, n(p, "done") + n(p, "skipped") + n(p, "failed"))));
  const nice = max <= 4 ? 4 : Math.pow(10, Math.floor(Math.log10(max)));
  const top = Math.ceil(max / nice) * nice || 4;
  const band = (W - L - R) / Math.max(1, points.length);
  const bw = Math.min(24, Math.max(2, band * 0.7));
  const y = (v: number) => T + (H - T - B) * (1 - v / top);
  const every = Math.ceil(points.length / Math.max(2, Math.floor((W - L - R) / 70)));
  const hp = hover !== null ? points[hover] : null;
  const tipLeft = hover !== null ? Math.min(W - 170, Math.max(0, L + band * hover + band / 2 + 12)) : 0;
  return (
    <div className="chart relative w-full" ref={box}>
      <svg className="block h-auto w-full" viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Jobs per ${granularity}: reviewed, skipped and failed`}>
        {points.map((_, i) => (
          <rect key={"h" + i} x={L + band * i} y={T} width={band} height={H - T - B} fill={hover === i ? "var(--muted)" : "transparent"} fillOpacity={0.5}
            tabIndex={0} onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} onFocus={() => setHover(i)} onBlur={() => setHover(null)} />
        ))}
        {[0, top / 2, top].map((v) => (
          <g key={"g" + v} pointerEvents="none">
            <line x1={L} x2={W - R} y1={y(v)} y2={y(v)} stroke="var(--grid)" strokeWidth={1} />
            <text x={L - 6} y={y(v) + 4} textAnchor="end" fontSize={11} fill="var(--muted-foreground)">
              {fmtNum(v)}
            </text>
          </g>
        ))}
        {points.map((p, i) => {
          const x = L + band * i + (band - bw) / 2;
          const segs = series.map(([k, , c]) => [n(p, k), c] as [number, string]).filter(([v]) => v > 0);
          let acc = 0;
          return (
            <g key={"b" + i} pointerEvents="none">
              {segs.map(([v, c], k) => {
                // Stack upward from the baseline; a 2px surface gap under every
                // segment but the first; only the top segment gets rounded ends.
                let yTop = y(acc + v);
                const yBot = y(acc) - (k > 0 ? 2 : 0);
                if (yBot - yTop < 1) yTop = yBot - 1;
                const r = k === segs.length - 1 ? Math.min(4, bw / 2, yBot - yTop) : 0;
                acc += v;
                const d = `M${x},${yBot}V${yTop + r}Q${x},${yTop} ${x + r},${yTop}H${x + bw - r}Q${x + bw},${yTop} ${x + bw},${yTop + r}V${yBot}Z`;
                return <path key={k} d={d} fill={`var(${c})`} />;
              })}
              {i % every === 0 && (
                <text x={L + band * i + band / 2} y={H - B + 16} textAnchor="middle" fontSize={11} fill="var(--muted-foreground)">
                  {bucketLabel(p.bucket, granularity)}
                </text>
              )}
            </g>
          );
        })}
        <line x1={L} x2={W - R} y1={H - B} y2={H - B} stroke="var(--grid)" strokeWidth={1} pointerEvents="none" />
      </svg>
      <div className={"tip pointer-events-none absolute z-10 w-40 rounded-md border bg-popover p-2.5 text-xs text-popover-foreground shadow-md [&>div]:flex [&>div]:justify-between [&>div]:gap-3 [&>div>span:first-child]:text-muted-foreground " + (hp ? "show block" : "hidden")} id="mTip" ref={(el) => el && (el.style.left = tipLeft + "px", el.style.top = "28px")}>
        {hp && (
          <>
            <b className="mb-1 block font-semibold">{bucketLabel(hp.bucket, granularity, true)}</b>
            {series.map(([k, label]) => (
              <div key={k}>
                <span>{label}</span>
                <span>{n(hp, k)}</span>
              </div>
            ))}
            <div>
              <span>Comments</span>
              <span>{n(hp, "comments")}</span>
            </div>
            <div>
              <span>Tokens</span>
              <span>{fmtNum(n(hp, "tokens_in") + n(hp, "tokens_out"))}</span>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
