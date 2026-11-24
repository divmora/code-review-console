import { ReactNode, useEffect, useState } from "react";
import { diffCount, useUnsaved } from "../unsaved";
import { get, put } from "../api";
import { Empty, errMsg, Field, FormGrid, NativeSelect, Note, StateBadge, Sub, useMe, useToast } from "../ui";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

type Engine = {
  concurrency?: number; batch_tokens?: number; max_files?: number; full_file_max_lines?: number; context_lines?: number; passes?: number;
  verify?: boolean | null; verify_model?: string; verify_can_drop?: string; verify_can_add?: boolean; repo_config_file?: string;
  defaults?: Record<string, unknown>;
};

type View = {
  workers: number; queue_size: number; job_timeout: string; debounce: string; history: number;
  bot_name?: string; allow_repo_model: boolean; extra_env: string[]; engine: Engine;
  dashboard_token_set: boolean; dashboard_token_expires?: string; dashboard_token_expired: boolean;
  dashboard_access: "admins" | "token" | "public"; env_set: Record<string, boolean>;
  metrics_access: "off" | "token" | "public"; metrics_token_set: boolean;
};

function Card2({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
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

function NumField({ label, value, onChange, hint, placeholder, min, max }: { label: string; value?: number; onChange: (v: number) => void; hint?: ReactNode; placeholder?: string; min?: number; max?: number }) {
  return (
    <Field label={label} hint={hint}>
      <Input type="number" min={min} max={max} placeholder={placeholder} value={value || ""} onChange={(e) => onChange(parseInt(e.target.value || "0", 10))} />
    </Field>
  );
}

// scrapeConfig is a Prometheus scrape job for this console.
function scrapeConfig(access: string, token: string) {
  const u = new URL(location.origin);
  const lines = [
    "- job_name: code-review-console",
    "  scheme: " + u.protocol.replace(":", ""),
    "  metrics_path: /metrics",
    "  static_configs:",
    "    - targets: [\"" + u.host + "\"]",
  ];
  if (access === "token") lines.push("  authorization:", "    credentials: " + (token || "<the metrics token>"));
  return lines.join("\n");
}

// newToken makes a random token (32 hex characters).
function newToken() {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

// Tuning edits how many reviews run, timeouts, the review engine, the
// bot's mention handle, extra environment variables and /metrics access
// (dashboard access moved to the Dashboards page). Everything applies to
// the next review, without a restart.
export default function Tuning() {
  const toast = useToast();
  const me = useMe();
  const [v, setV] = useState<View | null>(null);
  const [env, setEnv] = useState("");
  const [defaults, setDefaults] = useState("");
  const [mToken, setMToken] = useState("");
  const [mClear, setMClear] = useState(false);
  const [mCopied, setMCopied] = useState(false);
  const [base, setBase] = useState<View | null>(null); // as loaded or saved
  const take = (raw: Partial<View>) => {
    // Older consoles leave empty fields out (#95): fill them in.
    const n = { ...raw, extra_env: raw.extra_env ?? [], engine: raw.engine ?? {}, env_set: raw.env_set ?? {} } as View;
    setV(n);
    setBase(n);
    setEnv(n.extra_env.join(", "));
    setDefaults(n.engine.defaults ? JSON.stringify(n.engine.defaults, null, 2) : "");
    setMToken("");
    setMClear(false);
    setMCopied(false);
  };
  useEffect(() => {
    get<Partial<View>>("/admin/api/tuning").then(take).catch((e) => toast(errMsg(e), true));
  }, []);
  // Unsaved changes: the fields, the text boxes and a token typed or cleared.
  let count = 0;
  if (v && base) {
    count = diffCount({ ...v, extra_env: [], engine: { ...v.engine, defaults: undefined } }, { ...base, extra_env: [], engine: { ...base.engine, defaults: undefined } });
    if (env !== base.extra_env.join(", ")) count++;
    if (defaults !== (base.engine.defaults ? JSON.stringify(base.engine.defaults, null, 2) : "")) count++;
    if (mToken || mClear) count++;
  }
  useUnsaved("tuning", { label: "Queue, review engine and access", count, save: () => save(), discard: () => base && take(base) });
  if (!v) return <Empty>Loading…</Empty>;
  const set = <K extends keyof View>(k: K) => (x: View[K]) => setV({ ...v, [k]: x });
  const eng = <K extends keyof Engine>(k: K) => (x: Engine[K]) => setV({ ...v, engine: { ...v.engine, [k]: x } });
  async function save() {
    if (!v) return;
    let parsed: Record<string, unknown> | undefined;
    if (defaults.trim()) {
      try {
        parsed = JSON.parse(defaults);
      } catch {
        toast("Repository defaults: not valid JSON", true);
        return;
      }
    }
    const body = {
      ...v, engine: { ...v.engine, defaults: parsed },
      extra_env: env.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean),
      metrics_token: mToken, clear_metrics_token: mClear,
    };
    try {
      take(await put<Partial<View>>("/admin/api/tuning", body));
      toast("Settings saved");
    } catch (e) {
      toast(errMsg(e), true);
    }
  }
  const verify = v.engine.verify === undefined || v.engine.verify === null ? "" : String(v.engine.verify);
  return (
    <section className="grid gap-4">
      <Card2 title="Queue" description="How many reviews run at once and how long they may take. Changes apply at once; running reviews finish with their old timeout.">
        <FormGrid className="lg:grid-cols-3">
          <NumField label="Reviews at once" min={1} max={64} value={v.workers} onChange={set("workers")} hint={<Sub>Each one is a container or pod with the Executor page's resources.</Sub>} />
          <NumField label="Waiting reviews (queue size)" min={1} max={10000} value={v.queue_size} onChange={set("queue_size")} hint={<Sub>More webhooks get HTTP 503 and the code host retries.</Sub>} />
          <Field label="Review timeout" hint={<Sub>Between 1m and 6h.</Sub>}>
            <Input value={v.job_timeout} placeholder="20m" onChange={(e) => set("job_timeout")(e.target.value)} />
          </Field>
          <Field label="Wait after a push" hint={<Sub>Pushes within this time become one review. 0s reviews at once.</Sub>}>
            <Input value={v.debounce} placeholder="30s" onChange={(e) => set("debounce")(e.target.value)} />
          </Field>
          <NumField label="Jobs on the dashboard" min={10} max={5000} value={v.history} onChange={set("history")} />
        </FormGrid>
      </Card2>
      <Card2 title="Review engine" description="The codereview CLI's review settings. Leave a field empty for the CLI's default.">
        <FormGrid className="lg:grid-cols-3">
          <NumField label="Model calls at once, per review" placeholder="4" min={0} max={32} value={v.engine.concurrency} onChange={eng("concurrency")} />
          <NumField label="Tokens per model call" placeholder="40000" min={0} value={v.engine.batch_tokens} onChange={eng("batch_tokens")} />
          <NumField label="Skip reviews with more files than" placeholder="150" min={0} value={v.engine.max_files} onChange={eng("max_files")} />
          <NumField label="Send whole files up to (lines)" placeholder="800" min={0} value={v.engine.full_file_max_lines} onChange={eng("full_file_max_lines")} />
          <NumField label="Context lines around changes" placeholder="60" min={0} value={v.engine.context_lines} onChange={eng("context_lines")} />
          <NumField label="Review passes" placeholder="1" min={0} max={3} value={v.engine.passes} onChange={eng("passes")} hint={<Sub>2 or 3 catch more, for one more model call each.</Sub>} />
          <Field label="Verification pass">
            <NativeSelect value={verify} onChange={(e) => eng("verify")(e.target.value === "" ? null : e.target.value === "true")}>
              <option value="">CLI default (on)</option>
              <option value="true">On</option>
              <option value="false">Off</option>
            </NativeSelect>
          </Field>
          <Field label="Verification model">
            <NativeSelect value={v.engine.verify_model || ""} onChange={(e) => eng("verify_model")(e.target.value)}>
              <option value="">The review model</option>
              {v.engine.verify_model && !me.profiles.includes(v.engine.verify_model) && <option value={v.engine.verify_model}>{v.engine.verify_model}</option>}
              {me.profiles.map((p) => <option key={p} value={p}>{p}</option>)}
            </NativeSelect>
          </Field>
          <Field label="The verifier may drop">
            <NativeSelect value={v.engine.verify_can_drop || ""} onChange={(e) => eng("verify_can_drop")(e.target.value)}>
              <option value="">CLI default (confident)</option>
              <option value="confident">Minor findings, and major ones it is sure are wrong</option>
              <option value="minor">Only minor and nitpick findings</option>
              <option value="all">Anything it disputes</option>
            </NativeSelect>
          </Field>
          <Field label="The verifier may add findings">
            <NativeSelect value={String(!!v.engine.verify_can_add)} onChange={(e) => eng("verify_can_add")(e.target.value === "true")}>
              <option value="false">No</option>
              <option value="true">Yes</option>
            </NativeSelect>
          </Field>
          <Field label="Repository config file">
            <Input placeholder=".codereview.yaml" value={v.engine.repo_config_file || ""} onChange={(e) => eng("repo_config_file")(e.target.value)} />
          </Field>
          <Field label="Repositories may pick a model">
            <NativeSelect value={String(v.allow_repo_model)} onChange={(e) => set("allow_repo_model")(e.target.value === "true")}>
              <option value="false">No</option>
              <option value="true">Yes, with model: in their config file</option>
            </NativeSelect>
          </Field>
          <Field label="Repository defaults (JSON)" className="lg:col-span-3" hint={<Sub>Keys of a repository config file (language, path_filters, path_instructions, guidelines, high_level_summary…), applied under each repository's own file. The profile, comment limit and severity are under Default review settings.</Sub>}>
            <Textarea rows={4} spellCheck={false} className="font-mono text-xs" placeholder='{"language": "English", "path_filters": ["!**/*.lock"]}' value={defaults} onChange={(e) => setDefaults(e.target.value)} />
          </Field>
        </FormGrid>
      </Card2>
      <Card2 title="Bot and review jobs">
        <FormGrid className="lg:grid-cols-2">
          <Field label="Default bot name" hint={<Sub>The mention handle (@name review) for hosts that set none. Empty: each host's token user.</Sub>}>
            <Input placeholder="codereview" value={v.bot_name || ""} onChange={(e) => set("bot_name")(e.target.value)} />
          </Field>
          <Field label="Extra environment variables for reviews" hint={
            <span className="grid gap-1">
              <Sub>Names of the console's variables to pass to every review (model key variables are passed already). The console's own secrets can't be passed.</Sub>
              {v.extra_env.length > 0 && (
                <span className="flex flex-wrap gap-1.5">
                  {v.extra_env.map((n) => <StateBadge key={n} kind={v.env_set[n] ? "on" : "warn"}>{n}{v.env_set[n] ? "" : ": not set"}</StateBadge>)}
                </span>
              )}
            </span>
          }>
            <Input placeholder="MY_PROXY_CA, SSL_CERT_DIR" value={env} onChange={(e) => setEnv(e.target.value)} />
          </Field>
        </FormGrid>
      </Card2>
      <Card2 title="Prometheus metrics" description="/metrics in the Prometheus text format: queue, reviews by kind and result, durations, tokens, webhooks by outcome, and when the license and host tokens expire. No repository names.">
        <FormGrid className="lg:grid-cols-2">
          <Field label="Who can read /metrics" hint={<Sub>{v.metrics_access === "public" ? "Anyone who can reach the console's URL. Fine on a private network the scraper shares." : v.metrics_access === "token" ? "Scrapers send the token as a bearer token." : "Off: /metrics answers 404."}</Sub>}>
            <NativeSelect value={v.metrics_access} onChange={(e) => set("metrics_access")(e.target.value as View["metrics_access"])}>
              <option value="off">Off</option>
              <option value="token">Scrapers with the token</option>
              <option value="public">Anyone who can reach the console</option>
            </NativeSelect>
          </Field>
          <Field label="Metrics token" hint={<Sub>{mClear ? "Removed when you save." : mToken ? "Replaces the current token when you save." : v.metrics_token_set ? "Set. Stored hashed and never shown again." : "No token."}</Sub>}>
            <div className="flex flex-wrap items-center gap-2">
              <Input className="max-w-sm font-mono" placeholder={v.metrics_token_set ? "Set (leave empty to keep it)" : "Not set"} value={mToken} onChange={(e) => { setMToken(e.target.value); setMClear(false); setMCopied(false); }} />
              <Button variant="outline" onClick={() => { setMToken(newToken()); setMClear(false); setMCopied(false); }}>Generate</Button>
              {v.metrics_token_set && (
                <Button variant="ghost" className="text-destructive" onClick={() => { setMToken(""); setMClear(true); }} disabled={mClear}>{mClear ? "Removed on save" : "Remove"}</Button>
              )}
            </div>
          </Field>
          {v.metrics_access !== "off" && (
            <Field label="Prometheus scrape config" className="lg:col-span-2" hint={mToken ? <Sub className="text-warning">Copy it now: once saved, the token can't be shown again.</Sub> : <Sub>Generate a token to fill it in.</Sub>}>
              <div className="grid gap-2">
                <Textarea readOnly rows={v.metrics_access === "token" ? 7 : 5} className="font-mono text-xs" value={scrapeConfig(v.metrics_access, mToken)} onFocus={(e) => e.target.select()} />
                <div>
                  <Button variant="outline" size="sm" onClick={() => {
                    navigator.clipboard?.writeText(scrapeConfig(v.metrics_access, mToken)).then(() => setMCopied(true), () => toast("Copy failed: select the text and copy it", true));
                  }}>{mCopied ? "Copied" : "Copy"}</Button>
                </div>
              </div>
            </Field>
          )}
        </FormGrid>
      </Card2>
      <Card className="py-4">
        <CardFooter className="flex flex-wrap items-center gap-3">
          <Button onClick={save}>Save queue and review settings</Button>
          <Note>Applies to the next review, without a restart.</Note>
        </CardFooter>
      </Card>
    </section>
  );
}
