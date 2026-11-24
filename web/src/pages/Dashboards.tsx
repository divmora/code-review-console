import { useEffect, useMemo, useState } from "react";
import { Copy, ExternalLink, KeyRound, Plus, Search, Trash2 } from "lucide-react";
import { del, get, post, put } from "../api";
import { fmtDate } from "../format";
import type { Repo } from "../types";
import { Empty, errMsg, Field, FormGrid, LockNote, NativeSelect, Note, Panel, SidePanel, StateBadge, Sub, Toolbar, useConfirm, useFeature, useMe, useToast } from "../ui";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { stickyEnd, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

// Status dashboards (issue #155): each has its own title, access, token and
// repositories, so a team sees only its own.

type Access = "admins" | "signed_in" | "token" | "public";
type Pattern = { platform: string; host: string; pattern: string };
export type Dashboard = {
  id: number; slug: string; title: string; description: string; access: Access; hide_repos: boolean; all_repos: boolean; enabled: boolean;
  token_set: boolean; token_expires?: string; token_expired?: boolean;
  repos: { id: number; platform: string; host: string; path: string }[]; patterns: Pattern[];
  repo_count: number; url: string; default: boolean; updated_at?: string; updated_by?: string;
};
type Openable = { slug: string; title: string; description: string; url: string };

export const accessLabels: Record<Access, string> = {
  admins: "Signed-in admins",
  signed_in: "Anyone signed in",
  token: "Admins, and anyone with its token",
  public: "Anyone who can reach the console",
};

const slugify = (s: string) =>
  s.toLowerCase().normalize("NFKD").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 64);

function TokenState({ d }: { d: Dashboard }) {
  if (d.access !== "token") return null;
  if (!d.token_set) return <StateBadge kind="warn">No token yet</StateBadge>;
  if (d.token_expired) return <StateBadge kind="warn">Expired</StateBadge>;
  if (!d.token_expires) return <Sub>Never expires</Sub>;
  return <Sub>Expires {fmtDate(d.token_expires)}</Sub>;
}

function useCopy() {
  const toast = useToast();
  return (text: string, what = "Link") =>
    navigator.clipboard
      .writeText(text)
      .then(() => toast(what + " copied"))
      .catch(() => toast("Copy failed: select the text and copy it", true));
}

type Draft = {
  id?: number; slug: string; title: string; description: string; access: Access; hide_repos: boolean; enabled: boolean; all_repos: boolean;
  repos: Set<number>; patterns: Pattern[]; slugTouched: boolean; isDefault: boolean;
};

const toDraft = (d?: Dashboard): Draft =>
  d
    ? { id: d.id, slug: d.slug, title: d.title, description: d.description, access: d.access, hide_repos: d.hide_repos, enabled: d.enabled, all_repos: d.all_repos,
        repos: new Set(d.repos.map((r) => r.id)), patterns: d.patterns.map((p) => ({ ...p })), slugTouched: true, isDefault: d.default }
    : { slug: "", title: "", description: "", access: "signed_in", hide_repos: false, enabled: true, all_repos: false, repos: new Set(), patterns: [], slugTouched: false, isDefault: false };

// linkOf is where a dashboard being edited opens.
const linkOf = (v: Draft) => {
  const signIn = v.access === "admins" || v.access === "signed_in";
  if (v.isDefault) return signIn ? "/admin/dashboard (and /dashboard)" : "/dashboard";
  return (signIn ? "/admin/dashboards/" : "/dashboards/") + (v.slug || "…");
};

