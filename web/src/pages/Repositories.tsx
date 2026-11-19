import { Fragment, useEffect, useState } from "react";
import { del, get, post, put } from "../api";
import { ago } from "../format";
import type { Access, Overrides, Repo } from "../types";
import { Chips, Empty, errMsg, SettingsEditor, settingsChips, StateBadge, useMe, useToast } from "../ui";

function whyText(e: Access): string {
  if (e.source === "repository") return "set on this repository";
  if (e.source === "allowlist") return "no rule matches · allowlist default";
  if (e.source === "default") return "no rule matches · reviewed by default";
  return "by " + e.source;
}

export default function Repositories() {
  const me = useMe();
  const toast = useToast();
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
        toast("Added " + r.path);
      })
      .catch((e) => toast(errMsg(e), true));
  const remove = (r: Repo) => {
    if (!confirm("Remove " + r.path + "? It reappears (set to Inherit) if it sends another webhook.")) return;
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

  return (
    <section>
      <div className="bar">
        <input type="search" placeholder="Filter repositories" aria-label="Filter repositories" value={q} onChange={(e) => setQ(e.target.value)} />
        <select aria-label="Show" value={show} onChange={(e) => setShow(e.target.value)}>
          <option value="all">All</option>
          <option value="on">Reviewed</option>
          <option value="off">Not reviewed</option>
        </select>
        <span className="sub">{repos && on + " reviewed · " + (repos.length - on) + " not reviewed"}</span>
        <span className="spacer" />
        <button className="btn" onClick={() => setAdding(!adding)}>
          Add repository
        </button>
      </div>
      {adding && (
        <div className="panel pad">
          <div className="grid">
            <label className="f">
              Platform
              <select value={form.platform} onChange={(e) => setForm({ ...form, platform: e.target.value })}>
                <option value="github">GitHub</option>
                <option value="gitlab">GitLab</option>
              </select>
            </label>
            <label className="f">
              Host
              <input placeholder="github.com" value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} />
            </label>
            <label className="f">
              Repository path
              <input placeholder="owner/repo or group/sub/project" value={form.path} onChange={(e) => setForm({ ...form, path: e.target.value })} />
            </label>
            <label className="f">
              Reviews
              <select value={form.mode} onChange={(e) => setForm({ ...form, mode: e.target.value })}>
                <option value="enabled">Enabled</option>
                <option value="disabled">Disabled</option>
                <option value="inherit">Inherit from rules</option>
              </select>
            </label>
          </div>
          <button className="btn primary" onClick={add}>
            Add
          </button>
        </div>
      )}
      <div className="panel">
        {!repos ? (
          <Empty>Loading…</Empty>
        ) : !list.length ? (
          <Empty>{repos.length ? "No repositories match." : "No repositories yet. They appear when they send their first webhook, or add one above."}</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Reviews</th>
                <th>Repository</th>
                <th>Last webhook</th>
                <th>Effective settings</th>
                <th>Last changed</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((r) => {
                const e = r.effective;
                const flags = [e.require_reviewer && "reviewer required", e.skip_branch_sync && "skips branch syncs", e.score_labels && "score labels", e.commit_status && "commit status"].filter(Boolean);
                return (
                  <Fragment key={r.id}>
                    <tr>
                      <td>
                        <select aria-label={"Reviews for " + r.path} value={r.mode} onChange={(ev) => save(r, ev.target.value, r.settings || {}).catch((x) => toast(errMsg(x), true))}>
                          <option value="inherit">Inherit</option>
                          <option value="enabled">Enabled</option>
                          <option value="disabled">Disabled</option>
                        </select>
                        <div className="why">
                          <StateBadge kind={e.enabled ? "on" : "off"}>{e.enabled ? "Reviewed" : "Not reviewed"}</StateBadge>
                        </div>
                        <div className="why">{whyText(e)}</div>
                        <div className="why" title={`reviewer: ${e.require_reviewer_source} · branch syncs: ${e.skip_branch_sync_source} · labels: ${e.score_labels_source} · status: ${e.commit_status_source}`}>
                          {flags.join(" · ")}
                        </div>
                      </td>
                      <td>
                        <div>{r.path}</div>
                        <div className="plat">
                          {r.platform} · {r.host}
                        </div>
                      </td>
                      <td className="muted">{ago(r.last_seen)}</td>
                      <td>
                        <Chips items={settingsChips(e.settings)} />
                        {(e.rules || []).length > 0 && JSON.stringify(e.settings) !== JSON.stringify(r.settings || {}) && <div className="why">includes rule settings</div>}
                      </td>
                      <td className="muted">{r.updated_by ? r.updated_by + " · " + ago(r.updated_at) : "—"}</td>
                      <td>
                        <button className="btn" onClick={() => setEditing(editing === r.id ? null : r.id)}>
                          {editing === r.id ? "Close" : "Settings"}
                        </button>
                      </td>
                    </tr>
                    {editing === r.id && (
                      <tr className="editor">
                        <td colSpan={6}>
                          <SettingsEditor
                            value={r.settings}
                            onSave={(s) => save(r, r.mode, s).then(() => setEditing(null)).catch((x) => toast(errMsg(x), true))}
                            onCancel={() => setEditing(null)}
                            danger={
                              <button className="btn danger" onClick={() => remove(r)}>
                                Remove
                              </button>
                            }
                          />
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
        Repositories appear here automatically when they send their first webhook, set to <b>Inherit</b>: they follow the most specific group or pattern rule, and otherwise the default (
        {me.allowlist ? "not reviewed, because the allowlist is on" : "reviewed, because the allowlist is off"}). Choosing Enabled or Disabled here overrides every rule. Settings here override the rules' settings and the repository's own <code>.codereview.yaml</code>.
      </p>
      {me.require_reviewer && (
        <p className="sub">
          <b>Reviewer required:</b> even in an enabled repository, a merge request is reviewed only when the bot user is one of its reviewers. Adding the bot as a reviewer starts a review; <code>@bot review</code> comments still work.
        </p>
      )}
    </section>
  );
}
