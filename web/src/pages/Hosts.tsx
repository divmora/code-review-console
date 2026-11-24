import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ago, fmtDate, fmtTokenTime } from "../format";
import type { TokenExpiry } from "../types";
import { fmtDateTime } from "../format";
import { Check, Copy, Plug, Plus, Radio, Webhook } from "lucide-react";
import { del, get, post, put, qs } from "../api";
import { Empty, errMsg, Field, FormGrid, LockNote, NativeSelect, Note, Panel, SidePanel, StateBadge, Sub, Toolbar, useConfirm, useFeature, useToast } from "../ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { stickyEnd, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

type Host = {
  id: number; platform: "github" | "gitlab" | "bitbucket"; host: string; base_url: string; name: string; enabled: boolean;
  settings: { app_id?: number; email?: string; require_signature?: boolean; skip_tls_verify?: boolean; bot_name?: string; reviewer?: string };
  secrets: { token: boolean; app_private_key: boolean; api_token: boolean };
  webhooks: Hook[]; events: string[]; licensed: boolean; comment_warning?: string; token_expiry?: TokenExpiry; check?: HostCheck;
  // The handle @bot commands use, and where it comes from (host bot name,
  // Tuning, or the token user found by the last check).
  mention?: string; mention_source?: "host" | "settings" | "token";
};

// Delivery is one entry of the webhook history (the Webhooks page).
type Delivery = { id: number; received_at: string; outcome: string; reason: string; auth: string; event: string; kind: string; action: string; project: string };

type Hook = {
  id: string; host_id: number; name: string; enabled: boolean; url: string;
  secrets: { webhook_secret: boolean; signing_tokens: number }; created_at: string; created_by: string;
  accept_secret_token?: boolean; accepts?: string;
  // The latest delivery on this URL and the last 24 hours' counts.
  deliveries?: { last?: Delivery; accepted: number; rejected: number };
};

const rejectedOutcome = (o: string) => o === "rejected" || o === "error";

// deliveryText says what happened to a delivery in a few words.
function deliveryText(d: Delivery) {
  if (rejectedOutcome(d.outcome)) return (d.outcome === "error" ? "failed" : "rejected") + (d.reason ? ": " + d.reason : "");
  const via = d.auth === "token-unsigned" ? "secret token, not signed" : d.auth === "token" ? "secret token" : d.auth;
  return "accepted" + (via ? " (" + via + ")" : "") + (d.outcome !== "queued" ? ", " + d.outcome : ", review queued");
}

// HookHealth is a webhook's latest delivery and its last 24 hours, with a
// link to its deliveries on the Webhooks page.
function HookHealth({ hook }: { hook: Hook }) {
  const d = hook.deliveries;
  const link = (
    <Link className="text-primary underline-offset-2 hover:underline" to={"/webhooks" + qs({ endpoint: hook.id })}>
      Show deliveries
    </Link>
  );
  if (!d?.last)
    return (
      <div className="text-sm text-muted-foreground">
        No deliveries yet. Set the URL and secret on the code host, then use <b>Check a test delivery</b>.
      </div>
    );
  const bad = rejectedOutcome(d.last.outcome);
  return (
    <div className="grid gap-0.5 text-sm">
      <div>
        Last delivery <span title={fmtDateTime(d.last.received_at)}>{ago(d.last.received_at)}</span>:{" "}
        <span className={bad ? "text-destructive" : "text-success"}>{deliveryText(d.last)}</span>
      </div>
      <div className="text-muted-foreground">
        Last 24 h: {d.accepted} accepted{d.rejected ? ", " : ""}
        {d.rejected > 0 && <span className="text-destructive">{d.rejected} rejected or failed</span>} · {link}
      </div>
    </div>
  );
}

const testSteps: Record<Host["platform"], string> = {
  gitlab: "In GitLab, open Settings → Webhooks on the group or project (Admin area → System hooks for a system hook), find this URL and choose Test → Merge request events or Comments.",
  github: "On GitHub, open the webhook (repository or organization Settings → Webhooks), then Recent Deliveries → Redeliver on any delivery. A new webhook sends a ping when it's created.",
  bitbucket: "Bitbucket has no test button: open, update or comment on a pull request in a repository this webhook covers.",
};

