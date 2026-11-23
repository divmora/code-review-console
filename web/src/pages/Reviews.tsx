import { useEffect, useState } from "react";
import { ExternalLink, Play, Search } from "lucide-react";
import { get, post, qs } from "../api";
import JobsChart, { fillBuckets, Metrics, series } from "../Chart";
import { avg, ScoreChart, ScoreChips, ScoreKind, ScoreLegend, Scores, ScoreSums, scoreKinds } from "../scores";
import { ago, fmtDur, fmtNum, fmtDateTime, utcTitle } from "../format";
import { Empty, errMsg, Locked, NativeSelect, Note, Panel, SidePanel, StateBadge, Sub, Toolbar, useFeature, useMe, useToast } from "../ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Job = {
  id: number; kind: string; platform: string; host: string; repo: string; number: number; url: string; trigger: string;
  state: string; started: string; finished: string; error: string; note: string; comments: number; files: number;
  tokens_in: number; tokens_out: number; model: string; requested_by: string; result?: any; scores?: Scores | null;
};

const severityKind: Record<string, "off" | "warn" | "idle"> = { critical: "off", major: "warn", minor: "idle", nitpick: "idle" };

function Detail({ job, onRerun }: { job: Job; onRerun: (full: boolean) => void }) {
  const toast = useToast();
  const [d, setD] = useState<Job | null>(null);
  useEffect(() => {
    get<Job>("/admin/api/reviews/" + job.id).then(setD).catch((e) => toast(errMsg(e), true));
  }, [job.id]);
  if (!d) return <p className="text-sm text-muted-foreground">Loading…</p>;
  let r: any = null;
  try {
    r = d.result ? (typeof d.result === "string" ? JSON.parse(d.result) : d.result) : null;
  } catch {
    r = null;
  }
  const v = r?.verification;
  return (
    <div className="grid gap-5 text-sm">
      {d.error && (
        <div className="rounded-md border border-destructive/40 bg-danger-soft p-3">
          <div className="mb-1 font-medium text-destructive">Error</div>
          <pre className="font-mono text-xs whitespace-pre-wrap text-destructive">{d.error}</pre>
        </div>
      )}
      {r ? (
        <>
          {r.summary && (
            <div>
              <h3 className="mb-1 font-semibold">Summary</h3>
              <p className="leading-relaxed text-muted-foreground">{r.summary}</p>
            </div>
          )}
          {r.scores && (
            <div className="grid grid-cols-3 gap-2">
              {(["quality", "security", "maintainability"] as const).map((k) => (
                <div key={k} className="rounded-md border p-2.5">
                  <div className="text-lg font-semibold">{r.scores[k]}</div>
                  <div className="text-xs text-muted-foreground capitalize">{k}</div>
                </div>
              ))}
            </div>
          )}
          <div>
            <h3 className="mb-2 font-semibold">{(r.findings || []).length ? `Findings (${r.findings.length})` : "No findings"}</h3>
            <div className="grid gap-2">
              {(r.findings || []).map((f: any, i: number) => (
                <div className="finding rounded-md border p-3" key={i}>
                  <div className="font-medium">{(f.kind === "question" ? "❓ " : "") + f.title}</div>
                  <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                    <StateBadge kind={severityKind[f.severity] ?? "idle"}>{f.severity}</StateBadge>
                    <Badge variant="outline" className="font-normal">
                      {f.category}
                    </Badge>
                    <span className="font-mono">
                      {f.path}:{f.end_line}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          </div>
          {v?.ran && (
            <p className="text-muted-foreground">
              Verification ({v.model}): {v.confirmed || 0} confirmed · {v.false_positives || 0} removed · {v.questions || 0} questions
            </p>
          )}
          {(v?.removed || []).length > 0 && (
            <div className="grid gap-1.5">
              {(v.removed as any[]).map((x: any, i: number) => (
                <p key={"r" + i} className="text-xs text-muted-foreground">
                  Removed: <span className="text-foreground">{x.title}</span> — {x.reason}
                </p>
              ))}
            </div>
          )}
        </>
      ) : (
        !d.error && <p className="text-muted-foreground">{d.note || "No review result stored for this job."}</p>
      )}
      <Separator />
      <p className="text-xs text-muted-foreground">
        Model {d.model || "—"} · {d.comments} comments · {d.files} files
      </p>
      {job.kind.includes("review") && (
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => onRerun(false)}>
            Re-run (new changes)
          </Button>
          <Button variant="outline" onClick={() => onRerun(true)}>
            Re-run full review
          </Button>
        </div>
      )}
    </div>
  );
}

export default function Reviews() {
  const toast = useToast();
  const [gran, setGran] = useState("day");
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const [hosts, setHosts] = useState<{ host: string; name: string }[]>([]);
  const [f, setF] = useState({ host: "", repo: "", state: "", kind: "", trigger: "", from: "", to: "", score_kind: "", score_below: "" });
  const [sort, setSort] = useState<"" | ScoreKind | "lowest">("");
  const [repoQ, setRepoQ] = useState("");
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [more, setMore] = useState(false);
  const [open, setOpen] = useState<number | null>(null);
  const [url, setURL] = useState("");
  const [full, setFull] = useState(false);

  useEffect(() => {
    get<{ hosts: { host: string; name: string }[] }>("/admin/api/hosts").then((d) => setHosts(d.hosts || [])).catch(() => {});
  }, []);
  const hasMetrics = useFeature("metrics");
  const me = useMe();
  useEffect(() => {
    if (hasMetrics) get<Metrics>("/admin/api/metrics" + qs({ granularity: gran, host: f.host })).then(setMetrics).catch((e) => toast(errMsg(e), true));
  }, [gran, f.host, hasMetrics]);
  useEffect(() => {
    const t = window.setTimeout(() => setF((x) => ({ ...x, repo: repoQ.trim() })), 300);
    return () => window.clearTimeout(t);
  }, [repoQ]);
  const loadJobs = (before?: number) =>
    get<{ reviews: Job[] }>("/admin/api/reviews" + qs({ limit: 100, ...f, before }))
      .then((d) => {
        setJobs((js) => (before ? (js || []).concat(d.reviews) : d.reviews));
        setMore(d.reviews.length >= 100);
      })
      .catch((e) => toast(errMsg(e), true));
  useEffect(() => {
    loadJobs();
  }, [f]);

  const reviewNow = () => {
    if (!url.trim()) return toast("Paste a pull request or merge request URL", true);
    post<{ pr: string }>("/admin/api/reviews", { url: url.trim(), full })
      .then((r) => {
        setURL("");
        toast("Queued " + r.pr + " — it appears here when it finishes");
      })
      .catch((e) => toast(errMsg(e), true));
  };
  const rerun = (j: Job, isFull: boolean) =>
    post<{ pr: string }>(`/admin/api/reviews/${j.id}/rerun`, { full: isFull })
      .then((r) => toast("Queued " + r.pr))
      .catch((e) => toast(errMsg(e), true));

  const pts = metrics ? fillBuckets(metrics) : [];
  const tot = { jobs: 0, done: 0, skipped: 0, failed: 0, comments: 0, tokens_in: 0, tokens_out: 0, duration_ms: 0,
    scored: 0, quality_sum: 0, security_sum: 0, maintainability_sum: 0 } as Record<string, number>;
  pts.forEach((p) => Object.keys(tot).forEach((k) => (tot[k] += ((p as any)[k] as number) || 0)));
  const ran = tot.done + tot.failed;
  const tiles: [string, string][] = [
    ["Jobs", fmtNum(tot.jobs)],
    ["Reviewed", fmtNum(tot.done)],
    ["Skipped", fmtNum(tot.skipped)],
    ["Failed", fmtNum(tot.failed) + (ran ? " · " + Math.round((100 * tot.failed) / ran) + "%" : "")],
    ["Comments posted", fmtNum(tot.comments)],
    ["Tokens in / out", fmtNum(tot.tokens_in) + " / " + fmtNum(tot.tokens_out)],
    ["Avg time", fmtDur(ran ? tot.duration_ms / Math.max(1, tot.jobs) : 0)],
    ["Tokens today (UTC)", fmtNum(metrics?.today_tokens || 0) + (metrics?.daily_token_budget ? " of " + fmtNum(metrics.daily_token_budget) : "")],
  ];
  const periodAvg = Object.fromEntries(scoreKinds.map(([k]) => [k, avg({ bucket: "", ...tot } as ScoreSums, k)])) as Record<ScoreKind, number | null>;
  if (tot.scored)
    tiles.push(["Avg scores (Q / S / M)", scoreKinds.map(([k]) => Math.round(periodAvg[k] || 0)).join(" / ")], ["Scored reviews", fmtNum(tot.scored)]);
  const lowest = (j: Job) => (j.scores ? Math.min(j.scores.quality, j.scores.security, j.scores.maintainability) : 101);
  const shown = !jobs || !sort ? jobs : [...jobs].sort((a, b) => (sort === "lowest" ? lowest(a) - lowest(b) : (a.scores?.[sort] ?? 101) - (b.scores?.[sort] ?? 101)));
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  const openJob = jobs?.find((j) => j.id === open) ?? null;

  return (
    <section className="grid gap-4">
      <Toolbar>
        {hasMetrics && (
          <NativeSelect className="w-auto" aria-label="Period" value={gran} onChange={(e) => setGran(e.target.value)}>
            <option value="hour">Last 48 hours</option>
            <option value="day">Last 30 days</option>
            <option value="month">Last 12 months</option>
          </NativeSelect>
        )}
        <span className="flex-1" />
        <Input type="url" className="rn-url w-full sm:w-80" placeholder="Paste a PR/MR URL to review it now" aria-label="Pull request URL" value={url} onChange={(e) => setURL(e.target.value)} onKeyDown={(e) => e.key === "Enter" && reviewNow()} />
        <div className="flex items-center gap-2">
          <Checkbox id="rnFull" checked={full} onCheckedChange={(v) => setFull(v === true)} />
          <Label htmlFor="rnFull" className="text-sm font-normal text-muted-foreground">
            Full
          </Label>
        </div>
        <Button onClick={reviewNow}>
          <Play /> Review now
        </Button>
      </Toolbar>

      {!hasMetrics && (
        <Locked feature="metrics">
          Totals, charts and token use, and history kept longer than {me.free_retention_days} days, are part of the licensed console. Every review is listed below.
        </Locked>
      )}
      {hasMetrics && (
        <>
          <div className="tiles grid grid-cols-2 gap-3 md:grid-cols-4">
            {tiles.map(([l, v]) => (
              <Card key={l} className="tile gap-1 py-4">
                <CardContent className="px-4">
                  <div className="text-xl font-semibold tabular-nums">{v}</div>
                  <div className="text-xs text-muted-foreground">{l}</div>
                </CardContent>
              </Card>
            ))}
          </div>
          <Card className="py-4">
            <CardContent className="grid gap-3 px-4">
              <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
                {series.map(([k, label, c]) => (
                  <span key={k} className="flex items-center gap-1.5">
                    <span className="size-2.5 rounded-sm" ref={(el) => el?.style.setProperty("background", `var(${c})`)} />
                    {label} {fmtNum(tot[k as string] || 0)}
                  </span>
                ))}
              </div>
              {metrics && <JobsChart points={pts} granularity={metrics.granularity} />}
            </CardContent>
          </Card>
          {tot.scored > 0 && metrics && (
            <Card className="py-4">
              <CardContent className="grid gap-3 px-4">
                <ScoreLegend values={periodAvg} />
                <ScoreChart points={pts as ScoreSums[]} granularity={metrics.granularity} />
                <Sub>Average scores of the reviews in each {metrics.granularity}. Dashed lines at 80 and 50 are the bands of the score labels.</Sub>
              </CardContent>
            </Card>
          )}
        </>
      )}

      <Toolbar>
        <NativeSelect className="w-auto" aria-label="Host" value={f.host} onChange={set("host")}>
          <option value="">All hosts</option>
          {hosts.map((h) => (
            <option key={h.host} value={h.host}>
              {h.name}
            </option>
          ))}
        </NativeSelect>
        <div className="relative w-full sm:w-52">
          <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
          <Input type="search" className="pl-8" placeholder="Repository" aria-label="Repository" value={repoQ} onChange={(e) => setRepoQ(e.target.value)} />
        </div>
        <NativeSelect className="w-auto" aria-label="State" value={f.state} onChange={set("state")}>
          <option value="">Any state</option>
          <option value="done">Done</option>
          <option value="skipped">Skipped</option>
          <option value="failed">Failed</option>
        </NativeSelect>
        <NativeSelect className="w-auto" aria-label="Kind" value={f.kind} onChange={set("kind")}>
          <option value="">Any kind</option>
          <option value="review">Review</option>
          <option value="full review">Full review</option>
          <option value="answer">Answer</option>
          <option value="help">Help</option>
          <option value="learn">Learn</option>
          <option value="remember">Remember</option>
        </NativeSelect>
        <NativeSelect className="w-auto" aria-label="Trigger" value={f.trigger} onChange={set("trigger")}>
          <option value="">Any trigger</option>
          <option value="push">Push</option>
          <option value="command">Command</option>
          <option value="reply">Reply</option>
          <option value="ui">Admin panel</option>
        </NativeSelect>
        <Input type="date" className="w-auto" aria-label="From" value={f.from} onChange={set("from")} />
        <Input type="date" className="w-auto" aria-label="To" value={f.to} onChange={set("to")} />
        <NativeSelect className="w-auto" aria-label="Score" value={f.score_kind} onChange={set("score_kind")}>
          <option value="">Any score</option>
          <option value="any">Lowest score</option>
          {scoreKinds.map(([k, label]) => (
            <option key={k} value={k}>
              {label}
            </option>
          ))}
        </NativeSelect>
        {f.score_kind && (
          <NativeSelect className="w-auto" aria-label="Score below" value={f.score_below} onChange={set("score_below")}>
            <option value="">at any value</option>
            <option value="50">below 50</option>
            <option value="60">below 60</option>
            <option value="70">below 70</option>
            <option value="80">below 80</option>
            <option value="90">below 90</option>
          </NativeSelect>
        )}
        <NativeSelect className="w-auto" aria-label="Sort" value={sort} onChange={(e) => setSort(e.target.value as typeof sort)}>
          <option value="">Newest first</option>
          <option value="lowest">Lowest score first</option>
          {scoreKinds.map(([k, label]) => (
            <option key={k} value={k}>
              Lowest {label.toLowerCase()} first
            </option>
          ))}
        </NativeSelect>
      </Toolbar>

      <Panel>
        {!jobs ? (
          <Empty>Loading…</Empty>
        ) : !shown || !shown.length ? (
          <Empty>No reviews match.</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">Finished</TableHead>
                <TableHead>Pull request</TableHead>
                <TableHead>Job</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Scores</TableHead>
                <TableHead className="text-right">Comments / files</TableHead>
                <TableHead className="text-right">Tokens in / out</TableHead>
                <TableHead className="text-right">Took</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {shown.map((j) => {
                const dur = j.started && !j.started.startsWith("0001-") ? new Date(j.finished).getTime() - new Date(j.started).getTime() : 0;
                return (
                  <TableRow key={j.id} className="cursor-pointer" onClick={() => setOpen(j.id)}>
                    <TableCell className="pl-4 text-muted-foreground" title={fmtDateTime(j.finished, true) + " · " + utcTitle(j.finished)}>
                      {ago(j.finished)}
                    </TableCell>
                    <TableCell>
                      <div className="font-medium">
                        {j.url ? (
                          <a className="inline-flex items-center gap-1 hover:underline" href={j.url} target="_blank" rel="noopener noreferrer" onClick={(e) => e.stopPropagation()}>
                            {j.repo} #{j.number} <ExternalLink className="size-3 text-muted-foreground" />
                          </a>
                        ) : (
                          `${j.repo} #${j.number}`
                        )}
                      </div>
                      <Sub>
                        {j.platform} · {j.host}
                      </Sub>
                    </TableCell>
                    <TableCell>
                      <div>{j.kind}</div>
                      <Sub>{j.trigger + (j.requested_by ? " · " + j.requested_by : "")}</Sub>
                    </TableCell>
                    <TableCell className="max-w-64 whitespace-normal">
                      <StateBadge kind={j.state === "done" ? "on" : j.state === "failed" ? "off" : "idle"}>{j.state}</StateBadge>
                      {(j.error || j.note) && <Sub className="line-clamp-2">{j.error ? j.error.slice(0, 120) : j.note}</Sub>}
                    </TableCell>
                    <TableCell>
                      <ScoreChips s={j.scores} />
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {j.comments} / {j.files}
                    </TableCell>
                    <TableCell className="text-right text-muted-foreground tabular-nums">
                      {fmtNum(j.tokens_in)} / {fmtNum(j.tokens_out)}
                    </TableCell>
                    <TableCell className="text-right text-muted-foreground tabular-nums">{fmtDur(dur)}</TableCell>
                    <TableCell className="pr-4 text-right">
                      <Button variant="ghost" size="sm" onClick={(e) => (e.stopPropagation(), setOpen(j.id))}>
                        Details
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </Panel>
      {more && (
        <div>
          <Button variant="outline" onClick={() => jobs && loadJobs(jobs[jobs.length - 1].id)}>
            Load older
          </Button>
        </div>
      )}
      <Note>
        Every finished review, answer, help and learning job. Charts read hourly, daily and monthly rollups that are updated as each job finishes. Reviews are kept for the retention set in Settings; hourly detail for 3 days, daily and monthly totals for good.
        Scores (quality, security, maintainability, 1 to 100) come from the review; sorting by score orders the reviews loaded so far, and the score filter searches all of them.
      </Note>

      <SidePanel
        wide
        open={!!openJob}
        onClose={() => setOpen(null)}
        title={openJob ? `${openJob.repo} #${openJob.number}` : ""}
        description={openJob ? `${openJob.kind} · ${openJob.trigger} · ${openJob.state} · ${ago(openJob.finished)}` : undefined}
      >
        {openJob && <Detail key={openJob.id} job={openJob} onRerun={(isFull) => rerun(openJob, isFull)} />}
      </SidePanel>
    </section>
  );
}
