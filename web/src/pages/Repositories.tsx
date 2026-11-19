import { useEffect, useState } from "react";
import { Plus, Search, Settings2 } from "lucide-react";
import { del, get, post, put } from "../api";
import { ago } from "../format";
import type { Access, Overrides, Repo } from "../types";
import { Chips, Empty, errMsg, Field, FormGrid, NativeSelect, Note, Panel, SettingsEditor, settingsChips, SidePanel, StateBadge, Sub, Toolbar, useConfirm, useMe, useToast } from "../ui";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

function whyText(e: Access): string {
  if (e.source === "repository") return "set on this repository";
  if (e.source === "allowlist") return "no rule matches · allowlist default";
  if (e.source === "default") return "no rule matches · reviewed by default";
  return "by " + e.source;
}

export default function Repositories() {
  const me = useMe();
  const toast = useToast();
  const confirm = useConfirm();
  const [repos, setRepos] = useState<Repo[] | null>(null);
  const [q, setQ] = useState("");
  const [show, setShow] = useState("all");
  const [editing, setEditing] = useState<number | null>(null);
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ platform: "github", host: "", path: "", mode: "enabled" });

  const load = () => get<Repo[]>("/admin/api/repos").then(setRepos).catch((e) => toast(errMsg(e), true));
  useEffect(() => {
    load();
  }, []);

  const save = (r: Repo, mode: string, settings: Overrides) =>
    put<Repo>("/admin/api/repos/" + r.id, { mode, settings }).then((x) => {
      setRepos((rs) => (rs || []).map((y) => (y.id === x.id ? x : y)));
      toast(x.path + ": reviews " + (x.effective.enabled ? "on" : "off") + " (" + x.effective.source + ")");
      return x;
    });
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

  const list = (repos || []).filter((r) => {
    if (show === "on" && !r.effective.enabled) return false;
    if (show === "off" && r.effective.enabled) return false;
    return !q || (r.host + "/" + r.path).toLowerCase().includes(q.toLowerCase());
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
        </NativeSelect>
        <span className="text-sm text-muted-foreground">{repos && on + " reviewed · " + (repos.length - on) + " not reviewed"}</span>
        <span className="flex-1" />
        <Button variant="outline" onClick={() => setAdding(true)}>
          <Plus /> Add repository
        </Button>
      </Toolbar>

      <Panel>
        {!repos ? (
          <Empty>Loading…</Empty>
        ) : !list.length ? (
          <Empty>{repos.length ? "No repositories match." : "No repositories yet. They appear when they send their first webhook, or add one above."}</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">Reviews</TableHead>
                <TableHead>Repository</TableHead>
                <TableHead>Last webhook</TableHead>
                <TableHead>Effective settings</TableHead>
                <TableHead>Last changed</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((r) => {
                const e = r.effective;
                const flags = [e.require_reviewer && "reviewer required", e.skip_branch_sync && "skips branch syncs", e.score_labels && "score labels", e.commit_status && "commit status"].filter(Boolean);
                return (
                  <TableRow key={r.id}>
                    <TableCell className="w-44 pl-4 align-top whitespace-normal">
                      <NativeSelect aria-label={"Reviews for " + r.path} value={r.mode} onChange={(ev) => save(r, ev.target.value, r.settings || {}).catch((x) => toast(errMsg(x), true))}>
                        <option value="inherit">Inherit</option>
                        <option value="enabled">Enabled</option>
                        <option value="disabled">Disabled</option>
                      </NativeSelect>
                      <div className="mt-1.5">
                        <StateBadge kind={e.enabled ? "on" : "off"}>{e.enabled ? "Reviewed" : "Not reviewed"}</StateBadge>
                      </div>
                      <Sub>{whyText(e)}</Sub>
                      {flags.length > 0 && <Sub>{flags.join(" · ")}</Sub>}
                    </TableCell>
                    <TableCell className="align-top">
                      <div className="font-medium">{r.path}</div>
                      <Sub>
                        {r.platform} · {r.host}
                      </Sub>
                    </TableCell>
                    <TableCell className="align-top text-muted-foreground">{ago(r.last_seen)}</TableCell>
                    <TableCell className="align-top whitespace-normal">
                      <Chips items={settingsChips(e.settings)} />
                      {(e.rules || []).length > 0 && JSON.stringify(e.settings) !== JSON.stringify(r.settings || {}) && <Sub>includes rule settings</Sub>}
                    </TableCell>
                    <TableCell className="align-top text-muted-foreground">{r.updated_by ? r.updated_by + " · " + ago(r.updated_at) : "—"}</TableCell>
                    <TableCell className="pr-4 text-right align-top">
                      <Button variant="ghost" size="sm" onClick={() => setEditing(r.id)}>
                        <Settings2 /> Settings
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </Panel>

      <Note>
        Repositories appear here automatically when they send their first webhook, set to <b>Inherit</b>: they follow the most specific group or pattern rule, and otherwise the default (
        {me.allowlist ? "not reviewed, because the allowlist is on" : "reviewed, because the allowlist is off"}). Choosing Enabled or Disabled here overrides every rule. Settings here override the rules' settings and the repository's own <code>.codereview.yaml</code>.
      </Note>
      {me.require_reviewer && (
        <Note>
          <b>Reviewer required:</b> even in an enabled repository, a merge request is reviewed only when the bot user is one of its reviewers. Adding the bot as a reviewer starts a review; <code>@bot review</code> comments still work.
        </Note>
      )}

      <SidePanel open={!!edited} onClose={() => setEditing(null)} title={edited?.path ?? ""} description={edited ? `${edited.platform} · ${edited.host} · review settings for this repository` : undefined}>
        {edited && (
          <SettingsEditor
            key={edited.id}
            value={edited.settings}
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
