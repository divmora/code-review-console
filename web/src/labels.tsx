import { useEffect, useMemo, useState } from "react";
import { Tags } from "lucide-react";
import { get, post } from "./api";
import type { Labels, Repo } from "./types";
import { errMsg, Field, FormGrid, NativeSelect, Sub } from "./ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

// Label suggestions in review settings (issue #162). The patterns work as
// the CLI matches them: * any run of characters, ? one, case-insensitive,
// anything else literal; suggest-only wins over allow.

export function labelGlob(pattern: string, name: string): boolean {
  const p = pattern.trim();
  if (!p) return false;
  if (!/[*?]/.test(p)) return p.toLowerCase() === name.toLowerCase();
  const re = "^" + p.split("").map((c) => (c === "*" ? ".*" : c === "?" ? "." : c.replace(/[.+^${}()|[\]\\/-]/g, "\\$&"))).join("") + "$";
  try {
    return new RegExp(re, "i").test(name);
  } catch {
    return false;
  }
}

// labelUse is how the patterns treat a label: set (allowed), suggest
// (suggest only) or "" (never used).
export function labelUse(name: string, allow: string[], suggestOnly: string[]): "set" | "suggest" | "" {
  if (suggestOnly.some((p) => labelGlob(p, name))) return "suggest";
  if (allow.some((p) => labelGlob(p, name))) return "set";
  return "";
}

const scopeOf = (name: string) => {
  const i = name.lastIndexOf("::");
  return i > 0 ? name.slice(0, i) : "";
};

export const parsePatterns = (s: string) =>
  s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter(Boolean);

export function labelsChip(l?: Labels): string | null {
  if (!l?.mode) return l && (l.allow || l.suggest_only) ? "label patterns" : null;
  return "labels " + l.mode;
}

// LabelFields edits the labels block. The text fields hold one pattern
// per line (or comma-separated).
export type LabelForm = { mode: string; allow: string; suggestOnly: string; max: string; instructions: string };

export const labelForm = (l?: Labels): LabelForm => ({
  mode: l?.mode ?? "",
  allow: (l?.allow ?? []).join("\n"),
  suggestOnly: (l?.suggest_only ?? []).join("\n"),
  max: l?.max ? String(l.max) : "",
  instructions: l?.instructions ?? "",
});

export function labelsOf(f: LabelForm): Labels | undefined {
  const l: Labels = {};
  if (f.mode) l.mode = f.mode as Labels["mode"];
  const allow = parsePatterns(f.allow);
  const so = parsePatterns(f.suggestOnly);
  if (allow.length) l.allow = allow;
  if (so.length) l.suggest_only = so;
  if (Number(f.max) > 0) l.max = Number(f.max);
  if (f.instructions.trim()) l.instructions = f.instructions.trim();
  return Object.keys(l).length ? l : undefined;
}

