import { useEffect, useState } from "react";
import { del, get, post, put } from "../api";
import { Empty, errMsg, StateBadge, useToast } from "../ui";

type Host = {
  id: number; platform: "github" | "gitlab" | "bitbucket"; host: string; base_url: string; name: string; enabled: boolean;
  settings: { app_id?: number; email?: string; require_signature?: boolean; skip_tls_verify?: boolean; bot_name?: string; reviewer?: string };
  secrets: { token: boolean; app_private_key: boolean; api_token: boolean; webhook_secrets: number; signing_tokens: number };
  webhook_url: string; events: string[];
};

const names = { github: "GitHub", gitlab: "GitLab", bitbucket: "Bitbucket" };
const defaults = { github: "https://api.github.com", gitlab: "https://gitlab.com", bitbucket: "https://api.bitbucket.org/2.0" };
const lines = (s: string) => s.split("\n").map((x) => x.trim()).filter(Boolean);

function Secret({ label, set, multi, list, help, value, onChange }: { label: string; set: boolean; multi?: boolean; list?: boolean; help?: string; value: string; onChange: (v: string) => void }) {
  const ph = set ? "Set ✓ — leave blank to keep" : list ? "one per line" : "";
  return (
    <label className="f">
      {label + (help ? " — " + help : "")}
      {multi ? (
        <textarea placeholder={ph} autoComplete="off" value={value} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <input type="password" placeholder={ph} autoComplete="new-password" value={value} onChange={(e) => onChange(e.target.value)} />
      )}
    </label>
  );
}

