import { Fragment, useEffect, useState } from "react";
import { get, post, qs } from "../api";
import JobsChart, { Point, series } from "../Chart";
import { ago, fmtDur, fmtNum } from "../format";
import { Empty, errMsg, StateBadge, useToast } from "../ui";

type Job = {
  id: number; kind: string; platform: string; host: string; repo: string; number: number; url: string; trigger: string;
  state: string; started: string; finished: string; error: string; note: string; comments: number; files: number;
  tokens_in: number; tokens_out: number; model: string; requested_by: string; result?: any;
};

type Metrics = { granularity: string; from: string; to: string; points: Point[]; today_tokens: number; daily_token_budget: number };

// Fill empty buckets so the time axis is continuous.
function fillBuckets(d: Metrics): Point[] {
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

function Detail({ job, onRerun }: { job: Job; onRerun: (full: boolean) => void }) {
  const toast = useToast();
  const [d, setD] = useState<Job | null>(null);
  useEffect(() => {
    get<Job>("/admin/api/reviews/" + job.id).then(setD).catch((e) => toast(errMsg(e), true));
  }, [job.id]);
  if (!d) return <div className="muted">Loading…</div>;
  let r: any = null;
  try {
    r = d.result ? (typeof d.result === "string" ? JSON.parse(d.result) : d.result) : null;
  } catch {
    r = null;
  }
  const v = r?.verification;
  return (
    <div className="detail-box">
      {d.error && (
        <div className="finding">
          <div className="t">Error</div>
          <pre className="detail">{d.error}</pre>
        </div>
      )}
      {r ? (
        <>
          {r.summary && (
            <>
              <h3>Summary</h3>
              <p>{r.summary}</p>
            </>
          )}
          {r.scores && <p className="sub">Scores — quality {r.scores.quality} · security {r.scores.security} · maintainability {r.scores.maintainability}</p>}
          <h3>{(r.findings || []).length ? `Findings (${r.findings.length})` : "No findings"}</h3>
          {(r.findings || []).map((f: any, i: number) => (
            <div className="finding" key={i}>
              <div className="t">{(f.kind === "question" ? "❓ " : "") + f.title}</div>
              <div className="plat">
                {f.severity} · {f.category} · {f.path}:{f.end_line}
              </div>
            </div>
          ))}
          {v?.ran && (
            <p className="sub">
              Verification ({v.model}): {v.confirmed || 0} confirmed · {v.false_positives || 0} removed · {v.questions || 0} questions
            </p>
          )}
          {(v?.removed || []).map((x: any, i: number) => (
            <div className="finding" key={"r" + i}>
              <div className="plat">
                Removed: {x.title} — {x.reason}
              </div>
            </div>
          ))}
        </>
      ) : (
        !d.error && <p className="muted">{d.note || "No review result stored for this job."}</p>
      )}
      <p className="sub">
        Model {d.model || "—"} · {d.comments} comments · {d.files} files
      </p>
      {job.kind.includes("review") && (
        <div className="bar mt">
          <button className="btn" onClick={() => onRerun(false)}>
            Re-run (new changes)
          </button>
          <button className="btn" onClick={() => onRerun(true)}>
            Re-run full review
          </button>
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
  const [f, setF] = useState({ host: "", repo: "", state: "", kind: "", trigger: "", from: "", to: "" });
  const [repoQ, setRepoQ] = useState("");
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [more, setMore] = useState(false);
  const [open, setOpen] = useState<number | null>(null);
  const [url, setURL] = useState("");
  const [full, setFull] = useState(false);

  useEffect(() => {
    get<{ hosts: { host: string; name: string }[] }>("/admin/api/hosts").then((d) => setHosts(d.hosts || [])).catch(() => {});
  }, []);
  useEffect(() => {
    get<Metrics>("/admin/api/metrics" + qs({ granularity: gran, host: f.host })).then(setMetrics).catch((e) => toast(errMsg(e), true));
  }, [gran, f.host]);
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
  const tot = { jobs: 0, done: 0, skipped: 0, failed: 0, comments: 0, tokens_in: 0, tokens_out: 0, duration_ms: 0 } as Record<string, number>;
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
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });

  return (
    <section>
      <div className="bar">
        <select aria-label="Period" value={gran} onChange={(e) => setGran(e.target.value)}>
          <option value="hour">Last 48 hours</option>
          <option value="day">Last 30 days</option>
          <option value="month">Last 12 months</option>
        </select>
        <span className="spacer" />
        <input type="url" className="rn-url" placeholder="Paste a PR/MR URL to review it now" aria-label="Pull request URL" value={url} onChange={(e) => setURL(e.target.value)} />
        <label className="sub">
          <input type="checkbox" checked={full} onChange={(e) => setFull(e.target.checked)} /> full
        </label>
        <button className="btn primary" onClick={reviewNow}>
          Review now
        </button>
      </div>
      <div className="tiles">
        {tiles.map(([l, v]) => (
          <div className="tile" key={l}>
            <div className="n">{v}</div>
            <div className="l">{l}</div>
          </div>
        ))}
      </div>
      <div className="panel pad chartbox">
        <div className="legend">
          {series.map(([k, label, c]) => (
            <span key={k} ref={(el) => el?.style.setProperty("--sw", `var(${c})`)}>
              {label} {fmtNum(tot[k as string] || 0)}
            </span>
          ))}
        </div>
        {metrics && <JobsChart points={pts} granularity={metrics.granularity} />}
      </div>
      <div className="bar mt">
        <select aria-label="Host" value={f.host} onChange={set("host")}>
          <option value="">All hosts</option>
          {hosts.map((h) => (
            <option key={h.host} value={h.host}>
              {h.name}
            </option>
          ))}
        </select>
        <input type="search" placeholder="Repository" aria-label="Repository" value={repoQ} onChange={(e) => setRepoQ(e.target.value)} />
        <select aria-label="State" value={f.state} onChange={set("state")}>
          <option value="">Any state</option>
          <option value="done">Done</option>
          <option value="skipped">Skipped</option>
          <option value="failed">Failed</option>
        </select>
        <select aria-label="Kind" value={f.kind} onChange={set("kind")}>
          <option value="">Any kind</option>
          <option value="review">Review</option>
          <option value="full review">Full review</option>
          <option value="answer">Answer</option>
          <option value="help">Help</option>
          <option value="learn">Learn</option>
          <option value="remember">Remember</option>
        </select>
        <select aria-label="Trigger" value={f.trigger} onChange={set("trigger")}>
          <option value="">Any trigger</option>
          <option value="push">Push</option>
          <option value="command">Command</option>
          <option value="reply">Reply</option>
          <option value="ui">Admin panel</option>
        </select>
        <input type="date" aria-label="From" value={f.from} onChange={set("from")} />
        <input type="date" aria-label="To" value={f.to} onChange={set("to")} />
      </div>
      <div className="panel">
        {!jobs ? (
          <Empty>Loading…</Empty>
        ) : !jobs.length ? (
          <Empty>No reviews match.</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Finished</th>
                <th>Pull request</th>
                <th>Job</th>
                <th>State</th>
                <th>Comments / files</th>
                <th>Tokens in / out</th>
                <th>Took</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {jobs.map((j) => {
                const dur = j.started && !j.started.startsWith("0001-") ? new Date(j.finished).getTime() - new Date(j.started).getTime() : 0;
                return (
                  <Fragment key={j.id}>
                    <tr>
                      <td className="muted nowrap" title={new Date(j.finished).toLocaleString()}>
                        {ago(j.finished)}
                      </td>
                      <td>
                        <div>
                          {j.url ? (
                            <a href={j.url} target="_blank" rel="noopener noreferrer">
                              {j.repo} #{j.number}
                            </a>
                          ) : (
                            `${j.repo} #${j.number}`
                          )}
                        </div>
                        <div className="plat">
                          {j.platform} · {j.host}
                        </div>
                      </td>
                      <td>
                        <div>{j.kind}</div>
                        <div className="plat">{j.trigger + (j.requested_by ? " · " + j.requested_by : "")}</div>
                      </td>
                      <td>
                        <StateBadge kind={j.state === "done" ? "on" : j.state === "failed" ? "off" : "idle"}>{j.state}</StateBadge>
                        <div className="why">{j.error ? j.error.slice(0, 90) : j.note}</div>
                      </td>
                      <td className="nowrap">
                        {j.comments} / {j.files}
                      </td>
                      <td className="nowrap muted">
                        {fmtNum(j.tokens_in)} / {fmtNum(j.tokens_out)}
                      </td>
                      <td className="nowrap muted">{fmtDur(dur)}</td>
                      <td>
                        <button className="btn" onClick={() => setOpen(open === j.id ? null : j.id)}>
                          {open === j.id ? "Close" : "Details"}
                        </button>
                      </td>
                    </tr>
                    {open === j.id && (
                      <tr className="editor">
                        <td colSpan={8}>
                          <Detail job={j} onRerun={(isFull) => rerun(j, isFull)} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
      {more && (
        <div className="bar mt">
          <button className="btn" onClick={() => jobs && loadJobs(jobs[jobs.length - 1].id)}>
            Load older
          </button>
        </div>
      )}
      <p className="sub">Every finished review, answer, help and learning job. Charts read hourly, daily and monthly rollups that are updated as each job finishes. Reviews are kept for the retention set in Settings; hourly detail for 3 days, daily and monthly totals for good.</p>
    </section>
  );
}
