import { Fragment, useEffect, useState } from "react";
import { del, get, post, put } from "../api";
import { ago } from "../format";
import type { Repo, Rule } from "../types";
import { Chips, Empty, errMsg, SettingsEditor, settingsChips, Tri, useToast } from "../ui";

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
    <SettingsEditor value={rule.settings} inheritModel="Inherit" onSave={(s) => onSave({ ...vals, settings: s })}
      danger={<button className="btn danger" onClick={onDelete}>Delete rule</button>}
      extra={
        <>
          <div className="sub">Settings for this level</div>
          <div className="grid">
            {flags.map(([k, name, on, off]) => (
              <label className="f" key={k}>
                {name}
                <Tri value={vals[k]} on={on} off={off} onChange={(v) => setVals({ ...vals, [k]: v })} />
              </label>
            ))}
          </div>
          <div className="sub mt">Review settings</div>
        </>
      }
    />
  );
}

export default function Rules() {
  const toast = useToast();
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
        setEditing(r.id);
        toast("Saved rule " + label(r) + " — set its options below");
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
  const remove = (rule: Rule) => {
    if (!confirm("Delete the rule for " + label(rule) + " on " + rule.host + "? Repositories fall back to the level above.")) return;
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

  return (
    <section>
      <div className="bar">
        <span className="sub">Settings for a whole host, or for a group, organisation or name pattern on a host.</span>
        <span className="spacer" />
        <button className="btn" onClick={() => setAdding(!adding)}>
          Add rule
        </button>
      </div>
      {adding && (
        <div className="panel pad">
          <div className="grid">
            <label className="f">
              Platform
              <select value={form.platform} onChange={(e) => setForm({ ...form, platform: e.target.value })}>
                <option value="gitlab">GitLab</option>
                <option value="github">GitHub</option>
              </select>
            </label>
            <label className="f">
              Host
              <input placeholder="gitlab.com" value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} />
            </label>
            <label className="f">
              Group, owner or pattern
              <input placeholder="empty = whole host · acme · acme/svc-*" value={form.pattern} onChange={(e) => setForm({ ...form, pattern: e.target.value })} />
            </label>
            <label className="f">
              Reviews
              <Tri value={form.enabled} on="Enabled" off="Disabled" onChange={(v) => setForm({ ...form, enabled: v })} />
            </label>
          </div>
          <button className="btn primary" onClick={add}>
            Add rule
          </button>
        </div>
      )}
      <div className="panel">
        {!rules ? (
          <Empty>Loading…</Empty>
        ) : !rules.length ? (
          <Empty>No rules yet. Add one for a whole host, a group or a name pattern.</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Scope</th>
                <th>Sets</th>
                <th>Covers</th>
                <th>Last changed</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {sorted.map((rule) => {
                const n = covers(rule);
                const set = flags.filter(([k]) => rule[k] !== null && rule[k] !== undefined).map(([k, name, on, off]) => name + ": " + (rule[k] ? on : off));
                return (
                  <Fragment key={rule.id}>
                    <tr>
                      <td>
                        <div>{rule.pattern ? <code>{rule.pattern}</code> : <b>Whole host</b>}</div>
                        <div className="plat">
                          {rule.platform} · {rule.host}
                        </div>
                      </td>
                      <td>
                        <Chips items={set} empty="inherits everything" />
                        <div className="why">
                          <Chips items={settingsChips(rule.settings)} />
                        </div>
                      </td>
                      <td className="muted">{n + (n === 1 ? " known repository" : " known repositories")}</td>
                      <td className="muted">{rule.updated_by ? rule.updated_by + " · " + ago(rule.updated_at) : "—"}</td>
                      <td>
                        <button className="btn" onClick={() => setEditing(editing === rule.id ? null : rule.id)}>
                          {editing === rule.id ? "Close" : "Edit"}
                        </button>
                      </td>
                    </tr>
                    {editing === rule.id && (
                      <tr className="editor">
                        <td colSpan={5}>
                          <RuleEditor rule={rule} onSave={(b) => save(rule, b)} onDelete={() => remove(rule)} />
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
      <p className="sub">
        Levels, broadest first: <b>Settings</b> (everything) → <b>whole host</b> (empty pattern) → <b>group or owner</b> (<code>acme</code>, <code>acme/platform</code>, subgroups included) or <b>pattern</b> (<code>acme/svc-*</code>; <code>*</code> stays within one level) → <b>repository</b>. For each setting the most specific level that sets it wins; <b>Inherit</b> means "use the level above". Review settings add up the same way. Hosts are separate: a rule for <code>acme</code> on one host never touches <code>acme</code> on another.
      </p>
    </section>
  );
}