function HostForm({ host, onSaved, onCancel }: { host: Host | null; onSaved: (h: Host) => void; onCancel: () => void }) {
  const toast = useToast();
  const st = host?.settings ?? {};
  const sec = host?.secrets ?? { token: false, app_private_key: false, api_token: false, webhook_secrets: 0, signing_tokens: 0 };
  const [plat, setPlat] = useState<Host["platform"]>(host?.platform ?? "gitlab");
  const [v, setV] = useState({
    base: host?.base_url ?? "", name: host?.name ?? "", enabled: host ? host.enabled : true,
    token: "", appID: st.app_id ? String(st.app_id) : "", appKey: "", email: st.email ?? "", apiToken: "",
    secrets: "", signing: "", reqSig: !!st.require_signature, skipTLS: !!st.skip_tls_verify, bot: st.bot_name ?? "", reviewer: st.reviewer ?? "",
  });
  const set = (k: keyof typeof v) => (x: any) => setV({ ...v, [k]: typeof x === "object" && x?.target ? x.target.value : x });
  const save = () => {
    const body: any = { platform: plat, base_url: v.base.trim(), name: v.name.trim(), enabled: v.enabled, settings: { bot_name: v.bot.trim(), reviewer: v.reviewer.trim() } };
    if (plat === "github") body.settings.app_id = parseInt(v.appID || "0", 10);
    if (plat === "gitlab") Object.assign(body.settings, { skip_tls_verify: v.skipTLS, require_signature: v.reqSig });
    if (plat === "bitbucket") body.settings.email = v.email.trim();
    if (v.token.trim()) body.token = v.token.trim();
    if (v.appKey.trim()) body.app_private_key = v.appKey;
    if (v.apiToken.trim()) body.api_token = v.apiToken.trim();
    if (lines(v.secrets).length) body.webhook_secrets = lines(v.secrets); // blank keeps the current values
    if (lines(v.signing).length) body.signing_tokens = lines(v.signing);
    (host ? put<Host>("/admin/api/hosts/" + host.id, body) : post<Host>("/admin/api/hosts", body))
      .then((h) => {
        toast("Saved " + h.name);
        onSaved(h);
      })
      .catch((e) => toast(errMsg(e), true));
  };
  return (
    <div className="panel pad">
      <div className="fsec">{host ? "Edit " + host.name : "New host"}</div>
      <div className="hostgrid">
        <label className="f">
          Platform
          <select id="hPlat" value={plat} disabled={!!host} onChange={(e) => setPlat(e.target.value as Host["platform"])}>
            <option value="gitlab">GitLab</option>
            <option value="github">GitHub</option>
            <option value="bitbucket">Bitbucket Cloud</option>
          </select>
        </label>
        <label className="f">
          {plat === "gitlab" ? "Instance URL" : "API URL"}
          <input value={v.base} placeholder={defaults[plat]} onChange={set("base")} />
        </label>
        <label className="f">
          Name
          <input value={v.name} placeholder="shown in the console" onChange={set("name")} />
        </label>
        <label className="f">
          Status
          <select value={String(v.enabled)} onChange={(e) => setV({ ...v, enabled: e.target.value === "true" })}>
            <option value="true">Enabled</option>
            <option value="false">Disabled</option>
          </select>
        </label>
      </div>
      <div className="fsec">API access</div>
      <div className="hostgrid">
        {plat === "github" && (
          <>
            <Secret label="Token" help="or use a GitHub App below" set={sec.token} value={v.token} onChange={set("token")} />
            <label className="f">
              GitHub App ID
              <input type="number" value={v.appID} onChange={set("appID")} />
            </label>
            <Secret label="GitHub App private key (PEM)" set={sec.app_private_key} multi value={v.appKey} onChange={set("appKey")} />
          </>
        )}
        {plat === "gitlab" && (
          <>
            <Secret label="Access token (api scope)" set={sec.token} value={v.token} onChange={set("token")} />
            <label className="f">
              TLS
              <select value={String(v.skipTLS)} onChange={(e) => setV({ ...v, skipTLS: e.target.value === "true" })}>
                <option value="false">Verify certificates</option>
                <option value="true">Skip verification (self-signed)</option>
              </select>
            </label>
          </>
        )}
        {plat === "bitbucket" && (
          <>
            <Secret label="Access token" help="repository or workspace token" set={sec.token} value={v.token} onChange={set("token")} />
            <label className="f">
              Account email
              <input value={v.email} placeholder="with an API token instead" onChange={set("email")} />
            </label>
            <Secret label="Atlassian API token" set={sec.api_token} value={v.apiToken} onChange={set("apiToken")} />
          </>
        )}
      </div>
      <div className="fsec">Webhooks</div>
      <div className="hostgrid">
        {plat === "gitlab" ? (
          <>
            <Secret label="Signing tokens (whsec_…)" multi list set={sec.signing_tokens > 0} help={sec.signing_tokens ? sec.signing_tokens + " set" : "GitLab 19.0+, recommended"} value={v.signing} onChange={set("signing")} />
            <Secret label="Secret tokens" multi list set={sec.webhook_secrets > 0} help={sec.webhook_secrets ? sec.webhook_secrets + " set" : "older hooks"} value={v.secrets} onChange={set("secrets")} />
            <label className="f">
              Unsigned webhooks
              <select value={String(v.reqSig)} onChange={(e) => setV({ ...v, reqSig: e.target.value === "true" })}>
                <option value="false">Accept with a secret token</option>
                <option value="true">Reject</option>
              </select>
            </label>
          </>
        ) : (
          <Secret label="Webhook secrets" multi list set={sec.webhook_secrets > 0} help={sec.webhook_secrets ? sec.webhook_secrets + " set" : "one per hook"} value={v.secrets} onChange={set("secrets")} />
        )}
      </div>
      <div className="fsec">Bot identity</div>
      <div className="hostgrid">
        <label className="f">
          Mention name
          <input value={v.bot} placeholder="default: the token's user" onChange={set("bot")} />
        </label>
        <label className="f">
          Reviewer username
          <input value={v.reviewer} placeholder="default: the token's user" onChange={set("reviewer")} />
        </label>
      </div>
      <div className="bar mt">
        <button className="btn primary" onClick={save}>
          {host ? "Save host" : "Add host"}
        </button>
        <button className="btn" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </div>
  );
}

