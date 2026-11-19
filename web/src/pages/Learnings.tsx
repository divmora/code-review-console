import { Fragment, useEffect, useState } from "react";
import { del, get, post, put } from "../api";
import { ago } from "../format";
import { Empty, errMsg, Locked, useFeature, useToast } from "../ui";

type Learning = {
  id: number; platform: string; host: string; scope: string; text: string; path: string;
  status: "active" | "pending" | "disabled"; source: string; source_url: string; source_author: string; updated_at: string;
};

const sourceLabel: Record<string, string> = { reply: "from a reply", command: "@bot remember", manual: "added here" };

function Editor({ l, onSave, onCancel, onDelete }: { l: Learning; onSave: (b: Partial<Learning>) => void; onCancel: () => void; onDelete: () => void }) {
  const [scope, setScope] = useState(l.scope);
  const [path, setPath] = useState(l.path);
  const [text, setText] = useState(l.text);
  return (
    <div>
      <div className="grid">
        <label className="f">
          Applies to
          <input value={scope} placeholder="empty = whole host · group · group/repo" onChange={(e) => setScope(e.target.value)} />
        </label>
        <label className="f">
          Only files matching
          <input value={path} placeholder="optional glob" onChange={(e) => setPath(e.target.value)} />
        </label>
      </div>
      <label className="f">
        Learning
        <textarea maxLength={600} value={text} onChange={(e) => setText(e.target.value)} />
      </label>
      <div className="bar mt">
        <button className="btn primary" onClick={() => onSave({ scope: scope.trim(), path: path.trim(), text: text.trim() })}>
          Save
        </button>
        <button className="btn" onClick={onCancel}>
          Cancel
        </button>
        <span className="spacer" />
        <button className="btn danger" onClick={onDelete}>
          Delete
        </button>
      </div>
    </div>
  );
}

