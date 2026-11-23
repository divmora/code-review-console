import { ReactNode, useEffect, useState } from "react";
import { del, get, post, put } from "../api";
import type { Overrides } from "../types";
import { Empty, errMsg, Field, FormGrid, LockNote, NativeSelect, Note, Section, SettingsEditor, shortAgent, Sub, useConfirm, useFeature, useMe, useRefreshMe, useToast } from "../ui";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import Tuning from "./Tuning";
import { fmtDateTime } from "../format";

type Eff = {
  allowlist: boolean; require_reviewer: boolean; skip_branch_sync: boolean; sync_source_branches: string[];
  score_labels: boolean; commit_status: boolean; approve: boolean; approve_block: string; approve_min_score: number; approve_withdraw: boolean; review_retention_days: number; webhook_retention_days: number;
  learnings_enabled: boolean; learnings_auto_activate: boolean; daily_token_budget: number; repo_daily_token_budget: number;
  dashboard_enabled: boolean; hide_repos: boolean; dashboard_title: string; command_access: "write" | "anyone"; session_idle_minutes: number; review_defaults: Overrides; source?: Record<string, string>;
};

type SessionRow = { id: number; email: string; name: string; created: string; expires: string; current: boolean; ip?: string; user_agent?: string; last_seen?: string };

const when = (iso: string) => fmtDateTime(iso);

// settingLabel names settings the way the page does.
const settingLabel: Record<string, string> = {
  allowlist: "Repositories no rule matches", require_reviewer: "Who asks for a review", skip_branch_sync: "Branch syncs",
  sync_source_branches: "Also treat these source branches as syncs", score_labels: "Score labels", commit_status: "Commit status", approve: "Approve merge requests",
  approve_block: "Findings that block approval", approve_min_score: "Lowest score to approve", approve_withdraw: "When a later review doesn't pass",
  command_access: "Who can run review and question commands", learnings_enabled: "Learn from replies to the bot",
  learnings_auto_activate: "New learnings", daily_token_budget: "Daily tokens, all repositories",
  repo_daily_token_budget: "Daily tokens, per repository", review_retention_days: "Keep reviews",
  webhook_retention_days: "Keep webhook deliveries", dashboard_enabled: "Dashboard", hide_repos: "Repository names",
  dashboard_title: "Dashboard title",
  session_idle_minutes: "Sign out after inactivity",
};

