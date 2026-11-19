import { useEffect, useState } from "react";
import { Plug, Plus } from "lucide-react";
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
  webhook_url: string; events: string[]; licensed: boolean;
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
      <Section title="Webhooks">
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
                <TableHead>Webhook URL</TableHead>
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
                      <code className="hosturl break-all">{h.webhook_url}</code>
                      <Sub>Events: {h.events.join(", ")}</Sub>
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
        Each host is a GitHub (github.com or Enterprise), GitLab (gitlab.com or self-managed) or Bitbucket Cloud account the console reviews for. Tokens and webhook secrets are encrypted with <code>CODEREVIEW_CONSOLE_SECRET_KEY</code> before they are stored and are never shown again: leave a secret blank to keep it. Point each code host's webhook at the URL shown, with the events listed. Several hooks on one host (a GitLab system hook plus group hooks) can each have their own secret: add one per line.
      </Note>

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
