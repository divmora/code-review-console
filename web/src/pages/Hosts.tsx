import { useEffect, useState } from "react";
import { Check, Copy, Plug, Plus, Webhook } from "lucide-react";
import { del, get, post, put } from "../api";
import { Empty, errMsg, Field, FormGrid, LockNote, NativeSelect, Note, Panel, SidePanel, StateBadge, Sub, Toolbar, useConfirm, useFeature, useToast } from "../ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";

type Host = {
  id: number; platform: "github" | "gitlab" | "bitbucket"; host: string; base_url: string; name: string; enabled: boolean;
  settings: { app_id?: number; email?: string; require_signature?: boolean; skip_tls_verify?: boolean; bot_name?: string; reviewer?: string };
  secrets: { token: boolean; app_private_key: boolean; api_token: boolean; webhook_secrets: number; signing_tokens: number };
  webhook_url: string; webhooks: Hook[]; events: string[]; licensed: boolean; comment_warning?: string;
};

type Hook = {
  id: string; host_id: number; name: string; enabled: boolean; url: string;
  secrets: { webhook_secret: boolean; signing_tokens: number }; created_at: string; created_by: string;
};

const names = { github: "GitHub", gitlab: "GitLab", bitbucket: "Bitbucket" };
const defaults = { github: "https://api.github.com", gitlab: "https://gitlab.com", bitbucket: "https://api.bitbucket.org/2.0" };
const lines = (s: string) => s.split("\n").map((x) => x.trim()).filter(Boolean);

function Secret({ label, set, multi, list, help, value, onChange }: { label: string; set: boolean; multi?: boolean; list?: boolean; help?: string; value: string; onChange: (v: string) => void }) {
  const ph = set ? "Set ✓ — leave blank to keep" : list ? "one per line" : "";
  return (
    <Field label={label + (help ? " — " + help : "")}>
      {multi ? (
        <Textarea rows={3} className="font-mono text-xs" placeholder={ph} autoComplete="off" spellCheck={false} value={value} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <Input type="password" placeholder={ph} autoComplete="new-password" value={value} onChange={(e) => onChange(e.target.value)} />
      )}
    </Field>
  );
}

function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const toast = useToast();
  const [done, setDone] = useState(false);
  const copy = () =>
    navigator.clipboard
      .writeText(text)
      .then(() => {
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      })
      .catch(() => toast("Copy failed: select the text and copy it", true));
  return (
    <Button variant="ghost" size="sm" onClick={copy} aria-label={label}>
      {done ? <Check /> : <Copy />} {label}
    </Button>
  );
}

// SecretOnce shows a newly generated secret, which is never shown again.
function SecretOnce({ secret, platform, onDone }: { secret: string; platform: Host["platform"]; onDone: () => void }) {
  const where = platform === "gitlab" ? "GitLab's Secret token field" : platform === "github" ? "the webhook's Secret field" : "the webhook's Secret field";
  return (
    <div className="grid gap-2 rounded-lg border border-warning/40 bg-warning-soft p-3">
      <div className="text-sm font-medium">Copy the secret now: it is not shown again.</div>
      <div className="flex flex-wrap items-center gap-2">
        <code className="min-w-0 flex-1 break-all font-mono text-xs">{secret}</code>
        <CopyButton text={secret} />
      </div>
      <Sub>Paste it into {where}, together with the URL.</Sub>
      <div>
        <Button size="sm" variant="outline" onClick={onDone}>
          I've copied it
        </Button>
      </div>
    </div>
  );
}

