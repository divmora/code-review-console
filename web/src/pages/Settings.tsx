import { ReactNode, useEffect, useState } from "react";
import { get, put } from "../api";
import type { Overrides } from "../types";
import { Empty, errMsg, Field, FormGrid, LockNote, NativeSelect, Note, SettingsEditor, useFeature, useMe, useRefreshMe, useToast } from "../ui";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";

type Eff = {
  allowlist: boolean; require_reviewer: boolean; skip_branch_sync: boolean; sync_source_branches: string[];
  score_labels: boolean; commit_status: boolean; review_retention_days: number; webhook_retention_days: number;
  learnings_enabled: boolean; learnings_auto_activate: boolean; daily_token_budget: number; repo_daily_token_budget: number;
  dashboard_enabled: boolean; hide_repos: boolean; dashboard_title: string; review_defaults: Overrides; source?: Record<string, string>;
};

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
  const fromFile = Object.keys(eff.source || {}).filter((k) => eff.source![k] === "config");
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
        </FormGrid>
      </Group>
      <div className="grid gap-4 lg:grid-cols-2">
        <Group title="Learnings">
          <FormGrid className="lg:grid-cols-2">
            <Bool label="Learn from replies to the bot" value={v.learnings_enabled} onChange={set("learnings_enabled")} yes="On" no="Off" disabled={!has.learnings} note={<LockNote feature="learnings" />} />
            <Bool label="New learnings" value={v.learnings_auto_activate} onChange={set("learnings_auto_activate")} yes="Apply at once" no="Wait for an admin's approval" disabled={!has.learnings} />
          </FormGrid>
        </Group>
        <Group title="Budgets" description={'Input plus output tokens of finished jobs in the current UTC day. When a budget is used up, automatic reviews and chat commands wait until the next day; "Review now" and re-runs from this panel still run.'}>
          <FormGrid className="lg:grid-cols-2">
            <Num label="Daily tokens, all repositories" min={0} step={10000} placeholder="0 = no limit" value={v.daily_token_budget} onChange={set("daily_token_budget")} disabled={!has.budgets} note={<LockNote feature="budgets" />} />
            <Num label="Daily tokens per repository" min={0} step={10000} placeholder="0 = no limit" value={v.repo_daily_token_budget} onChange={set("repo_daily_token_budget")} disabled={!has.budgets} />
          </FormGrid>
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
        <Group title="Status dashboard">
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
          <span className="text-sm text-muted-foreground">{fromFile.length ? "Not saved yet (from the config file): " + fromFile.join(", ") : ""}</span>
        </CardFooter>
      </Card>
      <Group title="Default review settings" description="Apply to every repository. Group and pattern rules, then repository settings, override them.">
        <SettingsEditor key={edGen} value={eff.review_defaults} onSave={save} saveLabel="Save default review settings" />
      </Group>
      <Note>
        The config file is the initial configuration: on first start its values are copied here, and from then on these settings are kept in the database. Editing the config file later does not change them; the server log notes any difference. Every change is in the audit log.
      </Note>
    </section>
  );
}
