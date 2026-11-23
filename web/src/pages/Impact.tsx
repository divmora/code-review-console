import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { get } from "../api";
import JobsChart, { fillBuckets, Point, Series } from "../Chart";
import { fmtDate, fmtDur, fmtNum } from "../format";
import { avg, ScoreChart, ScoreLegend, ScoreSums, scoreKinds } from "../scores";
import { Empty, errMsg, Locked, NativeSelect, Note, Panel, Sub, Toolbar, useFeature, useToast } from "../ui";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

// Impact: what the console has done for this installation, read from its
// own database (nothing leaves it).

type Count = { severity?: string; category?: string; platform?: string; host?: string; repo?: string; count: number };
type ImpactPoint = { bucket: string; reviews: number; findings: Record<string, number>; scored: number; quality_sum: number; security_sum: number; maintainability_sum: number };
type Impact = {
  range: string; from: string; to: string; granularity: string; licensed: boolean;
  prs_reviewed: number; review_runs: number; active_repos: number; findings: number; by_severity: Record<string, number>;
  by_category: Count[]; security_findings: number; comments: number; first_reviews: number; median_first_review_ms: number;
  scored: number; quality_sum: number; security_sum: number; maintainability_sum: number;
  trend: ImpactPoint[]; top_by_reviews: Count[]; top_by_findings: Count[];
};

const severities: [string, string, string][] = [
  ["critical", "Critical", "--st-fail"],
  ["major", "Major", "--warning"],
  ["minor", "Minor", "--sc-quality"],
  ["nitpick", "Nitpick", "--st-skip"],
  ["other", "Other", "--muted-foreground"],
];
const ranges: [string, string][] = [
  ["30d", "Last 30 days"],
  ["90d", "Last 90 days"],
  ["365d", "Last 12 months"],
  ["all", "All history"],
];

// reviewsLink opens Reviews filtered to what a number on this page
// counts, over the same period (the Reviews "to" date is inclusive).
function reviewsLink(d: Impact, filters: Record<string, string>): string {
  const q = new URLSearchParams(filters);
  if (d.range !== "all") {
    q.set("from", d.from.slice(0, 10));
    q.set("to", new Date(new Date(d.to).getTime() - 864e5).toISOString().slice(0, 10));
  }
  return "/reviews?" + q.toString();
}

function Tile({ value, label, sub, to }: { value: string; label: string; sub?: string; to?: string }) {
  const body = (
    <CardContent className="px-4">
      <div className="text-xl font-semibold tabular-nums">{value}</div>
      <div className="text-xs text-muted-foreground">{label}</div>
      {sub && <div className="mt-1 text-xs text-muted-foreground">{sub}</div>}
    </CardContent>
  );
  return (
    <Card className={"tile gap-1 py-4" + (to ? " transition-colors hover:border-primary/50 hover:bg-accent/40" : "")}>
      {to ? (
        <Link to={to} className="rounded-xl focus-visible:outline-2 focus-visible:outline-ring" aria-label={`${label}: ${value} (open in Reviews)`}>
          {body}
        </Link>
      ) : (
        body
      )}
    </Card>
  );
}

