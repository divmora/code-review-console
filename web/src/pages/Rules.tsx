import { useEffect, useState } from "react";
import { Plus } from "lucide-react";
import { del, get, post, put } from "../api";
import { ago } from "../format";
import type { Repo, Rule } from "../types";
import { Chips, Empty, errMsg, Field, FormGrid, Locked, NativeSelect, Note, Panel, SettingsEditor, settingsChips, SidePanel, Sub, Toolbar, Tri, useConfirm, useFeature, useToast } from "../ui";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Flag = "enabled" | "require_reviewer" | "skip_branch_sync" | "score_labels" | "commit_status";
const flags: [Flag, string, string, string][] = [
  ["enabled", "Reviews", "Enabled", "Disabled"],
  ["require_reviewer", "Reviewer required", "On", "Off"],
  ["skip_branch_sync", "Branch syncs", "Skip", "Review"],
  ["score_labels", "Score labels", "On", "Off"],
  ["commit_status", "Commit status", "On", "Off"],
];
const label = (r: Rule) => r.pattern || "Whole host";

function RuleEditor({ rule, onSave, onDelete }: { rule: Rule; onSave: (b: any) => void; onDelete: () => void }) {
  const [vals, setVals] = useState<Record<Flag, boolean | null>>(() => Object.fromEntries(flags.map(([k]) => [k, rule[k] ?? null])) as Record<Flag, boolean | null>);
  return (
    <SettingsEditor
      value={rule.settings}
      inheritModel="Inherit"
      onSave={(s) => onSave({ ...vals, settings: s })}
      danger={
        <Button variant="ghost" className="text-destructive hover:text-destructive" onClick={onDelete}>
          Delete rule
        </Button>
      }
      extra={
        <>
          <div className="grid gap-3">
            <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">Settings for this level</h3>
            <FormGrid className="lg:grid-cols-2">
              {flags.map(([k, name, on, off]) => (
                <Field label={name} key={k}>
                  <Tri value={vals[k]} on={on} off={off} onChange={(v) => setVals({ ...vals, [k]: v })} />
                </Field>
              ))}
            </FormGrid>
          </div>
          <Separator />
          <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">Review settings</h3>
        </>
      }
    />
  );
}