// Sessions lists who is signed in to the admin panel and signs sessions
// out. Sessions are also ended on their own when an admin no longer
// matches the sign-in rules.
function Sessions() {
  const toast = useToast();
  const confirm = useConfirm();
  const [rows, setRows] = useState<SessionRow[] | null>(null);
  useEffect(() => {
    get<SessionRow[]>("/admin/api/sessions").then(setRows).catch((e) => toast(errMsg(e), true));
  }, []);
  const revoke = async (s: SessionRow) => {
    if (s.current) {
      if (!(await confirm({ title: "Sign yourself out?", description: "This is the session you are using now.", action: "Sign out" }))) return;
      post("/admin/logout").finally(() => (location.href = "/admin/login"));
      return;
    }
    if (!(await confirm({ title: "Sign out " + (s.name || s.email) + "?", description: "They can sign in again if the sign-in rules still allow them.", action: "Sign out", danger: true }))) return;
    try {
      setRows(await del<SessionRow[]>("/admin/api/sessions/" + s.id));
      toast("Signed out " + s.email);
    } catch (e) {
      toast(errMsg(e), true);
    }
  };
  const endOthers = async () => {
    if (!(await confirm({ title: "Sign out everyone else?", description: "Every other admin session ends now. Use it after removing someone from the sign-in rules or if a session may be compromised.", action: "Sign out others", danger: true }))) return;
    try {
      setRows(await post<SessionRow[]>("/admin/api/sessions/end-others"));
      toast("Signed out every other session");
    } catch (e) {
      toast(errMsg(e), true);
    }
  };
  const others = rows ? rows.filter((r) => !r.current).length : 0;
  return (
    <Card className="gap-0 overflow-hidden pb-0">
      <CardHeader className="pb-4">
        <CardTitle className="h2">Signed-in admins</CardTitle>
        <CardDescription>
          Every request is checked against the current sign-in rules, so someone removed from them is signed out at once. Group changes at the identity provider apply when the session ends (session_ttl) or here.
        </CardDescription>
        {others > 0 && (
          <div>
            <Button variant="outline" size="sm" onClick={endOthers}>Sign out everyone else</Button>
          </div>
        )}
      </CardHeader>
      {!rows ? (
        <Empty>Loading…</Empty>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="pl-6">Admin</TableHead>
              <TableHead>From</TableHead>
              <TableHead>Signed in</TableHead>
              <TableHead>Last active</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((s) => (
              <TableRow key={s.id}>
                <TableCell className="pl-6">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="font-medium">{s.name || s.email}</span>
                    {s.current && <Badge variant="secondary">you, this browser</Badge>}
                  </div>
                  {s.name && <Sub>{s.email}</Sub>}
                </TableCell>
                <TableCell>
                  <div className="font-mono text-xs">{s.ip || "—"}</div>
                  {s.user_agent && <Sub title={s.user_agent}>{shortAgent(s.user_agent)}</Sub>}
                </TableCell>
                <TableCell>{when(s.created)}</TableCell>
                <TableCell>{s.last_seen ? when(s.last_seen) : "—"}</TableCell>
                <TableCell>{when(s.expires)}</TableCell>
                <TableCell className="pr-6 text-right">
                  <Button size="sm" variant="ghost" className={s.current ? "" : "text-destructive"} onClick={() => revoke(s)}>
                    Sign out
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Card>
  );
}

// Bool is a two-option select for a boolean setting; the first option is true.
function Bool({ label, value, onChange, yes, no, disabled, note }: { label: ReactNode; value: boolean; onChange: (v: boolean) => void; yes: string; no: string; disabled?: boolean; note?: ReactNode }) {
  return (
    <Field label={label} hint={note}>
      <NativeSelect value={String(value)} disabled={disabled} onChange={(e) => onChange(e.target.value === "true")}>
        <option value="true">{yes}</option>
        <option value="false">{no}</option>
      </NativeSelect>
    </Field>
  );
}

function Num({ label, value, onChange, note, ...rest }: { label: string; value: number; onChange: (v: number) => void; min?: number; max?: number; step?: number; placeholder?: string; disabled?: boolean; note?: ReactNode }) {
  return (
    <Field label={label} hint={note}>
      <Input type="number" value={value || ""} {...rest} onChange={(e) => onChange(parseInt(e.target.value || "0", 10))} />
    </Field>
  );
}

function Group({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="h2">{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}

export default function Settings() {
  const toast = useToast();
  const refreshMe = useRefreshMe();
  const me = useMe();
  const has = { learnings: useFeature("learnings"), budgets: useFeature("budgets"), sync: useFeature("branch_sync"), metrics: useFeature("metrics") };
  const [eff, setEff] = useState<Eff | null>(null);
  const [v, setV] = useState<Eff | null>(null);
  const [branches, setBranches] = useState("");
  const [edGen, setEdGen] = useState(0); // remounts the defaults editor after a save
  const take = (e: Eff) => {
    setEff(e);
    setV(e);
    setBranches((e.sync_source_branches || []).join(", "));
  };
  useEffect(() => {
    get<Eff>("/admin/api/settings").then(take).catch((e) => toast(errMsg(e), true));
  }, []);
  if (!eff || !v) return <Empty>Loading…</Empty>;
  const set = <K extends keyof Eff>(k: K) => (x: Eff[K]) => setV({ ...v, [k]: x });
  const save = (defaults: Overrides) => {
    const { source: _s, ...body } = { ...v, review_defaults: defaults, dashboard_title: v.dashboard_title.trim(),
      sync_source_branches: branches.split(",").map((x) => x.trim()).filter(Boolean) };
    put<Eff>("/admin/api/settings", body)
      .then((e) => {
        take(e);
        setEdGen((n) => n + 1);
        refreshMe();
        toast("Settings saved");
      })
      .catch((e) => toast(errMsg(e), true));
  };
  const fromFile = Object.keys(eff.source || {}).filter((k) => eff.source![k] === "config").map((k) => settingLabel[k] || k.replace(/_/g, " "));
  return (
    <section className="grid gap-4">
      <Group title="Repository access" description="Which repositories are reviewed, and when.">
        <FormGrid>
          <Bool label="Repositories no rule matches" value={v.allowlist} onChange={set("allowlist")} yes="Not reviewed (allowlist on)" no="Reviewed (allowlist off)" />
          <Bool label="Who asks for a review" value={v.require_reviewer} onChange={set("require_reviewer")} yes="Only when the bot is a reviewer" no="Any opened MR or push" />
          <Bool label="Branch syncs (source is the default branch)" value={v.skip_branch_sync} onChange={set("skip_branch_sync")} yes="Skip" no="Review" disabled={!has.sync} note={<LockNote feature="branch_sync" />} />
          <Field label="Also treat these source branches as syncs">
            <Input placeholder="develop, release/*" value={branches} disabled={!has.sync} onChange={(e) => setBranches(e.target.value)} />
          </Field>
          <Bool label="Score labels on MRs/PRs" value={v.score_labels} onChange={set("score_labels")} yes="On" no="Off" />
          <Bool label="Commit status on the reviewed commit" value={v.commit_status} onChange={set("commit_status")} yes="On" no="Off" />
          <Field label="Who can run review and question commands" hint={<Note className="text-xs">Commands that run the model spend tokens. Write access means GitLab Developer or higher, GitHub write or above, Bitbucket write or admin (the Bitbucket token needs admin on the repository to check). @bot help works for everyone, and each person is limited to a few commands an hour.</Note>}>
            <NativeSelect value={v.command_access || "write"} onChange={(e) => set("command_access")(e.target.value as Eff["command_access"])}>
              <option value="write">People who can push to the repository</option>
              <option value="anyone">Anyone who can comment</option>
            </NativeSelect>
          </Field>
        </FormGrid>
      </Group>
      <div className="grid gap-4 lg:grid-cols-2">
        <Group title="Approve merge requests" description="The bot approves a merge request whose review passes, as its token's user (or the GitHub App). Rules can turn this on or off for groups and repositories. Needs code-review-bot 0.5 or later on the Executor page.">
          <FormGrid>
            <Bool label="Approve when the review passes" value={v.approve} onChange={set("approve")} yes="On" no="Off" />
            <Field label="Findings that block approval" hint={<Note className="text-xs">New findings, ones beyond the comment limit and ones still open from an earlier review all count; questions don't.</Note>}>
              <NativeSelect value={v.approve_block || "major"} onChange={(e) => set("approve_block")(e.target.value)}>
                <option value="critical">Critical</option>
                <option value="major">Major or critical</option>
                <option value="minor">Minor or above</option>
                <option value="nitpick">Any finding (nitpicks too)</option>
                <option value="none">None: approve whatever it found</option>
              </NativeSelect>
            </Field>
            <Num label="Lowest score to approve" value={v.approve_min_score} onChange={set("approve_min_score")} min={0} max={100} placeholder="0" note={<Note className="text-xs">Every score (quality, security, maintainability) must be at least this. 0 ignores the scores.</Note>} />
            <Bool label="When a later review doesn't pass" value={v.approve_withdraw} onChange={set("approve_withdraw")} yes="Withdraw the bot's approval" no="Leave it" />
          </FormGrid>
          <Note className="text-xs">With withdrawing on, a review that can't judge a new commit (partial, too large) also withdraws the approval (CLI 0.6 or later). GitLab: the bot's user must be allowed to approve. GitHub: withdrawing dismisses the bot's review, which branch protection may restrict.</Note>
        </Group>
        <Group title="Learnings">
          <FormGrid>
            <Bool label="Learn from replies to the bot" value={v.learnings_enabled} onChange={set("learnings_enabled")} yes="On" no="Off" disabled={!has.learnings} note={<LockNote feature="learnings" />} />
            <Bool label="New learnings" value={v.learnings_auto_activate} onChange={set("learnings_auto_activate")} yes="Apply at once" no="Wait for an admin's approval" disabled={!has.learnings}
              note={<Note className="text-xs">Even when set to apply at once, a learning only applies right away if its author can push to the repository (GitLab Developer or higher, GitHub write or above). Anyone else's waits for approval, and so does every learning from Bitbucket.</Note>} />
          </FormGrid>
        </Group>
        <Group title="Budgets" description={'Input plus output tokens of finished jobs in the current UTC day. When a budget is used up, automatic reviews and chat commands wait until the next day; "Review now" and re-runs from this panel still run.'}>
          <FormGrid className="lg:grid-cols-2">
            <Num label="Daily tokens, all repositories" min={0} step={10000} placeholder="0 = no limit" value={v.daily_token_budget} onChange={set("daily_token_budget")} disabled={!has.budgets} note={<LockNote feature="budgets" />} />
            <Num label="Daily tokens per repository" min={0} step={10000} placeholder="0 = no limit" value={v.repo_daily_token_budget} onChange={set("repo_daily_token_budget")} disabled={!has.budgets} />
          </FormGrid>
          {has.budgets && !v.daily_token_budget && !v.repo_daily_token_budget && (
            <Note className="text-warning">No daily limit is set: reviews and commands spend tokens without a cap. Set a budget to bound what a busy day, or misuse, can cost.</Note>
          )}
        </Group>
        <Group title="History">
          <FormGrid className="lg:grid-cols-2">
            <Num
              label="Keep reviews (days)"
              min={1}
              max={3650}
              value={v.review_retention_days}
              onChange={set("review_retention_days")}
              note={!has.metrics && v.review_retention_days > me.free_retention_days ? <span className="locknote text-xs text-warning">Without a license with metrics and long history, reviews are kept {me.free_retention_days} days.</span> : null}
            />
            <Num label="Keep webhook deliveries (days)" min={1} max={3650} value={v.webhook_retention_days} onChange={set("webhook_retention_days")} />
          </FormGrid>
        </Group>
        <Group title="Admin sessions" description="Every admin session also ends after session_ttl (12 hours by default), whatever happens.">
          <FormGrid>
            <Num label="Sign out after inactivity (minutes)" min={0} max={1440} placeholder="0 = never" value={v.session_idle_minutes} onChange={set("session_idle_minutes")}
              note={<Note className="text-xs">A session that makes no request for this long is signed out, so a browser left open on a shared machine doesn't keep admin rights. The panel's own refreshes (Setup, the dashboard) don't count as activity. 5 to 1440, or 0 for never.</Note>} />
          </FormGrid>
        </Group>
        <Group title="Status dashboard" description="Who can open it is set under Status dashboard access, further down.">
          <FormGrid className="lg:grid-cols-2">
            <Bool label="Dashboard" value={v.dashboard_enabled} onChange={set("dashboard_enabled")} yes="On" no="Off" />
            <Bool label="Repository names" value={!v.hide_repos} onChange={(x) => set("hide_repos")(!x)} yes="Shown" no="Hidden" />
            <Field label="Title" className="sm:col-span-2">
              <Input maxLength={120} value={v.dashboard_title} onChange={(e) => set("dashboard_title")(e.target.value)} />
            </Field>
          </FormGrid>
        </Group>
      </div>
      <Card className="py-4">
        <CardFooter className="flex flex-wrap items-center gap-3">
          <Button onClick={() => save(eff.review_defaults)}>Save settings</Button>
          <span className="text-sm text-muted-foreground">{fromFile.length ? "Showing defaults for: " + fromFile.join("; ") + ". Save to keep them." : ""}</span>
        </CardFooter>
      </Card>
      <Group title="Default review settings" description="Apply to every repository. Group and pattern rules, then repository settings, override them.">
        <SettingsEditor key={edGen} value={eff.review_defaults} onSave={save} saveLabel="Save default review settings" />
      </Group>
      <Section name="Queue, review engine and access settings"><Tuning /></Section>
      <Section name="Sign-in sessions"><Sessions /></Section>
      <Note>
        These settings are kept in the database and apply without a restart. Every change is in the audit log.
      </Note>
    </section>
  );
}
