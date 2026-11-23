import { useEffect, useState } from "react";
import { Plus, X } from "lucide-react";
import { del, get, put } from "../api";
import { Empty, errMsg, Field, FormGrid, NativeSelect, Note, Panel, SidePanel, StateBadge, Sub, Toolbar, useConfirm, useRefreshMe, useToast } from "../ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Config = Record<string, unknown>;
type Profile = {
  name: string; config: Config; source: "console";
  key_env?: string; key_set: boolean; updated_at?: string; updated_by?: string;
};
type View = { profiles: Profile[]; default: string; fallback: string[]; choice_source: "console" | ""; providers: string[] };

// The env var each provider reads its key from when api_key_env is empty
// ("" = no key: Bedrock uses the AWS credential chain, Ollama none).
const keyEnv: Record<string, string> = {
  openai: "OPENAI_API_KEY", anthropic: "ANTHROPIC_API_KEY", gemini: "GEMINI_API_KEY", bedrock: "", azure: "AZURE_OPENAI_API_KEY",
  ollama: "", openrouter: "OPENROUTER_API_KEY", groq: "GROQ_API_KEY", deepseek: "DEEPSEEK_API_KEY", mistral: "MISTRAL_API_KEY",
  xai: "XAI_API_KEY", together: "TOGETHER_API_KEY", litellm: "LITELLM_API_KEY", "openai-compatible": "OPENAI_COMPATIBLE_API_KEY",
};
const needsURL = (p: string) => p === "azure" || p === "openai-compatible";
const str = (v: unknown) => (typeof v === "string" ? v : v == null ? "" : String(v));

// Fields the form edits; any other keys of a profile are kept as they are.
const formKeys = ["provider", "model", "api_key_env", "base_url", "region", "timeout"] as const;
type Form = Record<(typeof formKeys)[number], string> & { name: string };

function ProfileForm({ profile, providers, onSaved, onCancel }: { profile: Profile | null; providers: string[]; onSaved: (v: View) => void; onCancel: () => void }) {
  const toast = useToast();
  const init = (): Form => {
    const c = profile?.config ?? {};
    return { name: profile?.name ?? "", provider: str(c.provider) || "anthropic", model: str(c.model), api_key_env: str(c.api_key_env), base_url: str(c.base_url), region: str(c.region), timeout: str(c.timeout) };
  };
  const [f, setF] = useState<Form>(init);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof Form) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  const save = async () => {
    const cfg: Config = { ...(profile?.config ?? {}) };
    for (const k of formKeys) cfg[k] = f[k].trim();
    setBusy(true);
    try {
      onSaved(await put<View>("/admin/api/models/" + encodeURIComponent(f.name.trim()), { config: cfg }));
      toast("Saved " + f.name.trim());
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusy(false);
    }
  };
  const def = keyEnv[f.provider];
  return (
    <div className="grid gap-4">
      <FormGrid className="lg:grid-cols-2">
        <Field label="Name" hint={<Sub>Repositories and the default refer to it by this name. Lowercase, e.g. claude or fast.</Sub>}>
          <Input value={f.name} onChange={set("name")} disabled={!!profile} placeholder="claude" autoFocus={!profile} />
        </Field>
        <Field label="Provider">
          <NativeSelect value={f.provider} onChange={set("provider")}>
            {providers.map((p) => <option key={p} value={p}>{p}</option>)}
          </NativeSelect>
        </Field>
        <Field label="Model" className="lg:col-span-2" hint={<Sub>The provider's model ID{f.provider === "bedrock" ? " or inference profile ID" : ""}.</Sub>}>
          <Input value={f.model} onChange={set("model")} placeholder={f.provider === "ollama" ? "qwen2.5-coder:14b" : ""} />
        </Field>
        {def !== "" && (
          <Field label="API key: environment variable" className="lg:col-span-2" hint={<Sub>The console never stores API keys. Set the key in this environment variable of the console (for example in its Kubernetes Secret) and restart it once; the console passes it to review jobs. Empty means {def}.</Sub>}>
            <Input value={f.api_key_env} onChange={set("api_key_env")} placeholder={def} spellCheck={false} />
          </Field>
        )}
        <Field label={needsURL(f.provider) ? "Base URL" : "Base URL (optional)"} className="lg:col-span-2" hint={<Sub>{needsURL(f.provider) ? "Required for this provider." : "Only for a proxy or a self-hosted endpoint."}</Sub>}>
          <Input value={f.base_url} onChange={set("base_url")} placeholder="https://" spellCheck={false} />
        </Field>
        {f.provider === "bedrock" && (
          <Field label="AWS region" hint={<Sub>Credentials come from the AWS chain (IRSA, instance role, env).</Sub>}>
            <Input value={f.region} onChange={set("region")} placeholder="us-east-1" />
          </Field>
        )}
        <Field label="Timeout (optional)" hint={<Sub>Per request, e.g. 2m or 10m for slow local models.</Sub>}>
          <Input value={f.timeout} onChange={set("timeout")} placeholder="default" />
        </Field>
      </FormGrid>
      <div className="flex gap-2">
        <Button onClick={save} disabled={busy || !f.name.trim() || !f.model.trim()}>{profile ? "Save profile" : "Add profile"}</Button>
        <Button variant="outline" onClick={onCancel}>Cancel</Button>
      </div>
    </div>
  );
}