export default function Rules() {
  const toast = useToast();
  const confirm = useConfirm();
  const licensed = useFeature("rules");
  const [rules, setRules] = useState<Rule[] | null>(null);
  const [repos, setRepos] = useState<Repo[]>([]);
  const [editing, setEditing] = useState<number | null>(null);
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ platform: "gitlab", host: "", pattern: "", enabled: null as boolean | null });
  const reloadRepos = () => get<Repo[]>("/admin/api/repos").then((r) => setRepos(r || []));
  useEffect(() => {
    get<Rule[]>("/admin/api/rules").then((r) => setRules(r || [])).catch((e) => toast(errMsg(e), true));
    reloadRepos().catch(() => {});
  }, []);

  const covers = (rule: Rule) => {
    const l = rule.pattern ? rule.pattern : "host " + rule.host;
    return repos.filter((r) => (r.effective.rules || []).includes(l) && r.platform === rule.platform && r.host === rule.host).length;
  };
  const add = () => {
    const body: any = { platform: form.platform, host: form.host.trim(), pattern: form.pattern.trim() };
    if (form.enabled !== null) body.enabled = form.enabled;
    post<Rule>("/admin/api/rules", body)
      .then((r) => {
        setRules((rs) => (rs || []).filter((x) => x.id !== r.id).concat([r]));
        setForm({ ...form, pattern: "" });
        setAdding(false);
        setEditing(r.id);
        toast("Saved rule " + label(r) + " — set its options");
        return reloadRepos();
      })
      .catch((e) => toast(errMsg(e), true));
  };
  const save = (rule: Rule, body: any) =>
    put<Rule>("/admin/api/rules/" + rule.id, body)
      .then((r) => {
        setRules((rs) => (rs || []).map((x) => (x.id === r.id ? r : x)));
        setEditing(null);
        toast("Saved " + label(r));
        return reloadRepos();
      })
      .catch((e) => toast(errMsg(e), true));
  const remove = async (rule: Rule) => {
    if (!(await confirm({ title: "Delete the rule for " + label(rule) + "?", description: "On " + rule.host + ". Repositories fall back to the level above.", action: "Delete rule", danger: true }))) return;
    del("/admin/api/rules/" + rule.id)
      .then(() => {
        setRules((rs) => (rs || []).filter((x) => x.id !== rule.id));
        setEditing(null);
        toast("Deleted rule " + label(rule));
        return reloadRepos();
      })
      .catch((e) => toast(errMsg(e), true));
  };

  const sorted = (rules || []).slice().sort((a, b) =>
    (a.platform + a.host).localeCompare(b.platform + b.host) || (a.pattern ? 1 : 0) - (b.pattern ? 1 : 0) || a.pattern.localeCompare(b.pattern));
  const edited = rules?.find((r) => r.id === editing) ?? null;

  return (
    <section className="grid gap-4">
      {!licensed && (
        <Locked feature="rules">
          Settings for a whole host, a group or a name pattern are part of the licensed console. Without a license, repositories follow the global settings and their own mode; any rules below are kept but ignored.
        </Locked>
      )}
      <Toolbar>
        <span className="text-sm text-muted-foreground">Settings for a whole host, or for a group, organisation or name pattern on a host.</span>
        <span className="flex-1" />
        {licensed && (
          <Button variant="outline" onClick={() => setAdding(true)}>
            <Plus /> Add rule
          </Button>
        )}
      </Toolbar>
      <Panel>
        {!rules ? (
          <Empty>Loading…</Empty>
        ) : !rules.length ? (
          <Empty>No rules yet. Add one for a whole host, a group or a name pattern.</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">Scope</TableHead>
                <TableHead>Sets</TableHead>
                <TableHead>Covers</TableHead>
                <TableHead>Last changed</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {sorted.map((rule) => {
                const n = covers(rule);
                const set = flags.filter(([k]) => rule[k] !== null && rule[k] !== undefined).map(([k, name, on, off]) => name + ": " + (rule[k] ? on : off));
                return (
                  <TableRow key={rule.id}>
                    <TableCell className="pl-4 align-top">
                      <div>{rule.pattern ? <code>{rule.pattern}</code> : <b>Whole host</b>}</div>
                      <Sub>
                        {rule.platform} · {rule.host}
                      </Sub>
                    </TableCell>
                    <TableCell className="align-top whitespace-normal">
                      <Chips items={set} empty="inherits everything" />
                      <div className="mt-1">
                        <Chips items={settingsChips(rule.settings)} />
                      </div>
                    </TableCell>
                    <TableCell className="align-top text-muted-foreground">{n + (n === 1 ? " known repository" : " known repositories")}</TableCell>
                    <TableCell className="align-top text-muted-foreground">{rule.updated_by ? rule.updated_by + " · " + ago(rule.updated_at) : "—"}</TableCell>
                    <TableCell className="pr-4 text-right align-top">
                      {licensed ? (
                        <Button variant="ghost" size="sm" onClick={() => setEditing(rule.id)}>
                          Edit
                        </Button>
                      ) : (
                        <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => remove(rule)}>
                          Delete
                        </Button>
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
        Levels, broadest first: <b>Settings</b> (everything) → <b>whole host</b> (empty pattern) → <b>group or owner</b> (<code>acme</code>, <code>acme/platform</code>, subgroups included) or <b>pattern</b> (<code>acme/svc-*</code>; <code>*</code> stays within one level) → <b>repository</b>. For each setting the most specific level that sets it wins; <b>Inherit</b> means "use the level above". Review settings add up the same way. Hosts are separate: a rule for <code>acme</code> on one host never touches <code>acme</code> on another.
      </Note>

      <SidePanel open={!!edited} onClose={() => setEditing(null)} title={edited ? label(edited) : ""} description={edited ? `${edited.platform} · ${edited.host}` : undefined}>
        {edited && <RuleEditor key={edited.id} rule={edited} onSave={(b) => save(edited, b)} onDelete={() => remove(edited)} />}
      </SidePanel>

      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Add rule</DialogTitle>
            <DialogDescription>After adding it, set its options.</DialogDescription>
          </DialogHeader>
          <FormGrid className="lg:grid-cols-2">
            <Field label="Platform">
              <NativeSelect value={form.platform} onChange={(e) => setForm({ ...form, platform: e.target.value })}>
                <option value="gitlab">GitLab</option>
                <option value="github">GitHub</option>
              </NativeSelect>
            </Field>
            <Field label="Host">
              <Input placeholder="gitlab.com" value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} />
            </Field>
            <Field label="Group, owner or pattern" className="sm:col-span-2">
              <Input placeholder="empty = whole host · acme · acme/svc-*" value={form.pattern} onChange={(e) => setForm({ ...form, pattern: e.target.value })} />
            </Field>
            <Field label="Reviews">
              <Tri value={form.enabled} on="Enabled" off="Disabled" onChange={(v) => setForm({ ...form, enabled: v })} />
            </Field>
          </FormGrid>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAdding(false)}>
              Cancel
            </Button>
            <Button onClick={add}>Add rule</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