export default function Hosts() {
  const toast = useToast();
  const [data, setData] = useState<{ mode: string; key_env?: string; hosts: Host[] } | null>(null);
  const [editing, setEditing] = useState<number | null>(null); // 0 = new
  const [tests, setTests] = useState<Record<number, { pending?: boolean; ok?: boolean; message?: string }>>({});
  const load = () => get<{ mode: string; key_env?: string; hosts: Host[] }>("/admin/api/hosts").then(setData).catch((e) => toast(errMsg(e), true));
  useEffect(() => {
    load();
  }, []);
  const test = (h: Host) => {
    setTests((t) => ({ ...t, [h.id]: { pending: true } }));
    post<{ ok: boolean; message: string }>(`/admin/api/hosts/${h.id}/test`)
      .then((r) => setTests((t) => ({ ...t, [h.id]: r })))
      .catch((e) => setTests((t) => ({ ...t, [h.id]: { ok: false, message: errMsg(e) } })));
  };
  const remove = (h: Host) => {
    if (!confirm("Delete " + h.name + "? Webhooks from it will be rejected and its stored tokens removed.")) return;
    del("/admin/api/hosts/" + h.id)
      .then(() => {
        toast("Deleted " + h.name);
        load();
      })
      .catch((e) => toast(errMsg(e), true));
  };
  const managed = data?.mode === "database";
  const editHost = editing ? data?.hosts.find((h) => h.id === editing) ?? null : null;
  return (
    <section>
      <div className="bar">
        <span className="sub">{data && (managed ? "Managed here; stored encrypted in the database." : `Hosts come from the config file (read-only). Set ${data.key_env || "CODEREVIEW_CONSOLE_SECRET_KEY"} to manage them here.`)}</span>
        <span className="spacer" />
        {managed && (
          <button className="btn" onClick={() => setEditing(0)}>
            Add host
          </button>
        )}
      </div>
      {editing !== null && (
        <HostForm key={editing} host={editHost} onCancel={() => setEditing(null)} onSaved={(h) => { setEditing(null); load().then(() => test(h)); }} />
      )}
      <div className="panel">
        {!data ? (
          <Empty>Loading…</Empty>
        ) : !data.hosts.length ? (
          <Empty>No code hosts yet. Add GitHub, GitLab or Bitbucket.</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Host</th>
                <th>Credentials</th>
                <th>Webhook URL</th>
                <th>Check</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.hosts.map((h) => {
                const s = h.secrets;
                const auth = [s.token && "token ✓", h.settings.app_id && `App ${h.settings.app_id}${s.app_private_key ? " ✓" : " (no key)"}`, s.api_token && "API token ✓",
                  s.signing_tokens && `${s.signing_tokens} signing token${s.signing_tokens > 1 ? "s" : ""}`, s.webhook_secrets && `${s.webhook_secrets} webhook secret${s.webhook_secrets > 1 ? "s" : ""}`].filter(Boolean) as string[];
                const t = tests[h.id];
                return (
                  <tr key={h.id}>
                    <td>
                      <div>
                        <b>{h.name}</b>
                      </div>
                      <div className="plat">
                        {names[h.platform]} · {h.host}
                      </div>
                      <div>
                        <StateBadge kind={h.enabled ? "on" : "idle"}>{h.enabled ? "Enabled" : "Disabled"}</StateBadge>
                      </div>
                    </td>
                    <td>
                      <span>
                        {auth.map((a) => (
                          <span className="chip" key={a}>
                            {a}
                          </span>
                        ))}
                      </span>
                      <div className="why">
                        mention: {h.settings.bot_name ? "@" + h.settings.bot_name : "token user"}
                        {h.settings.reviewer ? " · reviewer: " + h.settings.reviewer : ""}
                      </div>
                    </td>
                    <td>
                      <code className="hosturl">{h.webhook_url}</code>
                      <div className="why">Events: {h.events.join(", ")}</div>
                    </td>
                    <td>{t && (t.pending ? <span className="muted">testing…</span> : <StateBadge kind={t.ok ? "on" : "off"}>{t.message}</StateBadge>)}</td>
                    <td className="nowrap">
                      <button className="btn" onClick={() => test(h)}>
                        Test
                      </button>{" "}
                      {managed && (
                        <>
                          <button className="btn" onClick={() => { setEditing(h.id); window.scrollTo(0, 0); }}>
                            Edit
                          </button>{" "}
                          <button className="btn danger" onClick={() => remove(h)}>
                            Delete
                          </button>
                        </>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
      <p className="sub">
        Each host is a GitHub (github.com or Enterprise), GitLab (gitlab.com or self-managed) or Bitbucket Cloud account the console reviews for. Tokens and webhook secrets are encrypted with <code>CODEREVIEW_CONSOLE_SECRET_KEY</code> before they are stored and are never shown again: leave a secret blank to keep it. Point each code host's webhook at the URL shown, with the events listed. Several hooks on one host (a GitLab system hook plus group hooks) can each have their own secret: add one per line.
      </p>
    </section>
  );
}
