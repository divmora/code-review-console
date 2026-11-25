import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Ellipsis, FolderPlus, KeyRound, LayoutDashboard, Plus, Search, Settings2 } from "lucide-react";
import type { Dashboard } from "./Dashboards";
import { del, get, post, put } from "../api";
import { ago } from "../format";
import type { Access, Overrides, Repo } from "../types";
import { Chips, Empty, errMsg, Field, FormGrid, NativeSelect, Note, Panel, SettingsEditor, settingsChips, SidePanel, StateBadge, Sub, Toolbar, useConfirm, useFeature, useMe, useToast } from "../ui";
import { avg, ScoreChart, ScoreChips, ScoreLegend, Scores, ScoreSums, scoreKinds, Sparkline } from "../scores";
import { fillBuckets, Metrics } from "../Chart";

type RepoScore = { platform: string; host: string; repo: string; number: number; at: string; latest: Scores; trend: ScoreSums[] };

// dailyTrend fills the days without a scored review, so the sparkline's
// x axis is time.
function dailyTrend(since: string, days: number, trend: ScoreSums[]): ScoreSums[] {
  const by: Record<string, ScoreSums> = {};
  trend.forEach((p) => (by[new Date(p.bucket).toISOString().slice(0, 10)] = p));
  const out: ScoreSums[] = [];
  const start = new Date(since).getTime();
  for (let i = 0; i < days; i++) {
    const d = new Date(start + i * 86400e3).toISOString();
    out.push(by[d.slice(0, 10)] || { bucket: d });
  }
  return out;
}
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { stickyEnd, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";
import { Checkbox } from "@/components/ui/checkbox";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

type Mode = Repo["mode"];

// groupOf is the group or owner a repository is in ("acme/platform" for
// "acme/platform/api"); "" for a repository at the top level.
function groupOf(path: string): string {
  const i = path.lastIndexOf("/");
  return i > 0 ? path.slice(0, i) : "";
}

// ruleLink opens Add rule on the Rules page, prefilled for a group.
const ruleLink = (r: { platform: string; host: string }, group: string) =>
  "/rules?" + new URLSearchParams({ new: "1", platform: r.platform, host: r.host, pattern: group }).toString();

// flagsOf lists the effective switches that change what a review does.
const flagsOf = (e: Access) =>
  [e.require_reviewer && "reviewer required", e.skip_branch_sync && "skips branch syncs", e.score_labels && "score labels", e.commit_status && "commit status", e.approve && "approves when it passes"].filter(Boolean) as string[];

// StateBadge for a repository: reviewed or not, with why (and its
// switches) on hover or focus instead of under every row (issue #134).
function ReviewState({ e }: { e: Access }) {
  const flags = flagsOf(e);
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button type="button" className="rounded-full focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none" aria-label={(e.enabled ? "Reviewed: " : "Not reviewed: ") + whyText(e)}>
          <StateBadge kind={e.enabled ? "on" : "off"}>{e.enabled ? "Reviewed" : "Not reviewed"}</StateBadge>
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-64">
        <div>{whyText(e)}</div>
        {flags.length > 0 && <div className="opacity-80">{flags.join(" · ")}</div>}
      </TooltipContent>
    </Tooltip>
  );
}

function whyText(e: Access): string {
  if (e.source === "repository") return "set on this repository";
  if (e.source === "allowlist") return "no rule matches · allowlist default";
  if (e.source === "default") return "no rule matches · reviewed by default";
  return "by " + e.source;
}