// TestDelivery waits for the next delivery on a webhook after the admin
// triggers one on the code host, and says whether it arrived and why it
// was refused. It compares delivery IDs, not clocks.
function TestDelivery({ hook, platform, onClose }: { hook: Hook; platform: Host["platform"]; onClose: () => void }) {
  const [base, setBase] = useState<number | null>(null);
  const [got, setGot] = useState<Delivery | null>(null);
  const [waited, setWaited] = useState(0);
  const latest = () => get<{ events: Delivery[] }>("/admin/api/webhooks" + qs({ endpoint: hook.id, limit: 1 })).then((d) => d.events[0]);
  useEffect(() => {
    latest().then((e) => setBase(e ? e.id : 0)).catch(() => setBase(0));
  }, [hook.id]);
  useEffect(() => {
    if (base === null || got || waited >= 60) return;
    const t = window.setTimeout(() => {
      latest()
        .then((e) => e && e.id > base && setGot(e))
        .catch(() => {})
        .finally(() => setWaited((n) => n + 1));
    }, 3000);
    return () => window.clearTimeout(t);
  }, [base, got, waited]);
  const bad = got && rejectedOutcome(got.outcome);
  return (
    <div role="status" className="grid gap-2 rounded-lg border bg-muted/40 p-3 text-sm">
      <div className="font-medium">Check a test delivery</div>
      <div className="text-muted-foreground">{testSteps[platform]}</div>
      {!got ? (
        <div className="text-muted-foreground">
          {base === null ? "Starting…" : waited >= 60 ? "Nothing arrived in 3 minutes. Check that the code host can reach the URL and that the webhook is enabled there." : "Waiting for the next delivery on this URL…"}
        </div>
      ) : (
        <div className={bad ? "text-destructive" : "text-success"}>
          {bad ? "Arrived, but " : "Arrived and "}
          {deliveryText(got)}
          {bad && got.auth === "failed" && ". Paste this webhook's secret into the code host again, or use New secret."}
        </div>
      )}
      <div className="flex gap-2">
        {(got || waited >= 60) && (
          <Button size="sm" variant="outline" onClick={() => (setGot(null), setWaited(0), latest().then((e) => setBase(e ? e.id : 0)).catch(() => {}))}>
            Wait for another
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>
    </div>
  );
}

const names = { github: "GitHub", gitlab: "GitLab", bitbucket: "Bitbucket" };
const defaults = { github: "https://api.github.com", gitlab: "https://gitlab.com", bitbucket: "https://api.bitbucket.org/2.0" };

function Secret({ label, set, multi, help, value, onChange }: { label: string; set: boolean; multi?: boolean; help?: string; value: string; onChange: (v: string) => void }) {
  const ph = set ? "Set ✓ — leave blank to keep" : "";
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
  const [testing, setTesting] = useState(false);
  const host = platform === "gitlab" ? "GitLab" : platform === "github" ? "GitHub" : "Bitbucket";
  const update = (body: any, msg: string) =>
    put<{ webhook: Hook; secret?: string }>("/admin/api/webhooks/" + hook.id, body)
      .then((r) => {
        toast(msg);
        if (r.secret) setSecret(r.secret);
        onChanged();
      })
      .catch((e) => toast(errMsg(e), true));
  const regenerate = async () => {
    if (!(await confirm({ title: "Replace the secret of " + hook.name + "?", description: `The ${host} webhook stops working at once: every delivery is rejected until you paste the new secret into it. Reviews and @bot commands from it stop until then. The new secret is shown once.`, action: "Replace secret", danger: true }))) return;
    update({ regenerate_secret: true }, "New secret for " + hook.name);
  };
  const remove = async () => {
    if (!(await confirm({ title: "Delete " + hook.name + "?", description: `Its URL stops working at once: the ${host} webhook's deliveries are rejected, so its merge requests are no longer reviewed. Remove the webhook from ${host} too. This can't be undone; a new webhook gets a new URL.`, action: "Delete webhook", danger: true }))) return;
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
        {platform === "gitlab" && hook.accepts && (
          <Badge variant="outline" className={hook.accepts === "signature" ? "font-normal text-muted-foreground" : "font-normal text-warning"} title="What this webhook accepts">
            accepts: {hook.accepts}
          </Badge>
        )}
        <Button variant="outline" size="sm" className="h-7" onClick={() => update({ enabled: !hook.enabled }, hook.enabled ? "Disabled " + hook.name : "Enabled " + hook.name)}>
          {hook.enabled ? "Disable" : "Enable"}
        </Button>
        <span className="flex-1" />
        {/* Replacing the secret and deleting break the hook on the code host: kept apart from Disable, and confirmed. */}
        <div className="flex items-center gap-1 border-l pl-2">
          <Button variant="ghost" size="sm" className="text-warning hover:text-warning" onClick={regenerate}>
            New secret
          </Button>
          <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={remove}>
            Delete
          </Button>
        </div>
      </div>
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <HookHealth hook={hook} />
        </div>
        {!testing && (
          <Button variant="outline" size="sm" onClick={() => setTesting(true)}>
            <Radio /> Check a test delivery
          </Button>
        )}
      </div>
      {testing && <TestDelivery hook={hook} platform={platform} onClose={() => (setTesting(false), onChanged())} />}
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
      {platform === "gitlab" && hook.secrets.signing_tokens > 0 && (
        <Field label="Unsigned deliveries with the secret token" hint={<Sub>With a signing token, only signed deliveries are accepted, so the old secret token alone can't be used to send events. Accept both only while hooks still send unsigned events.</Sub>}>
          <NativeSelect className="max-w-sm" value={String(!!hook.accept_secret_token)}
            onChange={(e) => update({ accept_secret_token: e.target.value === "true" }, e.target.value === "true" ? "Also accepting the secret token" : "Signed deliveries only")}>
            <option value="false">Reject (signed deliveries only)</option>
            <option value="true">Accept too (while migrating)</option>
          </NativeSelect>
        </Field>
      )}
      <Sub>
        Added {fmtDate(hook.created_at)}
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
  const sec = host?.secrets ?? { token: false, app_private_key: false, api_token: false };
  const [plat, setPlat] = useState<Host["platform"]>(host?.platform ?? "gitlab");
  const [v, setV] = useState({
    base: host?.base_url ?? "", name: host?.name ?? "", enabled: host ? host.enabled : true,
    token: "", appID: st.app_id ? String(st.app_id) : "", appKey: "", email: st.email ?? "", apiToken: "",
    reqSig: !!st.require_signature, skipTLS: !!st.skip_tls_verify, bot: st.bot_name ?? "", reviewer: st.reviewer ?? "",
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
      {plat === "gitlab" && (
        <>
          <Section title="Webhooks">
            <Sub>Each webhook has its own URL and secrets: add them under Manage webhooks once the host is saved.</Sub>
            <Field label="Unsigned deliveries (every webhook of this host)" hint={<Sub>A webhook with its own signing token rejects unsigned deliveries either way, unless it is set to accept both.</Sub>}>
              <NativeSelect value={String(v.reqSig)} onChange={(e) => setV({ ...v, reqSig: e.target.value === "true" })}>
                <option value="false">Accept with a secret token (unless the webhook has a signing token)</option>
                <option value="true">Reject everywhere (signed deliveries only)</option>
              </NativeSelect>
            </Field>
          </Section>
          <Separator />
        </>
      )}
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
  const [tests, setTests] = useState<Record<number, HostCheck & { pending?: boolean }>>({});
  const load = () => get<{ mode: string; key_env?: string; hosts: Host[] }>("/admin/api/hosts").then(setData).catch((e) => toast(errMsg(e), true));
  useEffect(() => {
    load();
  }, []);
  const test = (h: Host) => {
    setTests((t) => ({ ...t, [h.id]: { ok: false, pending: true } }));
    post<HostCheck>(`/admin/api/hosts/${h.id}/test`)
      .then((r) => setTests((t) => ({ ...t, [h.id]: r })))
      .catch((e) => setTests((t) => ({ ...t, [h.id]: { ok: false, summary: "Check failed", message: errMsg(e) } })));
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
                <TableHead className={stickyEnd}>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.hosts.map((h) => {
                const s = h.secrets;
                const auth = [s.token && "token set", h.settings.app_id && `App ${h.settings.app_id}${s.app_private_key ? ", key set" : " (no key)"}`, s.api_token && "API token set"].filter(Boolean) as string[];
                const t = tests[h.id] || h.check;
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
                      {h.enabled && <TokenExpiryNote e={h.token_expiry} />}
                    </TableCell>
                    <TableCell className="align-top whitespace-normal">
                      <div className="flex flex-wrap gap-1">
                        {auth.map((a) => (
                          <Badge variant="outline" className="font-normal text-muted-foreground" key={a}>
                            {a}
                          </Badge>
                        ))}
                      </div>
                      <Sub title={h.mention_source === "token" ? "The token user, found by the last check" : h.mention_source === "settings" ? "The bot name in Settings" : h.mention_source === "host" ? "This host's bot name" : undefined}>
                        mention: {h.mention ? "@" + h.mention : "unknown until the token is checked"}
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
                        <Sub>None yet</Sub>
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
                    <TableCell className="w-56 max-w-56 align-top whitespace-normal">
                      <CheckResult t={t} />
                    </TableCell>
                    <TableCell className={cn("pr-4 text-right align-top", stickyEnd)}>
                      <div className="flex flex-col items-end gap-1 xl:flex-row xl:justify-end">
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

// TokenExpiryNote says when the host's token expires, as the code host
// reports it (looked up on save, Test, and once a day).
function TokenExpiryNote({ e }: { e?: TokenExpiry }) {
  if (!e) return null;
  if (e.error) return <Sub className="text-destructive">Token: {e.error}</Sub>;
  if (!e.known) return null;
  if (!e.expires) return <Sub>Token never expires</Sub>;
  const exp = new Date(e.expires);
  const days = Math.ceil((exp.getTime() - Date.now()) / 86400000);
  // In UTC with the time: a GitLab token stops at 00:00 UTC on its date,
  // which is the evening before west of UTC (issue #132).
  const date = fmtTokenTime(e.expires);
  if (days <= 0) return <div className="mt-1"><StateBadge kind="warn">Token expired {date}</StateBadge></div>;
  if (days <= 14) return <div className="mt-1" title={"Expires " + date}><StateBadge kind="warn">Token expires in {days} {days === 1 ? "day" : "days"}</StateBadge><Sub>{date}</Sub></div>;
  return <Sub>Token expires {date}</Sub>;
}

type HostCheck = { ok: boolean; summary?: string; message?: string; at?: string; user?: string; source?: "test" | "startup" };

// CheckResult is the latest check of a host's credentials (the identity
// lookup at start and after host changes, or Test): a short status that
// wraps inside its column, with the full result one click away (and on
// hover). "Not checked yet" until one runs, so a stored token is never
// mistaken for a working one.
function CheckResult({ t }: { t?: HostCheck & { pending?: boolean } }) {
  const [open, setOpen] = useState(false);
  if (!t) return <Sub>Not checked yet</Sub>;
  if (t.pending) return <span className="text-sm text-muted-foreground">testing…</span>;
  const summary = t.summary || (t.ok ? "OK" : "Check failed");
  return (
    <div className="grid min-w-0 gap-1" title={t.message}>
      <div><StateBadge kind={t.ok ? "on" : "off"}>{summary}</StateBadge></div>
      {t.at && (
        <Sub title={fmtDateTime(t.at)}>
          checked {ago(t.at)}
          {t.source === "startup" ? " (at start)" : t.source === "test" ? " (Test)" : ""}
        </Sub>
      )}
      {t.message && t.message !== summary && (
        <>
          <button type="button" className="w-fit text-xs text-muted-foreground underline-offset-2 hover:underline" onClick={() => setOpen(!open)}>
            {open ? "Hide details" : "Details"}
          </button>
          {open && <div className="text-xs break-words text-muted-foreground">{t.message}</div>}
        </>
      )}
    </div>
  );
}