export default function Learnings() {
  const toast = useToast();
  const licensed = useFeature("learnings");
  const [list, setList] = useState<Learning[] | null>(null);
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [editing, setEditing] = useState<number | null>(null);
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ platform: "gitlab", host: "", scope: "", path: "", text: "" });

  useEffect(() => {
    get<{ learnings: Learning[] }>("/admin/api/learnings").then((d) => setList(d.learnings || [])).catch((e) => toast(errMsg(e), true));
  }, []);
  const save = (l: Learning, patch: Partial<Learning>) =>
    put<Learning>("/admin/api/learnings/" + l.id, { scope: l.scope, text: l.text, path: l.path, status: l.status, ...patch }).then((x) => {
      setList((ls) => (ls || []).map((y) => (y.id === x.id ? x : y)));
      return x;
    });
  const add = () =>
    post<Learning>("/admin/api/learnings", form)
      .then((l) => {
        setList((ls) => (ls || []).concat([l]));
        setForm({ ...form, text: "" });
        setAdding(false);
        toast("Learning added");
      })
      .catch((e) => toast(errMsg(e), true));
  const remove = (l: Learning) => {
    if (!confirm("Delete this learning?")) return;
    del("/admin/api/learnings/" + l.id)
      .then(() => {
        setList((ls) => (ls || []).filter((x) => x.id !== l.id));
        setEditing(null);
        toast("Deleted");
      })
      .catch((e) => toast(errMsg(e), true));
  };

  const counts = { active: 0, pending: 0, disabled: 0 };
  (list || []).forEach((l) => (counts[l.status] += 1));
  const shown = (list || []).filter((l) => (!status || l.status === status) && (!q || (l.text + " " + l.scope + " " + l.host + " " + l.path).toLowerCase().includes(q.toLowerCase())));

  return (
    <section>
      {!licensed && (
        <Locked feature="learnings">
          Capturing team decisions from review replies, <code>@bot remember</code>, and applying them to reviews are part of the licensed console. Without a license nothing new is learned and existing learnings are not applied.
        </Locked>
      )}
      <div className="bar">
        <input type="search" placeholder="Search learnings" aria-label="Search learnings" value={q} onChange={(e) => setQ(e.target.value)} />
        <select aria-label="Status" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">Any status</option>
          <option value="active">Active</option>
          <option value="pending">Waiting for approval</option>
          <option value="disabled">Disabled</option>
        </select>
        <span className="sub">{list && `${counts.active} active · ${counts.pending} waiting · ${counts.disabled} disabled`}</span>
        <span className="spacer" />
        {licensed && (
          <button className="btn" onClick={() => setAdding(!adding)}>
            Add learning
          </button>
        )}
      </div>
      {adding && (
        <div className="panel pad">
          <div className="grid">
            <label className="f">
              Platform
              <select value={form.platform} onChange={(e) => setForm({ ...form, platform: e.target.value })}>
                <option value="gitlab">GitLab</option>
                <option value="github">GitHub</option>
                <option value="bitbucket">Bitbucket</option>
              </select>
            </label>
            <label className="f">
              Host
              <input placeholder="gitlab.example.com" value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} />
            </label>
            <label className="f">
              Applies to
              <input placeholder="empty = whole host · group · group/repo · acme/svc-*" value={form.scope} onChange={(e) => setForm({ ...form, scope: e.target.value })} />
            </label>
            <label className="f">
              Only files matching
              <input placeholder="optional glob, e.g. api/legacy/**" value={form.path} onChange={(e) => setForm({ ...form, path: e.target.value })} />
            </label>
          </div>
          <label className="f">
            Learning
            <textarea maxLength={600} placeholder="A rule for future reviews, e.g. Account numbers are compared without leading zeros on purpose." value={form.text} onChange={(e) => setForm({ ...form, text: e.target.value })} />
          </label>
          <div className="bar mt">
            <button className="btn primary" onClick={add}>
              Add learning
            </button>
            <button className="btn" onClick={() => setAdding(false)}>
              Cancel
            </button>
          </div>
        </div>
      )}
      <div className="panel">
        {!list ? (
          <Empty>Loading…</Empty>
        ) : !shown.length ? (
          <Empty>{list.length ? "No learnings match." : "No learnings yet. Reply to one of the bot's comments with a team decision, or use @bot remember."}</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Applies to</th>
                <th>Learning</th>
                <th>Status</th>
                <th>Source</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {shown.map((l) => (
                <Fragment key={l.id}>
                  <tr>
                    <td>
                      <div>{l.scope ? <code>{l.scope}</code> : <b>Whole host</b>}</div>
                      <div className="plat">
                        {l.platform} · {l.host}
                      </div>
                    </td>
                    <td>
                      <div>{l.text}</div>
                      {l.path && (
                        <div className="why">
                          <span className="chip">files: {l.path}</span>
                        </div>
                      )}
                    </td>
                    <td>
                      <select aria-label="Status" value={l.status} disabled={!licensed}
                        onChange={(e) => save(l, { status: e.target.value as Learning["status"] }).then((x) => toast("Learning " + x.status)).catch((x) => toast(errMsg(x), true))}>
                        <option value="active">Active</option>
                        <option value="pending">Waiting</option>
                        <option value="disabled">Disabled</option>
                      </select>
                    </td>
                    <td className="muted">
                      <div>{(sourceLabel[l.source] || l.source) + (l.source_author ? " · " + l.source_author : "")}</div>
                      {l.source_url && (
                        <a className="why" href={l.source_url} target="_blank" rel="noopener noreferrer">
                          open PR
                        </a>
                      )}
                      <div className="why">{ago(l.updated_at)}</div>
                    </td>
                    <td className="nowrap">
                      {licensed ? (
                        <button className="btn" onClick={() => setEditing(editing === l.id ? null : l.id)}>
                          {editing === l.id ? "Close" : "Edit"}
                        </button>
                      ) : (
                        <button className="btn danger" onClick={() => remove(l)}>
                          Delete
                        </button>
                      )}
                    </td>
                  </tr>
                  {editing === l.id && (
                    <tr className="editor">
                      <td colSpan={5}>
                        <Editor l={l} onCancel={() => setEditing(null)} onDelete={() => remove(l)}
                          onSave={(p) => save(l, p).then(() => { setEditing(null); toast("Saved"); }).catch((x) => toast(errMsg(x), true))} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
        )}
      </div>
      <p className="sub">
        Learnings are team decisions from review discussions. When someone replies to one of the bot's comments, the bot reacts 👀, asks the model whether the reply states a rule for future reviews, and if so saves it here (✅ on GitLab, 🎉 on GitHub). <code>@bot remember &lt;rule&gt;</code> saves one directly. Each starts scoped to its repository; widen <b>Applies to</b> to a group or the whole host so one standard covers many repositories. Matching active learnings go with every review: the reviewer follows them and the verifier drops findings they rule out.
      </p>
    </section>
  );
}