export function LabelFields({ value, onChange, repo }: { value: LabelForm; onChange: (f: LabelForm) => void; repo?: RepoRef }) {
  const set = (k: keyof LabelForm) => (v: string) => onChange({ ...value, [k]: v });
  const allow = parsePatterns(value.allow);
  const so = parsePatterns(value.suggestOnly);
  const addTo = (k: "allow" | "suggestOnly", p: string) => {
    const cur = parsePatterns(value[k]);
    if (!cur.some((x) => x.toLowerCase() === p.toLowerCase())) onChange({ ...value, [k]: [...cur, p].join("\n") });
  };
  return (
    <div className="grid gap-3 rounded-md border p-3">
      <h3 className="flex items-center gap-1.5 text-xs font-semibold tracking-wide text-muted-foreground uppercase">
        <Tags className="size-3.5" /> Labels
      </h3>
      <FormGrid className="lg:grid-cols-2">
        <Field
          label="Label suggestions"
          hint={<Sub>Picks the project's own labels for each merge request. Suggest adds a line to the walkthrough; Apply also sets them (the bot only ever removes labels it set). Needs code-review-bot 0.8.0 or later.</Sub>}
        >
          <NativeSelect value={value.mode} onChange={(e) => set("mode")(e.target.value)}>
            <option value="">Inherit (repository config, off by default)</option>
            <option value="off">Off</option>
            <option value="suggest">Suggest</option>
            <option value="apply">Apply</option>
          </NativeSelect>
        </Field>
        <Field label="Labels per merge request" hint={<Sub>At most 10; one per scoped group like <code>type::</code>.</Sub>}>
          <Input type="number" min={1} max={10} placeholder="Inherit (4)" value={value.max} onChange={(e) => set("max")(e.target.value)} />
        </Field>
        <Field label="Allowed labels" hint={<Sub>One pattern per line, for example <code>type::*</code> or <code>backend</code>. Only labels the project already has are used. Empty: inherit.</Sub>}>
          <Textarea rows={3} placeholder={"type::*\ncomponent::*"} value={value.allow} onChange={(e) => set("allow")(e.target.value)} />
        </Field>
        <Field label="Suggest only" hint={<Sub>Shown in the walkthrough, never set, even in Apply mode, for example <code>priority::*</code>.</Sub>}>
          <Textarea rows={3} placeholder="priority::*" value={value.suggestOnly} onChange={(e) => set("suggestOnly")(e.target.value)} />
        </Field>
      </FormGrid>
      <Field label="Label instructions">
        <Textarea rows={2} maxLength={4000} placeholder="For example: type::maintenance is for refactors and dependency bumps" value={value.instructions} onChange={(e) => set("instructions")(e.target.value)} />
      </Field>
      <LabelPicker repo={repo} allow={allow} suggestOnly={so} onAdd={addTo} />
    </div>
  );
}

export type RepoRef = { platform: string; host: string; path?: string };

type Listed = { listable: boolean; labels: { name: string; description?: string }[] };

// LabelPicker reads a repository's labels from the code host (with the
// bot's token) and shows which ones the patterns allow.
function LabelPicker({ repo, allow, suggestOnly, onAdd }: { repo?: RepoRef; allow: string[]; suggestOnly: string[]; onAdd: (k: "allow" | "suggestOnly", p: string) => void }) {
  const [open, setOpen] = useState(false);
  const [repos, setRepos] = useState<Repo[]>([]);
  const [pick, setPick] = useState(repo?.path ? `${repo.platform}:${repo.host}/${repo.path}` : "");
  const [res, setRes] = useState<Listed | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open && !repos.length) get<Repo[]>("/admin/api/repos").then((r) => setRepos(r || [])).catch(() => {});
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps
  const options = repos.filter((r) => !repo || (r.platform === repo.platform && r.host === repo.host)).map((r) => `${r.platform}:${r.host}/${r.path}`);
  const load = () => {
    const m = /^([a-z]+):([^/]+)\/(.+)$/.exec(pick.trim());
    const ref = m ? { platform: m[1], host: m[2], repo: m[3] } : repo ? { platform: repo.platform, host: repo.host, repo: pick.trim() } : null;
    if (!ref || !ref.repo) {
      setErr("Pick a repository (platform:host/path).");
      return;
    }
    setBusy(true);
    setErr("");
    post<Listed>("/admin/api/repo-labels", ref)
      .then(setRes)
      .catch((e) => {
        setRes(null);
        setErr(errMsg(e));
      })
      .finally(() => setBusy(false));
  };
  const groups = useMemo(() => {
    const g = new Map<string, { name: string; description?: string }[]>();
    for (const l of res?.labels ?? []) {
      const s = scopeOf(l.name);
      g.set(s, [...(g.get(s) ?? []), l]);
    }
    return [...g.entries()].sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)));
  }, [res]);
  const used = (res?.labels ?? []).filter((l) => labelUse(l.name, allow, suggestOnly)).length;
  if (!open) {
    return (
      <div>
        <Button type="button" variant="outline" size="sm" onClick={() => setOpen(true)}>
          Pick from a repository's labels
        </Button>
      </div>
    );
  }
  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Input className="max-w-md" list="label-picker-repos" placeholder={repo ? "group/project" : "gitlab:gitlab.com/group/project"} value={pick} onChange={(e) => setPick(e.target.value)} onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), load())} />
        <datalist id="label-picker-repos">
          {options.map((o) => (
            <option key={o} value={o} />
          ))}
        </datalist>
        <Button type="button" size="sm" onClick={load} disabled={busy}>
          {busy ? "Reading…" : "Show labels"}
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(false)}>
          Close
        </Button>
      </div>
      {err && <p className="text-xs text-destructive">{err}</p>}
      {res && !res.listable && (
        <Sub>Bitbucket pull requests have no labels: only exact names in Allowed labels are suggested, in the walkthrough.</Sub>
      )}
      {res?.listable && (
        <>
          <Sub>
            {res.labels.length} labels; the patterns use {used}. <Badge variant="default">set</Badge> allowed · <Badge variant="secondary">suggest</Badge> suggest only · click a label to allow it, or a scope to allow all of it.
          </Sub>
          <div className="grid max-h-72 gap-2 overflow-y-auto">
            {groups.map(([scope, ls]) => (
              <div key={scope || "-"} className="flex flex-wrap items-center gap-1.5">
                {scope ? (
                  <Button type="button" variant="ghost" size="sm" className="h-6 px-1.5 font-mono text-xs" title={`Allow ${scope}::*`} onClick={() => onAdd("allow", scope + "::*")}>
                    {scope}::*
                  </Button>
                ) : (
                  <span className="px-1.5 text-xs text-muted-foreground">unscoped</span>
                )}
                {ls.map((l) => {
                  const u = labelUse(l.name, allow, suggestOnly);
                  return (
                    <button type="button" key={l.name} title={l.description || "Allow " + l.name} onClick={() => !u && onAdd("allow", l.name)}>
                      <Badge variant={u === "set" ? "default" : u === "suggest" ? "secondary" : "outline"} className={u ? "" : "text-muted-foreground"}>
                        {l.name}
                      </Badge>
                    </button>
                  );
                })}
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