// RepoScoreTrend charts one repository's average scores over 30 days, 12
// months or all of its history (the daily and monthly rollups).
function RepoScoreTrend({ repo }: { repo: Repo }) {
  const [period, setPeriod] = useState("30d");
  const [m, setM] = useState<Metrics | null>(null);
  useEffect(() => {
    const q = new URLSearchParams({ platform: repo.platform, host: repo.host, repo: repo.path, granularity: period === "30d" ? "day" : "month" });
    if (period === "all") q.set("from", "2000-01-01");
    get<Metrics>("/admin/api/metrics?" + q.toString()).then(setM).catch(() => setM(null));
  }, [repo.id, period]);
  let pts = m ? (fillBuckets(m) as ScoreSums[]) : [];
  if (period === "all") pts = pts.slice(Math.max(0, pts.findIndex((p) => p.scored)));
  const tot = pts.reduce((a, p) => ({ bucket: "", scored: (a.scored || 0) + (p.scored || 0), quality_sum: (a.quality_sum || 0) + (p.quality_sum || 0),
    security_sum: (a.security_sum || 0) + (p.security_sum || 0), maintainability_sum: (a.maintainability_sum || 0) + (p.maintainability_sum || 0) }), { bucket: "" } as ScoreSums);
  return (
    <div className="mb-6 grid gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">Review scores</h3>
        <span className="flex-1" />
        <NativeSelect className="w-auto" aria-label="Period" value={period} onChange={(e) => setPeriod(e.target.value)}>
          <option value="30d">Last 30 days</option>
          <option value="12m">Last 12 months</option>
          <option value="all">All history</option>
        </NativeSelect>
      </div>
      {!m ? (
        <Sub>Loading…</Sub>
      ) : !tot.scored ? (
        <Sub>No scored reviews in this period.</Sub>
      ) : (
        <>
          <ScoreLegend values={Object.fromEntries(scoreKinds.map(([k]) => [k, avg(tot, k)]))} />
          <ScoreChart points={pts} granularity={m.granularity} />
          <Sub>{tot.scored} scored reviews; the numbers above are the period's averages.</Sub>
        </>
      )}
    </div>
  );
}