function KeyState({ p }: { p: Profile }) {
  if (!p.key_env) return <span className="text-sm text-muted-foreground">none needed</span>;
  return (
    <span className="grid gap-1">
      <code className="text-xs">{p.key_env}</code>
      {p.key_set ? <StateBadge kind="on">set</StateBadge> : <StateBadge kind="warn">not set in the console</StateBadge>}
    </span>
  );
}

// ChoiceCard picks the default model and the fallbacks tried in order.
function ChoiceCard({ v, onSaved }: { v: View; onSaved: (v: View) => void }) {
  const toast = useToast();
  const [def, setDef] = useState(v.default);
  const [fb, setFb] = useState<string[]>(v.fallback);
  const [add, setAdd] = useState("");
  useEffect(() => {
    setDef(v.default);
    setFb(v.fallback);
  }, [v.default, v.fallback.join(",")]);
  const names = v.profiles.map((p) => p.name);
  const free = names.filter((n) => n !== def && !fb.includes(n));
  const dirty = def !== v.default || fb.join(",") !== v.fallback.join(",");
  const save = async () => {
    try {
      onSaved(await put<View>("/admin/api/model-choice", { default: def, fallback: fb.filter((x) => x !== def) }));
      toast("Default model saved");
    } catch (e) {
      toast(errMsg(e), true);
    }
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle className="h2">Default model</CardTitle>
        <CardDescription>
          Reviews use the default unless a repository picks another profile. When it fails, the fallbacks are tried in order.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <FormGrid className="lg:grid-cols-2">
          <Field label="Default">
            <NativeSelect value={names.includes(def) ? def : ""} onChange={(e) => setDef(e.target.value)}>
              <option value="" disabled>Choose a profile</option>
              {names.map((n) => <option key={n} value={n}>{n}</option>)}
            </NativeSelect>
          </Field>
          <Field label="Fallbacks, in order">
            <div className="flex flex-wrap items-center gap-1.5">
              {fb.map((n, i) => (
                <Badge key={n} variant="secondary" className="gap-1">
                  {i + 1}. {n}
                  <button type="button" aria-label={"Remove " + n} className="rounded-sm hover:text-destructive" onClick={() => setFb(fb.filter((x) => x !== n))}>
                    <X className="size-3" />
                  </button>
                </Badge>
              ))}
              {free.length > 0 && (
                <NativeSelect className="h-8 w-auto" value={add} onChange={(e) => { if (e.target.value) setFb([...fb, e.target.value]); setAdd(""); }} aria-label="Add a fallback">
                  <option value="">{fb.length ? "Add another…" : "Add a fallback…"}</option>
                  {free.map((n) => <option key={n} value={n}>{n}</option>)}
                </NativeSelect>
              )}
              {!fb.length && !free.length && <span className="text-sm text-muted-foreground">add another profile to use one</span>}
            </div>
          </Field>
        </FormGrid>
        <div>
          <Button onClick={save} disabled={!dirty || !names.includes(def)}>Save default</Button>
        </div>
      </CardContent>
    </Card>
  );
}