function HookRow({ hook, platform, onChanged }: { hook: Hook; platform: Host["platform"]; onChanged: () => void }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [secret, setSecret] = useState("");
  const [name, setName] = useState(hook.name);
  const [signing, setSigning] = useState("");
  const update = (body: any, msg: string) =>
    put<{ webhook: Hook; secret?: string }>("/admin/api/webhooks/" + hook.id, body)
      .then((r) => {
        toast(msg);
        if (r.secret) setSecret(r.secret);
        onChanged();
      })
      .catch((e) => toast(errMsg(e), true));
  const regenerate = async () => {
    if (!(await confirm({ title: "Replace the secret of " + hook.name + "?", description: "Deliveries signed with the old secret are rejected until you paste the new one into the code host.", action: "Replace secret", danger: true }))) return;
    update({ regenerate_secret: true }, "New secret for " + hook.name);
  };
  const remove = async () => {
    if (!(await confirm({ title: "Delete " + hook.name + "?", description: "Its URL stops working at once. Remove the webhook from the code host too.", action: "Delete webhook", danger: true }))) return;
    del("/admin/api/webhooks/" + hook.id)
      .then(() => {
        toast("Deleted " + hook.name);
        onChanged();
      })
      .catch((e) => toast(errMsg(e), true));
  };
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input className="h-8 max-w-64" value={name} onChange={(e) => setName(e.target.value)} onBlur={() => name.trim() !== hook.name && update({ name }, "Renamed")} />
        <StateBadge kind={hook.enabled ? "on" : "idle"}>{hook.enabled ? "Enabled" : "Disabled"}</StateBadge>
        {platform === "gitlab" && hook.secrets.signing_tokens > 0 && <Badge variant="outline" className="font-normal text-muted-foreground">signing token ✓</Badge>}
        <span className="flex-1" />
        <Button variant="ghost" size="sm" onClick={() => update({ enabled: !hook.enabled }, hook.enabled ? "Disabled " + hook.name : "Enabled " + hook.name)}>
          {hook.enabled ? "Disable" : "Enable"}
        </Button>
        <Button variant="ghost" size="sm" onClick={regenerate}>
          New secret
        </Button>
        <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={remove}>
          Delete
        </Button>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <code className="hosturl min-w-0 flex-1 break-all">{hook.url}</code>
        <CopyButton text={hook.url} label="Copy URL" />
      </div>
      {secret && <SecretOnce secret={secret} platform={platform} onDone={() => setSecret("")} />}
      {platform === "gitlab" && (
        <Field label={"Signing token (whsec_…) — GitLab 19.0+, " + (hook.secrets.signing_tokens ? "set ✓, paste to replace" : "optional: copy it from the webhook in GitLab")}>
          <div className="flex gap-2">
            <Input type="password" autoComplete="off" value={signing} placeholder={hook.secrets.signing_tokens ? "Set ✓" : ""} onChange={(e) => setSigning(e.target.value)} />
            <Button variant="outline" size="sm" disabled={!signing.trim()} onClick={() => update({ signing_tokens: [signing.trim()] }, "Saved signing token").then(() => setSigning(""))}>
              Save
            </Button>
            {hook.secrets.signing_tokens > 0 && (
              <Button variant="ghost" size="sm" onClick={() => update({ signing_tokens: [] }, "Removed signing token")}>
                Remove
              </Button>
            )}
          </div>
        </Field>
      )}
      <Sub>
        Added {new Date(hook.created_at).toLocaleDateString()}
        {hook.created_by ? " by " + hook.created_by : ""}
      </Sub>
    </div>
  );
}

function CommentWarning({ text }: { text: string }) {
  return (
    <div role="status" className="rounded-lg border border-warning/40 bg-warning-soft p-3 text-sm text-warning">
      {text}
    </div>
  );
}