// Editor creates or changes a dashboard: title, link, access, names, and
// which repositories it shows (picked, or by group pattern, or all), with a
// live count of the repositories that match.
function Editor({ dashboard, repos, onSaved, onCancel }: { dashboard?: Dashboard; repos: Repo[]; onSaved: (d: Dashboard) => void; onCancel: () => void }) {
  const toast = useToast();
  const [v, setV] = useState<Draft>(() => toDraft(dashboard));
  const [q, setQ] = useState("");
  const [match, setMatch] = useState<{ count: number; repos: string[] } | null>(null);
  const [np, setNp] = useState<Pattern>(() => ({ platform: repos[0]?.platform ?? "gitlab", host: repos[0]?.host ?? "", pattern: "" }));
  const hosts = useMemo(() => Array.from(new Set(repos.map((r) => r.platform + "|" + r.host))).sort(), [repos]);
  const shown = repos.filter((r) => !q || (r.host + "/" + r.path).toLowerCase().includes(q.toLowerCase()));

  // Live count of the repositories the dashboard would show.
  useEffect(() => {
    const t = window.setTimeout(() => {
      const p = new URLSearchParams();
      if (v.all_repos) p.set("all", "1");
      v.repos.forEach((id) => p.append("repo", String(id)));
      v.patterns.forEach((x) => p.append("pattern", x.platform + "|" + x.host + "|" + x.pattern));
      get<{ count: number; repos: string[] }>("/admin/api/dashboards/match?" + p.toString())
        .then(setMatch)
        .catch(() => setMatch(null));
    }, 250);
    return () => window.clearTimeout(t);
  }, [v.all_repos, v.repos, v.patterns]);

  const setAccess = (access: Access) =>
    // A new public or token dashboard starts with names hidden: showing them is a choice.
    setV({ ...v, access, hide_repos: !v.id && (access === "public" || access === "token") ? true : v.hide_repos });
  const toggle = (id: number, on: boolean) => {
    const next = new Set(v.repos);
    if (on) next.add(id);
    else next.delete(id);
    setV({ ...v, repos: next });
  };
  const addPattern = () => {
    const pattern = np.pattern.trim().replace(/^\/+|\/+$/g, "");
    if (!pattern || !np.host) return;
    setV({ ...v, patterns: [...v.patterns, { ...np, pattern }] });
    setNp({ ...np, pattern: "" });
  };
  const save = () => {
    const body = {
      slug: v.slug.trim(), title: v.title.trim(), description: v.description.trim(), access: v.access, hide_repos: v.hide_repos, enabled: v.enabled,
      all_repos: v.all_repos, repos: Array.from(v.repos), patterns: v.patterns,
    };
    (v.id ? put<Dashboard>("/admin/api/dashboards/" + v.id, body) : post<Dashboard>("/admin/api/dashboards", body))
      .then((d) => {
        toast((v.id ? "Saved " : "Created ") + d.title);
        onSaved(d);
      })
      .catch((e) => toast(errMsg(e), true));
  };

  return (
    <div className="grid gap-6">
      <FormGrid className="lg:grid-cols-2">
        <Field label="Title">
          <Input value={v.title} maxLength={100} placeholder="e.g. HRMS" onChange={(e) => setV({ ...v, title: e.target.value, slug: v.slugTouched ? v.slug : slugify(e.target.value) })} />
        </Field>
        <Field label="Link" hint={<Sub>{linkOf(v)}</Sub>}>
          <Input value={v.slug} disabled={v.isDefault} spellCheck={false} placeholder="hrms" onChange={(e) => setV({ ...v, slug: e.target.value, slugTouched: true })} />
        </Field>
        <Field label="Description" className="lg:col-span-2">
          <Input value={v.description} maxLength={500} placeholder="optional" onChange={(e) => setV({ ...v, description: e.target.value })} />
        </Field>
        <Field label="Who can open it">
          <NativeSelect value={v.access} onChange={(e) => setAccess(e.target.value as Access)}>
            {(Object.keys(accessLabels) as Access[]).map((a) => (
              <option key={a} value={a}>
                {accessLabels[a]}
              </option>
            ))}
          </NativeSelect>
        </Field>
        <Field label="Repository names" hint={v.access === "public" && !v.hide_repos ? <Sub className="text-warning">Anyone who can reach the console sees these repositories' names and scores.</Sub> : undefined}>
          <NativeSelect value={String(v.hide_repos)} onChange={(e) => setV({ ...v, hide_repos: e.target.value === "true" })}>
            <option value="false">Shown, with their scores</option>
            <option value="true">Hidden (totals only)</option>
          </NativeSelect>
        </Field>
        <Field label="Status">
          <NativeSelect value={String(v.enabled)} onChange={(e) => setV({ ...v, enabled: e.target.value === "true" })}>
            <option value="true">On</option>
            <option value="false">Off</option>
          </NativeSelect>
        </Field>
      </FormGrid>
      {v.access === "token" && <Sub>Generate its token from the list once it is saved: it is shown once, and opens only this dashboard.</Sub>}
      <Separator />
      <div className="grid gap-3">
        <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">Repositories</h3>
        <label className="flex items-center gap-2 text-sm">
          <Checkbox checked={v.all_repos} onCheckedChange={(x) => setV({ ...v, all_repos: x === true })} />
          Every repository the console reviews
        </label>
        {!v.all_repos && (
          <>
            <div className="grid gap-2">
              <div className="flex flex-wrap items-center gap-2">
                <div className="relative w-full sm:w-72">
                  <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
                  <Input type="search" className="pl-8" placeholder="Filter, e.g. a group" aria-label="Filter repositories" value={q} onChange={(e) => setQ(e.target.value)} />
                </div>
                <Button size="sm" variant="outline" disabled={!shown.length} onClick={() => setV({ ...v, repos: new Set([...v.repos, ...shown.map((r) => r.id)]) })}>
                  Add all shown ({shown.length})
                </Button>
                <Button size="sm" variant="ghost" disabled={!v.repos.size} onClick={() => setV({ ...v, repos: new Set() })}>
                  Clear
                </Button>
                <span className="text-sm text-muted-foreground">{v.repos.size} picked</span>
              </div>
              <div className="max-h-64 overflow-y-auto rounded-md border">
                {!shown.length ? (
                  <div className="p-3 text-sm text-muted-foreground">No repositories match.</div>
                ) : (
                  shown.map((r) => (
                    <label key={r.id} className="flex items-center gap-2 border-b px-3 py-1.5 text-sm last:border-0 hover:bg-muted/50">
                      <Checkbox checked={v.repos.has(r.id)} onCheckedChange={(x) => toggle(r.id, x === true)} />
                      <span className="font-medium">{r.path}</span>
                      <span className="text-xs text-muted-foreground">{r.host}</span>
                    </label>
                  ))
                )}
              </div>
            </div>
            <div className="grid gap-2">
              <Sub>Patterns add every repository in a group (subgroups included) or matching a pattern, including new ones as they appear.</Sub>
              {v.patterns.map((p, i) => (
                <div key={i} className="flex items-center gap-2 text-sm">
                  <code>{p.pattern}</code>
                  <span className="text-muted-foreground">
                    {p.platform} · {p.host}
                  </span>
                  <Button size="sm" variant="ghost" aria-label={"Remove pattern " + p.pattern} onClick={() => setV({ ...v, patterns: v.patterns.filter((_, j) => j !== i) })}>
                    <Trash2 />
                  </Button>
                </div>
              ))}
              <div className="flex flex-wrap items-center gap-2">
                <NativeSelect className="w-auto max-w-72" aria-label="Host" value={np.platform + "|" + np.host}
                  onChange={(e) => {
                    const [platform, host] = e.target.value.split("|");
                    setNp({ ...np, platform, host });
                  }}>
                  {!hosts.length && <option value="|">no repositories yet</option>}
                  {hosts.map((h) => (
                    <option key={h} value={h}>
                      {h.split("|")[1]} ({h.split("|")[0]})
                    </option>
                  ))}
                </NativeSelect>
                <Input className="w-56" placeholder="ifmists/hrms or acme/svc-*" spellCheck={false} aria-label="Pattern" value={np.pattern}
                  onChange={(e) => setNp({ ...np, pattern: e.target.value })} onKeyDown={(e) => e.key === "Enter" && addPattern()} />
                <Button size="sm" variant="outline" disabled={!np.pattern.trim() || !np.host} onClick={addPattern}>
                  <Plus /> Add pattern
                </Button>
              </div>
            </div>
          </>
        )}
        <div className="rounded-md bg-muted/50 p-3 text-sm">
          {match === null ? (
            <span className="text-muted-foreground">Counting…</span>
          ) : (
            <>
              <b>{match.count}</b> {match.count === 1 ? "repository matches" : "repositories match"}
              {match.count > 0 && <span className="text-muted-foreground">: {match.repos.slice(0, 8).join(", ")}{match.count > 8 ? ", …" : ""}</span>}
            </>
          )}
        </div>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button onClick={save}>{v.id ? "Save dashboard" : "Create dashboard"}</Button>
        <Button variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

// TokenDialog generates a dashboard's token and shows its link once.
function TokenPanel({ d, onDone }: { d: Dashboard; onDone: () => void }) {
  const toast = useToast();
  const confirm = useConfirm();
  const copy = useCopy();
  const [days, setDays] = useState(90);
  const [link, setLink] = useState("");
  const rotate = async () => {
    if (d.token_set && !(await confirm({ title: "Replace the token of " + d.title + "?", description: "The current link stops working at once. Screens and people using it need the new one.", action: "Replace token", danger: true }))) return;
    post<{ link: string }>(`/admin/api/dashboards/${d.id}/token`, { days })
      .then((r) => {
        setLink(location.origin + r.link);
        onDone();
      })
      .catch((e) => toast(errMsg(e), true));
  };
  return (
    <div className="grid gap-3">
      <Sub>The token opens only this dashboard. Only a hash is stored, so the link is shown once.</Sub>
      <div className="flex flex-wrap items-center gap-2">
        <NativeSelect className="w-auto" aria-label="Valid for" value={String(days)} onChange={(e) => setDays(parseInt(e.target.value, 10))}>
          <option value="7">7 days</option>
          <option value="30">30 days</option>
          <option value="90">90 days</option>
          <option value="365">A year</option>
          <option value="0">Never expires</option>
        </NativeSelect>
        <Button size="sm" onClick={rotate}>
          <KeyRound /> {d.token_set ? "Replace token" : "Generate token"}
        </Button>
      </div>
      {link && (
        <div className="grid gap-2 rounded-lg border border-warning/40 bg-warning-soft p-3">
          <div className="text-sm font-medium">Copy the link now: it is not shown again.</div>
          <code className="text-xs break-all">{link}</code>
          <div>
            <Button size="sm" variant="outline" onClick={() => copy(link)}>
              <Copy /> Copy link
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

export default function Dashboards() {
  const me = useMe();
  const toast = useToast();
  const confirm = useConfirm();
  const copy = useCopy();
  const licensed = useFeature("metrics");
  const admin = me.role === "admin" || !me.role;
  const [list, setList] = useState<Dashboard[] | null>(null);
  const [openable, setOpenable] = useState<Openable[] | null>(null);
  const [repos, setRepos] = useState<Repo[]>([]);
  const [editing, setEditing] = useState<number | null>(null); // 0 = new
  const [tokenFor, setTokenFor] = useState<number | null>(null);
  const load = () =>
    get<{ dashboards: (Dashboard | Openable)[] }>("/admin/api/dashboards")
      .then((r) => (admin ? setList(r.dashboards as Dashboard[]) : setOpenable(r.dashboards as Openable[])))
      .catch((e) => toast(errMsg(e), true));
  useEffect(() => {
    load();
    if (admin) get<Repo[]>("/admin/api/repos").then(setRepos).catch(() => {});
  }, []);

  if (!admin)
    return (
      <section className="grid gap-4">
        <Panel>
          {!openable ? (
            <Empty>Loading…</Empty>
          ) : !openable.length ? (
            <Empty>No dashboard is open to you.</Empty>
          ) : (
            <Table>
              <TableBody>
                {openable.map((d) => (
                  <TableRow key={d.slug}>
                    <TableCell className="pl-4">
                      <div className="font-medium">{d.title}</div>
                      {d.description && <Sub>{d.description}</Sub>}
                    </TableCell>
                    <TableCell className="pr-4 text-right">
                      <Button variant="outline" size="sm" asChild>
                        <a href={d.url}>
                          <ExternalLink /> Open
                        </a>
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Panel>
      </section>
    );

  const edited = editing ? list?.find((d) => d.id === editing) : undefined;
  const tokenDash = tokenFor ? list?.find((d) => d.id === tokenFor) : undefined;
  const remove = async (d: Dashboard) => {
    if (!(await confirm({ title: "Delete " + d.title + "?", description: "Its link stops working. Its repositories and their reviews stay.", action: "Delete dashboard", danger: true }))) return;
    del("/admin/api/dashboards/" + d.id)
      .then(() => (toast("Deleted " + d.title), load()))
      .catch((e) => toast(errMsg(e), true));
  };
  return (
    <section className="grid gap-4">
      <Toolbar>
        <span className="text-sm text-muted-foreground">Each dashboard shows only its repositories, to the people its access lets in.</span>
        <span className="flex-1" />
        <Button variant="outline" disabled={!licensed} onClick={() => setEditing(0)}>
          <Plus /> New dashboard
        </Button>
      </Toolbar>
      {!licensed && <LockNote feature="metrics" />}
      <Panel>
        {!list ? (
          <Empty>Loading…</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">Dashboard</TableHead>
                <TableHead>Who can open it</TableHead>
                <TableHead>Repositories</TableHead>
                <TableHead className={stickyEnd}>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((d) => (
                <TableRow key={d.id || d.slug}>
                  <TableCell className="pl-4 align-top">
                    <div className="flex items-center gap-2">
                      <span className="font-medium">{d.title}</span>
                      {d.default && <StateBadge kind="idle">default</StateBadge>}
                      {!d.enabled && <StateBadge kind="off">Off</StateBadge>}
                    </div>
                    <Sub>
                      <a className="underline-offset-2 hover:underline" href={d.url}>
                        {d.url}
                      </a>
                      {d.hide_repos ? " · names hidden" : ""}
                    </Sub>
                  </TableCell>
                  <TableCell className="align-top whitespace-normal">
                    <div>{accessLabels[d.access]}</div>
                    <TokenState d={d} />
                  </TableCell>
                  <TableCell className="align-top">{d.all_repos ? "All" : d.repo_count}</TableCell>
                  <TableCell className={cn("pr-4 text-right align-top whitespace-nowrap", stickyEnd)}>
                    <Button variant="ghost" size="sm" title={d.access === "token" ? "Copies the link without a token: generate one to share" : undefined} onClick={() => copy(location.origin + d.url)}>
                      <Copy /> Copy link
                    </Button>
                    {d.access === "token" && d.id !== 0 && (
                      <Button variant="ghost" size="sm" onClick={() => setTokenFor(d.id)}>
                        <KeyRound /> Token
                      </Button>
                    )}
                    <Button variant="ghost" size="sm" disabled={d.id === 0 || (!d.default && !licensed)} onClick={() => setEditing(d.id)}>
                      Edit
                    </Button>
                    {!d.default && (
                      <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => remove(d)}>
                        Delete
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
      <Note>
        <b>Signed-in admins</b> and <b>anyone signed in</b> open their dashboards under <code>/admin/dashboards/…</code>; <b>token</b> and <b>public</b> dashboards at{" "}
        <code>/dashboards/…</code>, with their JSON at <code>/api/dashboards/…/status</code>. The default dashboard keeps <code>/dashboard</code>, so links shared before keep working.
        Every dashboard shows its repositories' running and recent reviews, scores and totals; other repositories never appear.
      </Note>

      <SidePanel open={editing !== null} onClose={() => setEditing(null)} title={edited ? "Edit " + edited.title : "New dashboard"} description="Who can open it, and which repositories it shows." wide>
        {editing !== null && <Editor key={editing} dashboard={edited} repos={repos} onSaved={() => (setEditing(null), load())} onCancel={() => setEditing(null)} />}
      </SidePanel>
      <SidePanel open={!!tokenDash} onClose={() => setTokenFor(null)} title={tokenDash ? "Token: " + tokenDash.title : ""} description="For screens and people without a sign-in.">
        {tokenDash && <TokenPanel d={tokenDash} onDone={load} />}
      </SidePanel>
    </section>
  );
}