export default function Models() {
  const toast = useToast();
  const confirm = useConfirm();
  const refreshMe = useRefreshMe();
  const [v, setV] = useState<View | null>(null);
  const [edit, setEdit] = useState<Profile | "new" | null>(null);
  const load = () => get<View>("/admin/api/models").then(setV).catch((e) => toast(errMsg(e), true));
  useEffect(() => { load(); }, []);
  const saved = (nv: View) => {
    setV(nv);
    setEdit(null);
    refreshMe(); // the repository form and Setup read the profile list
  };
  const remove = async (p: Profile) => {
    if (!(await confirm({ title: "Delete " + p.name + "?", description: "Repositories can't pick it any more.", action: "Delete profile", danger: true }))) return;
    try {
      saved(await del<View>("/admin/api/models/" + encodeURIComponent(p.name)));
      toast("Deleted " + p.name);
    } catch (e) {
      toast(errMsg(e), true);
    }
  };
  if (!v) return <Empty>Loading…</Empty>;
  const title = edit === null ? "" : edit === "new" ? "Add a model profile" : "Edit " + edit.name;
  return (
    <section className="grid gap-4">
      <Toolbar>
        <Button onClick={() => setEdit("new")}><Plus /> Add profile</Button>
      </Toolbar>
      <Panel>
        {v.profiles.length === 0 ? (
          <Empty>No model profiles yet. Add one to start reviewing.</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">Profile</TableHead>
                <TableHead>Provider and model</TableHead>
                <TableHead>API key</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {v.profiles.map((p) => (
                <TableRow key={p.name}>
                  <TableCell className="pl-4 align-top">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="font-medium">{p.name}</span>
                      {p.name === v.default && <Badge>default</Badge>}
                      {v.fallback.includes(p.name) && <Badge variant="secondary">fallback {v.fallback.indexOf(p.name) + 1}</Badge>}
                    </div>
                    <Sub>by {p.updated_by || "—"}</Sub>
                  </TableCell>
                  <TableCell className="align-top whitespace-normal">
                    <code className="text-xs">{str(p.config.provider)}:{str(p.config.model)}</code>
                    {!!p.config.base_url && <Sub>{str(p.config.base_url)}</Sub>}
                    {!!p.config.region && <Sub>{str(p.config.region)}</Sub>}
                  </TableCell>
                  <TableCell className="align-top"><KeyState p={p} /></TableCell>
                  <TableCell className="pr-4 text-right align-top">
                    <div className="flex justify-end gap-2">
                      <Button size="sm" variant="outline" onClick={() => setEdit(p)}>Edit</Button>
                      <Button size="sm" variant="ghost" className="text-destructive" onClick={() => remove(p)}>Delete</Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
      {v.profiles.length > 0 && <ChoiceCard v={v} onSaved={saved} />}
      <Note>
        API keys are never stored in the console: each profile names an environment variable of the console that holds its key, and the console hands that key to each review job.
      </Note>
      <SidePanel
        open={edit !== null}
        onClose={() => setEdit(null)}
        title={title}
        description="A provider, a model and where its API key comes from."
      >
        {edit !== null && (
          <ProfileForm
            key={edit === "new" ? "new" : edit.name}
            profile={edit === "new" ? null : edit}
            providers={v.providers}
            onSaved={saved}
            onCancel={() => setEdit(null)}
          />
        )}
      </SidePanel>
    </section>
  );
}