function HostWebhooks({ host, managed, onChanged }: { host: Host; managed: boolean; onChanged: () => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [created, setCreated] = useState<{ id: string; secret: string } | null>(null);
  const add = () =>
    post<{ webhook: Hook; secret: string }>(`/admin/api/hosts/${host.id}/webhooks`, { name: name.trim() })
      .then((r) => {
        setName("");
        setCreated({ id: r.webhook.id, secret: r.secret });
        toast("Added " + r.webhook.name);
        onChanged();
      })
      .catch((e) => toast(errMsg(e), true));
  const shared = host.secrets.webhook_secrets > 0 || host.secrets.signing_tokens > 0;
  return (
    <div className="grid gap-6">
      {host.comment_warning && <CommentWarning text={host.comment_warning} />}
      {managed && (
        <Section title="Add a webhook">
          <div className="flex flex-wrap gap-2">
            <Input className="max-w-80" value={name} placeholder={host.platform === "gitlab" ? "e.g. the group it's for" : "e.g. where it's set up"} onChange={(e) => setName(e.target.value)} />
            <Button onClick={add}>
              <Plus /> Add webhook
            </Button>
          </div>
          <Sub>
            The console generates a URL and a secret. Set the code host's webhook to them, with these events: {host.events.join(", ")}.
            {host.platform === "gitlab" && " A system hook alone isn't enough: it doesn't send comments, so @bot commands need a group or project webhook with Comments."}
          </Sub>
        </Section>
      )}
      <Section title={`Webhooks (${host.webhooks.length})`}>
        {!host.webhooks.length ? (
          <Sub>No webhooks yet{managed ? ": add one above." : "."}</Sub>
        ) : (
          host.webhooks.map((w) => (
            <div key={w.id} className="grid gap-2">
              {created?.id === w.id && <SecretOnce secret={created.secret} platform={host.platform} onDone={() => setCreated(null)} />}
              <HookRow hook={w} platform={host.platform} onChanged={onChanged} />
            </div>
          ))
        )}
      </Section>
      <Separator />
      <Section title="Shared URL">
        <div className="flex flex-wrap items-center gap-2">
          <code className="hosturl min-w-0 flex-1 break-all">{host.webhook_url}</code>
          <CopyButton text={host.webhook_url} label="Copy URL" />
        </div>
        <Sub>
          {shared
            ? "Still accepted for existing hooks. It is shared by every " + names[host.platform] + " host and checked with the secrets in the host's settings. Move hooks to their own URL above when you can."
            : "Shared by every " + names[host.platform] + " host. This host has no secrets of its own for it, so only the webhooks above work."}
        </Sub>
      </Section>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-3">
      <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">{title}</h3>
      {children}
    </div>
  );
}

function HostForm({ host, onSaved, onCancel }: { host: Host | null; onSaved: (h: Host) => void; onCancel: () => void }) {
  const toast = useToast();
  const identity = useFeature("hosts");
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
    <div className="grid gap-6">
      <Section title="Host">
        <FormGrid className="lg:grid-cols-2">
          <Field label="Platform">
            <NativeSelect id="hPlat" value={plat} disabled={!!host} onChange={(e) => setPlat(e.target.value as Host["platform"])}>
              <option value="gitlab">GitLab</option>
              <option value="github">GitHub</option>
              <option value="bitbucket">Bitbucket Cloud</option>
            </NativeSelect>
          </Field>
          <Field label="Status">
            <NativeSelect value={String(v.enabled)} onChange={(e) => setV({ ...v, enabled: e.target.value === "true" })}>
              <option value="true">Enabled</option>
              <option value="false">Disabled</option>
            </NativeSelect>
          </Field>
          <Field label={plat === "gitlab" ? "Instance URL" : "API URL"}>
            <Input value={v.base} placeholder={defaults[plat]} onChange={set("base")} />
          </Field>
          <Field label="Name">
            <Input value={v.name} placeholder="shown in the console" onChange={set("name")} />
          </Field>
        </FormGrid>
      </Section>
      <Separator />
      <Section title="API access">
        <div className="grid gap-4">
          {plat === "github" && (
            <>
              <Secret label="Token" help="or use a GitHub App below" set={sec.token} value={v.token} onChange={set("token")} />
              <Field label="GitHub App ID">
                <Input type="number" value={v.appID} onChange={set("appID")} />
              </Field>
              <Secret label="GitHub App private key (PEM)" set={sec.app_private_key} multi value={v.appKey} onChange={set("appKey")} />
            </>
          )}
          {plat === "gitlab" && (
            <>
              <Secret label="Access token (api scope)" set={sec.token} value={v.token} onChange={set("token")} />
              <Field label="TLS">
                <NativeSelect value={String(v.skipTLS)} onChange={(e) => setV({ ...v, skipTLS: e.target.value === "true" })}>
                  <option value="false">Verify certificates</option>
                  <option value="true">Skip verification (self-signed)</option>
                </NativeSelect>
              </Field>
            </>
          )}
          {plat === "bitbucket" && (
            <>
              <Secret label="Access token" help="repository or workspace token" set={sec.token} value={v.token} onChange={set("token")} />
              <Field label="Account email">
                <Input value={v.email} placeholder="with an API token instead" onChange={set("email")} />
              </Field>
              <Secret label="Atlassian API token" set={sec.api_token} value={v.apiToken} onChange={set("apiToken")} />
            </>
          )}
        </div>
      </Section>
      <Separator />
      <Section title="Shared URL secrets (optional)">
        <Sub>Only for hooks on the shared {"/webhooks/" + plat} URL. Webhooks added under Webhooks have their own URL and secret.</Sub>
        <div className="grid gap-4">
          {plat === "gitlab" ? (
            <>
              <Secret label="Signing tokens (whsec_…)" multi list set={sec.signing_tokens > 0} help={sec.signing_tokens ? sec.signing_tokens + " set" : "GitLab 19.0+, recommended"} value={v.signing} onChange={set("signing")} />
              <Secret label="Secret tokens" multi list set={sec.webhook_secrets > 0} help={sec.webhook_secrets ? sec.webhook_secrets + " set" : "older hooks"} value={v.secrets} onChange={set("secrets")} />
              <Field label="Unsigned webhooks">
                <NativeSelect value={String(v.reqSig)} onChange={(e) => setV({ ...v, reqSig: e.target.value === "true" })}>
                  <option value="false">Accept with a secret token</option>
                  <option value="true">Reject</option>
                </NativeSelect>
              </Field>
            </>
          ) : (
            <Secret label="Webhook secrets" multi list set={sec.webhook_secrets > 0} help={sec.webhook_secrets ? sec.webhook_secrets + " set" : "one per hook"} value={v.secrets} onChange={set("secrets")} />
          )}
        </div>
      </Section>
      <Separator />
      <Section title="Bot identity">
        <FormGrid className="lg:grid-cols-2">
          <Field label="Mention name" hint={<LockNote feature="hosts" />}>
            <Input value={v.bot} placeholder="default: the token's user" disabled={!identity && !v.bot} onChange={set("bot")} />
          </Field>
          <Field label="Reviewer username">
            <Input value={v.reviewer} placeholder="default: the token's user" disabled={!identity && !v.reviewer} onChange={set("reviewer")} />
          </Field>
        </FormGrid>
      </Section>
      <div className="flex flex-wrap gap-2">
        <Button onClick={save}>{host ? "Save host" : "Add host"}</Button>
        <Button variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

export default function Hosts() {
  const toast = useToast();
  const confirm = useConfirm();
  const multi = useFeature("hosts");
  const [data, setData] = useState<{ mode: string; key_env?: string; hosts: Host[] } | null>(null);
  const [editing, setEditing] = useState<number | null>(null); // 0 = new
  const [hooks, setHooks] = useState<number | null>(null);
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
  const remove = async (h: Host) => {
    if (!(await confirm({ title: "Delete " + h.name + "?", description: "Webhooks from it will be rejected and its stored tokens removed.", action: "Delete host", danger: true }))) return;
    del("/admin/api/hosts/" + h.id)
      .then(() => {
        toast("Deleted " + h.name);
        load();
      })
      .catch((e) => toast(errMsg(e), true));
  };
  const managed = data?.mode === "database";
  const editHost = editing ? data?.hosts.find((h) => h.id === editing) ?? null : null;
  const hooksHost = hooks ? data?.hosts.find((h) => h.id === hooks) ?? null : null;
  return (
    <section className="grid gap-4">
      <Toolbar>
        <span className="text-sm text-muted-foreground">
          {data && (managed ? "Managed here; stored encrypted in the database." : `Hosts come from the config file (read-only). Set ${data.key_env || "CODEREVIEW_CONSOLE_SECRET_KEY"} to manage them here.`)}
          {data && !multi && " Free: one host per platform."}
        </span>
        <span className="flex-1" />
        {managed && (
          <Button variant="outline" onClick={() => setEditing(0)}>
            <Plus /> Add host
          </Button>
        )}
      </Toolbar>
      <Panel>
        {!data ? (
          <Empty>Loading…</Empty>
        ) : !data.hosts.length ? (
          <Empty>No code hosts yet. Add GitHub, GitLab or Bitbucket.</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">Host</TableHead>
                <TableHead>Credentials</TableHead>
                <TableHead>Webhooks</TableHead>
                <TableHead>Check</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.hosts.map((h) => {
                const s = h.secrets;
                const auth = [s.token && "token ✓", h.settings.app_id && `App ${h.settings.app_id}${s.app_private_key ? " ✓" : " (no key)"}`, s.api_token && "API token ✓",
                  s.signing_tokens && `${s.signing_tokens} signing token${s.signing_tokens > 1 ? "s" : ""}`, s.webhook_secrets && `${s.webhook_secrets} webhook secret${s.webhook_secrets > 1 ? "s" : ""}`].filter(Boolean) as string[];
                const t = tests[h.id];
                return (
                  <TableRow key={h.id}>
                    <TableCell className="pl-4 align-top">
                      <div className="font-medium">{h.name}</div>
                      <Sub>
                        {names[h.platform]} · {h.host}
                      </Sub>
                      <div className="mt-1.5">
                        {h.licensed ? (
                          <StateBadge kind={h.enabled ? "on" : "idle"}>{h.enabled ? "Enabled" : "Disabled"}</StateBadge>
                        ) : (
                          <StateBadge kind="warn">Inactive: needs a license</StateBadge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="align-top whitespace-normal">
                      <div className="flex flex-wrap gap-1">
                        {auth.map((a) => (
                          <Badge variant="outline" className="font-normal text-muted-foreground" key={a}>
                            {a}
                          </Badge>
                        ))}
                      </div>
                      <Sub>
                        mention: {h.settings.bot_name ? "@" + h.settings.bot_name : "token user"}
                        {h.settings.reviewer ? " · reviewer: " + h.settings.reviewer : ""}
                      </Sub>
                    </TableCell>
                    <TableCell className="max-w-80 align-top whitespace-normal">
                      {h.webhooks.length ? (
                        <div className="grid gap-0.5 text-sm">
                          {h.webhooks.map((w) => (
                            <div key={w.id} className={w.enabled ? "" : "text-muted-foreground line-through"}>
                              {w.name}
                            </div>
                          ))}
                        </div>
                      ) : (
                        <Sub>{h.secrets.webhook_secrets || h.secrets.signing_tokens ? "Shared URL only" : "None yet"}</Sub>
                      )}
                      {h.comment_warning && (
                        <div className="mt-1.5">
                          <StateBadge kind="warn">No comment events</StateBadge>
                        </div>
                      )}
                      <Button variant="link" size="sm" className="h-auto px-0" onClick={() => setHooks(h.id)}>
                        <Webhook /> {managed ? "Manage webhooks" : "Show URLs"}
                      </Button>
                    </TableCell>
                    <TableCell className="max-w-56 align-top whitespace-normal">
                      {t && (t.pending ? <span className="text-sm text-muted-foreground">testing…</span> : <StateBadge kind={t.ok ? "on" : "off"}>{t.message}</StateBadge>)}
                    </TableCell>
                    <TableCell className="pr-4 text-right align-top">
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" size="sm" onClick={() => test(h)}>
                          <Plug /> Test
                        </Button>
                        {managed && (
                          <>
                            <Button variant="ghost" size="sm" onClick={() => setEditing(h.id)}>
                              Edit
                            </Button>
                            <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => remove(h)}>
                              Delete
                            </Button>
                          </>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </Panel>
      <Note>
        Each host is a GitHub (github.com or Enterprise), GitLab (gitlab.com or self-managed) or Bitbucket Cloud account the console reviews for. Tokens and webhook secrets are encrypted with <code>CODEREVIEW_CONSOLE_SECRET_KEY</code> before they are stored and are never shown again: leave a secret blank to keep it. Under <b>Manage webhooks</b>, add one webhook per hook you set up (a GitLab system hook, each group hook): each gets its own URL and secret, so one can be replaced or removed without touching the others.
      </Note>

      <SidePanel open={hooksHost !== null} onClose={() => setHooks(null)} title={hooksHost ? "Webhooks: " + hooksHost.name : ""} description="Each webhook has its own URL and secret." wide>
        {hooksHost && <HostWebhooks key={hooksHost.id} host={hooksHost} managed={managed} onChanged={load} />}
      </SidePanel>

      <SidePanel open={editing !== null} onClose={() => setEditing(null)} title={editHost ? "Edit " + editHost.name : "New host"} description="Credentials are encrypted before they are stored.">
        {editing !== null && (
          <HostForm
            key={editing}
            host={editHost}
            onCancel={() => setEditing(null)}
            onSaved={(h) => {
              setEditing(null);
              load().then(() => test(h));
            }}
          />
        )}
      </SidePanel>
    </section>
  );
}