export default function Repositories() {
  const me = useMe();
  const toast = useToast();
  const confirm = useConfirm();
  const [repos, setRepos] = useState<Repo[] | null>(null);
  const [q, setQ] = useState("");
  const [show, setShow] = useState("all");
  // group: "host|group" to show one group's repositories ("" = all).
  const [group, setGroup] = useState("");
  // selected repository IDs, for the bulk actions.
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  // Dashboards that repositories can be added to (admins; issue #155).
  const [boards, setBoards] = useState<Dashboard[]>([]);
  useEffect(() => {
    if (me.role && me.role !== "admin") return;
    get<{ dashboards: Dashboard[] }>("/admin/api/dashboards")
      .then((r) => setBoards((r.dashboards || []).filter((d) => d.id && !d.all_repos)))
      .catch(() => {});
  }, []);
  // addTo attaches the selected repositories to a dashboard.
  const addTo = async (d: Dashboard) => {
    const ids = new Set([...d.repos.map((r) => r.id), ...selected]);
    const added = ids.size - d.repos.length;
    if (!added) {
      toast("They are on " + d.title + " already");
      return;
    }
    if (!(await confirm({ title: `Add ${added} ${added === 1 ? "repository" : "repositories"} to ${d.title}?`, description: `Everyone who can open ${d.title} sees ${added === 1 ? "its" : "their"} reviews and scores${d.hide_repos ? " (names hidden)" : ""}.`, action: "Add" }))) return;
    setBulkBusy(true);
    put<Dashboard>("/admin/api/dashboards/" + d.id, { slug: d.slug, title: d.title, description: d.description, access: d.access, hide_repos: d.hide_repos, enabled: d.enabled,
      all_repos: false, repos: Array.from(ids), patterns: d.patterns })
      .then((saved) => {
        setBoards((bs) => bs.map((b) => (b.id === saved.id ? saved : b)));
        toast(`Added ${added} to ${d.title}`);
        setSelected(new Set());
        load();
      })
      .catch((e) => toast(errMsg(e), true))
      .finally(() => setBulkBusy(false));
  };
  const canChange = me.role !== "viewer";
  // A bulk action only touches repositories in view: a new filter clears the selection.
  useEffect(() => setSelected(new Set()), [q, show, group]);
  const rulesOK = useFeature("rules");
  const [editing, setEditing] = useState<number | null>(null);
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ platform: "github", host: "", path: "", mode: "enabled" });

  const load = () => get<Repo[]>("/admin/api/repos").then(setRepos).catch((e) => toast(errMsg(e), true));
  useEffect(() => {
    load();
  }, []);
  const hasMetrics = useFeature("metrics");
  const [scores, setScores] = useState<{ since: string; days: number; by: Record<string, RepoScore> } | null>(null);
  useEffect(() => {
    if (!hasMetrics) return;
    get<{ since: string; days: number; repos: RepoScore[] }>("/admin/api/repos/scores?days=30")
      .then((d) => setScores({ since: d.since, days: d.days, by: Object.fromEntries(d.repos.map((r) => [r.platform + "|" + r.host + "|" + r.repo, r])) }))
      .catch(() => {});
  }, [hasMetrics]);
  const scoreOf = (r: Repo) => scores?.by[r.platform + "|" + r.host.toLowerCase() + "|" + r.path.toLowerCase()];

  const save = (r: Repo, mode: string, settings: Overrides) =>
    put<Repo>("/admin/api/repos/" + r.id, { mode, settings }).then((x) => {
      setRepos((rs) => (rs || []).map((y) => (y.id === x.id ? x : y)));
      toast(x.path + ": reviews " + (x.effective.enabled ? "on" : "off") + " (" + x.effective.source + ")");
      return x;
    });
  // bulk sets the reviews mode of the selected repositories, keeping each
  // one's own settings; each change is audited like a single one.
  const bulk = async (mode: Mode) => {
    const rs = (repos || []).filter((r) => selected.has(r.id) && r.mode !== mode);
    if (!rs.length) {
      toast("Nothing to change: they are all set to " + mode);
      return;
    }
    if (rs.length > 1 && !(await confirm({ title: `Set ${rs.length} repositories to ${mode === "inherit" ? "Inherit" : mode === "enabled" ? "Enabled" : "Disabled"}?`, description: mode === "inherit" ? "They follow the rules and the default again." : "This overrides every rule for them. A rule for their group is often simpler: it covers new repositories too.", action: "Change " + rs.length }))) return;
    setBulkBusy(true);
    const results = await Promise.allSettled(rs.map((r) => put<Repo>("/admin/api/repos/" + r.id, { mode, settings: r.settings || {} })));
    const ok = results.flatMap((x) => (x.status === "fulfilled" ? [x.value] : []));
    const failed = results.filter((x) => x.status === "rejected") as PromiseRejectedResult[];
    setRepos((cur) => (cur || []).map((y) => ok.find((x) => x.id === y.id) || y));
    setBulkBusy(false);
    if (failed.length) toast(`Changed ${ok.length} of ${rs.length}: ${errMsg(failed[0].reason)}`, true);
    else {
      toast(`Set ${ok.length} ${ok.length === 1 ? "repository" : "repositories"} to ${mode}`);
      setSelected(new Set());
    }
  };
  const add = () =>
    post<Repo>("/admin/api/repos", { ...form, host: form.host.trim(), path: form.path.trim() })
      .then((r) => {
        setRepos((rs) => (rs || []).filter((x) => x.id !== r.id).concat([r]));
        setForm({ ...form, path: "" });
        setAdding(false);
        toast("Added " + r.path);
      })
      .catch((e) => toast(errMsg(e), true));
  const remove = async (r: Repo) => {
    if (!(await confirm({ title: "Remove " + r.path + "?", description: "It reappears, set to Inherit, if it sends another webhook.", action: "Remove", danger: true }))) return;
    del("/admin/api/repos/" + r.id)
      .then(() => {
        setRepos((rs) => (rs || []).filter((x) => x.id !== r.id));
        setEditing(null);
        toast("Removed " + r.path);
      })
      .catch((e) => toast(errMsg(e), true));
  };

  const keyOf = (r: Repo) => r.host + "|" + groupOf(r.path);
  const list = (repos || []).filter((r) => {
    if (show === "on" && !r.effective.enabled) return false;
    if (show === "off" && r.effective.enabled) return false;
    if (show === "set" && r.mode === "inherit") return false;
    if (group && keyOf(r) !== group) return false;
    return !q || (r.host + "/" + r.path).toLowerCase().includes(q.toLowerCase());
  });
  // Groups with repositories, for the filter.
  const groups = Array.from(new Set((repos || []).filter((r) => groupOf(r.path)).map(keyOf))).sort();
  // Groups where several repositories are enabled one by one: a rule for
  // the group is simpler and covers new repositories too.
  const byHand = Object.entries(
    (repos || []).reduce<Record<string, Repo[]>>((acc, r) => {
      if (r.mode === "enabled" && groupOf(r.path)) (acc[keyOf(r)] ||= []).push(r);
      return acc;
    }, {}),
  ).filter(([, rs]) => rs.length >= 3);
  const allShown = list.length > 0 && list.every((r) => selected.has(r.id));
  const toggle = (id: number, on: boolean) =>
    setSelected((cur) => {
      const next = new Set(cur);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  const on = (repos || []).filter((r) => r.effective.enabled).length;
  const edited = repos?.find((r) => r.id === editing) ?? null;

  return (
    <section className="grid gap-4">
      <Toolbar>
        <div className="relative w-full sm:w-64">
          <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
          <Input type="search" className="pl-8" placeholder="Filter repositories" aria-label="Filter repositories" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <NativeSelect className="w-auto" aria-label="Show" value={show} onChange={(e) => setShow(e.target.value)}>
          <option value="all">All</option>
          <option value="on">Reviewed</option>
          <option value="off">Not reviewed</option>
          <option value="set">Set on the repository</option>
        </NativeSelect>
        {groups.length > 1 && (
          <NativeSelect className="w-auto max-w-64" aria-label="Group" value={group} onChange={(e) => setGroup(e.target.value)}>
            <option value="">All groups</option>
            {groups.map((g) => (
              <option key={g} value={g}>
                {g.split("|")[1]} ({g.split("|")[0]})
              </option>
            ))}
          </NativeSelect>
        )}
        <span className="text-sm text-muted-foreground">{repos && on + " reviewed · " + (repos.length - on) + " not reviewed"}</span>
        <span className="flex-1" />
        <Button variant="outline" onClick={() => setAdding(true)}>
          <Plus /> Add repository
        </Button>
      </Toolbar>

      <Note>
        <b>Inherit</b> follows the most specific group or pattern rule, and otherwise the default ({me.allowlist ? "not reviewed: the allowlist is on" : "reviewed: the allowlist is off"}).{" "}
        <b>Enabled</b> or <b>Disabled</b> here overrides every rule. Hover a repository's state to see why.
      </Note>
      {canChange && rulesOK &&
        byHand.map(([k, rs]) => (
          <Note key={k}>
            {rs.length} repositories in <code>{k.split("|")[1]}</code> are enabled one by one. A rule for the group covers them and new ones as they appear; then select them here and choose <b>Inherit</b>.{" "}
            <Link className="font-medium text-primary underline-offset-2 hover:underline" to={ruleLink(rs[0], k.split("|")[1])}>
              Create a rule for {k.split("|")[1]}
            </Link>
          </Note>
        ))}
      {canChange && selected.size > 0 && (
        <div role="region" aria-label="Selected repositories" className="sticky top-2 z-10 flex flex-wrap items-center gap-2 rounded-lg border bg-card p-2 pl-4 shadow-sm">
          <span className="text-sm font-medium">{selected.size} selected</span>
          <span className="flex-1" />
          <Button size="sm" variant="outline" disabled={bulkBusy} onClick={() => bulk("enabled")}>
            Enable
          </Button>
          <Button size="sm" variant="outline" disabled={bulkBusy} onClick={() => bulk("disabled")}>
            Disable
          </Button>
          <Button size="sm" variant="outline" disabled={bulkBusy} onClick={() => bulk("inherit")}>
            Inherit
          </Button>
          {boards.length > 0 && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button size="sm" variant="outline" disabled={bulkBusy}>
                  <LayoutDashboard /> Add to dashboard…
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {boards.map((b) => (
                  <DropdownMenuItem key={b.id} onSelect={() => addTo(b)}>
                    {b.title}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>
            Clear
          </Button>
        </div>
      )}

      <Panel>
        {!repos ? (
          <Empty>Loading…</Empty>
        ) : !list.length ? (
          <Empty>{repos.length ? "No repositories match." : "No repositories yet. They appear when they send their first webhook, or add one above."}</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                {canChange && (
                  <TableHead className="w-10 pl-4">
                    <Checkbox aria-label="Select all shown" checked={allShown} onCheckedChange={(v) => setSelected(v === true ? new Set([...selected, ...list.map((r) => r.id)]) : new Set([...selected].filter((id) => !list.some((r) => r.id === id))))} />
                  </TableHead>
                )}
                <TableHead className={canChange ? "" : "pl-4"}>Reviews</TableHead>
                <TableHead>Repository</TableHead>
                <TableHead className="hidden xl:table-cell">Last webhook</TableHead>
                {hasMetrics && <TableHead>Scores · 30 days</TableHead>}
                <TableHead className="hidden lg:table-cell">Effective settings</TableHead>
                <TableHead className="hidden 2xl:table-cell">Last changed</TableHead>
                <TableHead className={stickyEnd}>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((r) => {
                const e = r.effective;
                const g = groupOf(r.path);
                return (
                  <TableRow key={r.id} data-state={selected.has(r.id) ? "selected" : undefined}>
                    {canChange && (
                      <TableCell className="pl-4 align-top">
                        <Checkbox className="mt-2" aria-label={"Select " + r.path} checked={selected.has(r.id)} onCheckedChange={(v) => toggle(r.id, v === true)} />
                      </TableCell>
                    )}
                    <TableCell className={cn("align-top whitespace-nowrap", !canChange && "pl-4")}>
                      <div className="flex items-center gap-2">
                        <NativeSelect className="h-8 w-28" aria-label={"Reviews for " + r.path} value={r.mode} disabled={!canChange} onChange={(ev) => save(r, ev.target.value, r.settings || {}).catch((x) => toast(errMsg(x), true))}>
                          <option value="inherit">Inherit</option>
                          <option value="enabled">Enabled</option>
                          <option value="disabled">Disabled</option>
                        </NativeSelect>
                        <ReviewState e={e} />
                      </div>
                    </TableCell>
                    <TableCell className="align-top">
                      <div className="font-medium">{r.path}</div>
                      <Sub>
                        {r.platform} · {r.host}
                        <span className="xl:hidden"> · last webhook {ago(r.last_seen)}</span>
                      </Sub>
                      {r.dashboards && r.dashboards.length > 0 && (
                        <Sub className="max-w-64 truncate" title={"On the dashboards " + r.dashboards.map((d) => d.title).join(", ")}>
                          <LayoutDashboard className="inline size-3 align-[-1px]" /> {r.dashboards.map((d) => d.title).join(", ")}
                        </Sub>
                      )}
                      {r.credential && (
                        <Sub className="max-w-64 truncate" title={"Uses the group token for " + r.credential.pattern + " (Code hosts), not the host's own"}>
                          <KeyRound className="inline size-3 align-[-1px]" /> {r.credential.name}
                        </Sub>
                      )}
                      {/* Below lg the settings column is hidden; its chips move here. */}
                      {settingsChips(e.settings).length > 0 && (
                        <div className="mt-1.5 lg:hidden">
                          <Chips items={settingsChips(e.settings)} />
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="hidden align-top text-muted-foreground xl:table-cell">{ago(r.last_seen)}</TableCell>
                    {hasMetrics && (
                      <TableCell className="align-top">
                        {(() => {
                          const sc = scoreOf(r);
                          if (!sc) return <Sub>No scored review yet</Sub>;
                          return (
                            <div className="grid gap-1" title={"Latest scored review: #" + sc.number + ", " + ago(sc.at)}>
                              <ScoreChips s={sc.latest} />
                              <Sparkline points={dailyTrend(scores!.since, scores!.days, sc.trend)} label={"Daily average scores of " + r.path + ", last 30 days"} />
                            </div>
                          );
                        })()}
                      </TableCell>
                    )}
                    <TableCell className="hidden align-top whitespace-normal lg:table-cell">
                      <Chips items={settingsChips(e.settings)} />
                      {(e.rules || []).length > 0 && JSON.stringify(e.settings) !== JSON.stringify(r.settings || {}) && <Sub>includes rule settings</Sub>}
                    </TableCell>
                    <TableCell className="hidden align-top text-muted-foreground 2xl:table-cell">{r.updated_by ? r.updated_by + " · " + ago(r.updated_at) : "—"}</TableCell>
                    <TableCell className={cn("pr-4 text-right align-top whitespace-nowrap", stickyEnd)}>
                      <Button variant="ghost" size="sm" onClick={() => setEditing(r.id)}>
                        <Settings2 /> Settings
                      </Button>
                      {g && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="size-8" aria-label={"More for " + r.path}>
                              <Ellipsis />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem onSelect={() => setGroup(keyOf(r))}>Show only {g}</DropdownMenuItem>
                            {canChange && rulesOK && (
                              <DropdownMenuItem asChild>
                                <Link to={ruleLink(r, g)}>
                                  <FolderPlus /> Create a rule for {g}
                                </Link>
                              </DropdownMenuItem>
                            )}
                          </DropdownMenuContent>
                        </DropdownMenu>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </Panel>

      <Note>
        Repositories appear here automatically when they send their first webhook, set to <b>Inherit</b>. To onboard a group, add a rule for it under <b>Rules</b> rather than enabling each repository: the rule covers new repositories too. Settings here override the rules' settings and the repository's own <code>.codereview.yaml</code>.
      </Note>
      {me.require_reviewer && (
        <Note>
          <b>Reviewer required:</b> even in an enabled repository, a merge request is reviewed only when the bot user is one of its reviewers. Adding the bot as a reviewer starts a review; <code>@bot review</code> comments still work.
        </Note>
      )}

      <SidePanel open={!!edited} onClose={() => setEditing(null)} title={edited?.path ?? ""} description={edited ? `${edited.platform} · ${edited.host} · review settings for this repository` : undefined}>
        {edited && hasMetrics && <RepoScoreTrend key={edited.id} repo={edited} />}
        {edited && (
          <SettingsEditor
            key={edited.id}
            value={edited.settings}
            labelRepo={{ platform: edited.platform, host: edited.host, path: edited.path }}
            onSave={(s) => save(edited, edited.mode, s).then(() => setEditing(null)).catch((x) => toast(errMsg(x), true))}
            onCancel={() => setEditing(null)}
            danger={
              <Button variant="ghost" className="text-destructive hover:text-destructive" onClick={() => remove(edited)}>
                Remove
              </Button>
            }
          />
        )}
      </SidePanel>

      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Add repository</DialogTitle>
            <DialogDescription>Repositories also appear by themselves when they send their first webhook.</DialogDescription>
          </DialogHeader>
          <FormGrid className="lg:grid-cols-2">
            <Field label="Platform">
              <NativeSelect value={form.platform} onChange={(e) => setForm({ ...form, platform: e.target.value })}>
                <option value="github">GitHub</option>
                <option value="gitlab">GitLab</option>
              </NativeSelect>
            </Field>
            <Field label="Host">
              <Input placeholder="github.com" value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} />
            </Field>
            <Field label="Repository path" className="sm:col-span-2">
              <Input placeholder="owner/repo or group/sub/project" value={form.path} onChange={(e) => setForm({ ...form, path: e.target.value })} />
            </Field>
            <Field label="Reviews">
              <NativeSelect value={form.mode} onChange={(e) => setForm({ ...form, mode: e.target.value })}>
                <option value="enabled">Enabled</option>
                <option value="disabled">Disabled</option>
                <option value="inherit">Inherit from rules</option>
              </NativeSelect>
            </Field>
          </FormGrid>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAdding(false)}>
              Cancel
            </Button>
            <Button onClick={add}>Add</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