function Section({ title, description, children }: { title: string; description?: string; children: React.ReactNode }) {
  return (
    <Card className="py-4">
      <CardHeader className="px-4">
        <CardTitle className="h2">{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="grid gap-3 px-4">{children}</CardContent>
    </Card>
  );
}

function Legend({ items }: { items: [string, string, string][] }) {
  return (
    <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
      {items.map(([k, label, c]) => (
        <span key={k} className="flex items-center gap-1.5">
          <span className="size-2.5 rounded-sm" ref={(el) => el?.style.setProperty("background", `var(${c})`)} />
          {label}
        </span>
      ))}
    </div>
  );
}

function RepoTable({ rows, unit, link }: { rows: Count[]; unit: string; link: (r: Count) => string }) {
  if (!rows.length) return <Sub>Nothing in this period.</Sub>;
  return (
    <Panel>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="pl-4">Repository</TableHead>
            <TableHead className="pr-4 text-right">{unit}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r) => (
            <TableRow key={`${r.platform}|${r.host}|${r.repo}`}>
              <TableCell className="pl-4">
                <Link to={link(r)} className="font-medium underline-offset-2 hover:underline">
                  {r.repo}
                </Link>
                <Sub>
                  {r.platform} · {r.host}
                </Sub>
              </TableCell>
              <TableCell className="pr-4 text-right tabular-nums">{fmtNum(r.count)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Panel>
  );
}

// Bars shows counts as horizontal bars, largest first; a row with a link
// opens the reviews behind it.
function Bars({ rows }: { rows: [string, number, string?, string?][] }) {
  const max = Math.max(1, ...rows.map(([, n]) => n));
  if (!rows.some(([, n]) => n > 0)) return <Sub>No findings in this period.</Sub>;
  return (
    <div className="grid gap-1.5">
      {rows.map(([label, v, c, to]) => (
        <div key={label} className="grid grid-cols-[8rem_1fr_3.5rem] items-center gap-2 text-sm">
          {to && v > 0 ? (
            <Link to={to} className="truncate capitalize underline-offset-2 hover:underline">
              {label}
            </Link>
          ) : (
            <span className="truncate capitalize">{label}</span>
          )}
          <span className="h-2 rounded-sm bg-muted">
            <span className="block h-2 rounded-sm" ref={(el) => el && (el.style.width = (100 * v) / max + "%", el.style.background = `var(${c || "--primary"})`)} />
          </span>
          <span className="text-right text-muted-foreground tabular-nums">{fmtNum(v)}</span>
        </div>
      ))}
    </div>
  );
}

export default function ImpactPage() {
  const toast = useToast();
  const licensed = useFeature("metrics");
  const [range, setRange] = useState("30d");
  const [d, setD] = useState<Impact | null>(null);
  useEffect(() => {
    setD(null);
    get<Impact>("/admin/api/impact?range=" + range).then(setD).catch((e) => toast(errMsg(e), true));
  }, [range]);

  const pts: (Point & ScoreSums & Record<string, unknown>)[] = d
    ? (fillBuckets({ granularity: d.granularity, from: d.from, to: d.to, today_tokens: 0, daily_token_budget: 0,
        points: d.trend.map((p) => ({ bucket: p.bucket, reviews: p.reviews, ...p.findings, scored: p.scored, quality_sum: p.quality_sum, security_sum: p.security_sum, maintainability_sum: p.maintainability_sum } as Point)) }) as any[])
    : [];
  const shown = d?.range === "all" ? pts.slice(Math.max(0, pts.findIndex((p) => (p as any).reviews))) : pts;
  const scoreAvg = d ? Object.fromEntries(scoreKinds.map(([k]) => [k, avg({ bucket: "", ...d }, k)])) : {};
  // The export link uses the range from the fixed list above, never text
  // read back from the page.
  const knownRange = ranges.find(([v]) => v === range)?.[0] ?? "30d";
  const exportURL = (format: "csv" | "json") => "/admin/api/impact/export?" + new URLSearchParams({ range: knownRange, format }).toString();
  const sevSplit = d ? severities.filter(([k]) => d.by_severity[k]).map(([k, label]) => `${fmtNum(d.by_severity[k])} ${label.toLowerCase()}`).join(" · ") : "";

  return (
    <section className="grid gap-4">
      <Toolbar>
        <NativeSelect className="w-auto" aria-label="Period" value={range} onChange={(e) => setRange(e.target.value)}>
          {ranges.map(([v, label]) => (
            <option key={v} value={v} disabled={!licensed && v !== "30d"}>
              {label}
              {!licensed && v !== "30d" ? " (licensed)" : ""}
            </option>
          ))}
        </NativeSelect>
        {d && (
          <span className="text-sm text-muted-foreground">
            {d.range === "all" ? "Everything recorded" : fmtDate(d.from) + " – " + fmtDate(new Date(new Date(d.to).getTime() - 1).toISOString())} (UTC)
          </span>
        )}
        <span className="flex-1" />
        {licensed && (
          <>
            <Button asChild variant="outline" size="sm">
              <a href={exportURL("csv")}>Export CSV</a>
            </Button>
            <Button asChild variant="outline" size="sm">
              <a href={exportURL("json")}>Export JSON</a>
            </Button>
          </>
        )}
      </Toolbar>

      {!d ? (
        <Empty>Loading…</Empty>
      ) : (
        <>
          <div className="tiles grid grid-cols-2 gap-3 md:grid-cols-4">
            <Tile value={fmtNum(d.prs_reviewed)} label="Merge requests reviewed" sub={fmtNum(d.review_runs) + " review runs"} to={reviewsLink(d, { state: "done" })} />
            <Tile value={fmtNum(d.findings)} label="Findings raised" sub={sevSplit || undefined} to={d.findings ? reviewsLink(d, { severity: "any" }) : undefined} />
            <Tile value={fmtNum(d.security_findings)} label="Security findings caught" sub="before merge, in review" to={d.security_findings ? reviewsLink(d, { category: "security" }) : undefined} />
            <Tile value={d.first_reviews ? fmtDur(d.median_first_review_ms) : "–"} label="Median time to first review" sub={d.first_reviews ? "from the webhook, " + fmtNum(d.first_reviews) + " merge requests" : undefined} />
            <Tile value={fmtNum(d.active_repos)} label="Active repositories" />
            <Tile value={fmtNum(d.comments)} label="Comments posted" />
            <Tile value={d.scored ? scoreKinds.map(([k]) => Math.round((scoreAvg as any)[k] || 0)).join(" / ") : "–"} label="Avg scores (Q / S / M)" sub={d.scored ? fmtNum(d.scored) + " scored reviews" : undefined} />
            <Tile value="–" label="Findings acted on" sub="not tracked yet" />
          </div>

          {!licensed ? (
            <Locked feature="metrics">Longer periods, trends, breakdowns by repository and category, and the CSV/JSON export are part of the licensed console. The numbers above cover the last 30 days.</Locked>
          ) : (
            <>
              <Section title="Reviews and findings" description={`Review runs and findings per ${d.granularity}.`}>
                <Legend items={[["reviews", "Review runs", "--st-done"]]} />
                <JobsChart points={shown} granularity={d.granularity} series={[["reviews", "Review runs", "--st-done"]]} label={`Review runs per ${d.granularity}`} />
                <Legend items={severities} />
                <JobsChart points={shown} granularity={d.granularity} series={severities as Series} label={`Findings per ${d.granularity}, by severity`} />
              </Section>
              {d.scored > 0 && (
                <Section title="Average scores" description="Dashed lines at 80 and 50 are the bands of the score labels.">
                  <ScoreLegend values={scoreAvg} />
                  <ScoreChart points={shown} granularity={d.granularity} />
                </Section>
              )}
              <div className="grid gap-4 lg:grid-cols-2">
                <Section title="Findings by severity">
                  <Bars rows={severities.map(([k, label, c]) => [label, d.by_severity[k] || 0, c, k === "other" ? undefined : reviewsLink(d, { severity: k })] as [string, number, string, string?])} />
                </Section>
                <Section title="Findings by category">
                  <Bars rows={d.by_category.map((c) => [c.category || "other", c.count, undefined, c.category && c.category !== "other" ? reviewsLink(d, { category: c.category }) : undefined] as [string, number, string?, string?])} />
                </Section>
                <Section title="Top repositories by reviews">
                  <RepoTable rows={d.top_by_reviews} unit="Review runs" link={(r) => reviewsLink(d, { host: r.host || "", repo: r.repo || "", state: "done" })} />
                </Section>
                <Section title="Top repositories by findings">
                  <RepoTable rows={d.top_by_findings} unit="Findings" link={(r) => reviewsLink(d, { host: r.host || "", repo: r.repo || "", severity: "any" })} />
                </Section>
              </div>
            </>
          )}
        </>
      )}
      <Note>
        Read from this console's own database; nothing leaves the installation. Findings are what reviews reported (questions aren't counted). Time to first review runs from the webhook to the first review's comments, including the wait after a push.
        Findings acted on (fixed or resolved versus dismissed) needs signals from the code host and isn't tracked yet. Periods are whole UTC days or months; reviews stored before this version are counted from their saved results.
      </Note>
    </section>
  );
}
