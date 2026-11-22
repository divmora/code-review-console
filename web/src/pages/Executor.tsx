import { useEffect, useState } from "react";
import { get, put } from "../api";
import { Empty, errMsg, Field, FormGrid, NativeSelect, Note, Sub, useToast } from "../ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

type Settings = {
  type?: string; version: string; cpu: string; memory: string; ttl?: string;
  node_selector?: Record<string, string>; tolerations?: Record<string, unknown>[]; affinity?: Record<string, unknown>;
  priority_class_name?: string; pod_labels?: Record<string, string>; pod_annotations?: Record<string, string>;
  ca_bundle: { config_map?: string; secret?: string; key?: string };
};
type View = {
  type: string; editable: boolean; source: "console" | "config"; image_repo?: string; image?: string;
  settings: Settings; fixed: Record<string, string>; min_cli_version: string; licensed: boolean;
};

// key=value lines <-> a map.
const toLines = (m?: Record<string, string>) => Object.entries(m ?? {}).map(([k, v]) => k + "=" + v).join("\n");
function fromLines(name: string, s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const raw of s.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const i = line.indexOf("=");
    if (i <= 0) throw new Error(name + ": write each entry as key=value (" + line + ")");
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}
const toJSON = (v: unknown) => (v && Object.keys(v as object).length ? JSON.stringify(v, null, 2) : "");
function fromJSON(name: string, s: string, array: boolean) {
  if (!s.trim()) return undefined;
  let v: unknown;
  try {
    v = JSON.parse(s);
  } catch {
    throw new Error(name + " is not valid JSON");
  }
  if (array ? !Array.isArray(v) : typeof v !== "object" || v === null || Array.isArray(v)) throw new Error(name + (array ? " must be a JSON list" : " must be a JSON object"));
  return v;
}

const fixedLabels: Record<string, string> = {
  binary: "Binary", network: "Docker network", namespace: "Namespace", service_account: "Service account",
  automount_service_account_token: "Kubernetes API token in pods", cluster: "Cluster connection", env: "Passed-through env vars",
};
const names: Record<string, string> = { local: "Local", docker: "Docker", kubernetes: "Kubernetes" };

type Form = {
  version: string; cpu: string; memory: string; ttl: string; priority: string;
  nodeSelector: string; labels: string; annotations: string; tolerations: string; affinity: string;
  caKind: "" | "config_map" | "secret"; caName: string; caKey: string;
};

function toForm(s: Settings): Form {
  const ca = s.ca_bundle ?? {};
  return {
    version: s.version ?? "", cpu: s.cpu ?? "", memory: s.memory ?? "", ttl: s.ttl ?? "", priority: s.priority_class_name ?? "",
    nodeSelector: toLines(s.node_selector), labels: toLines(s.pod_labels), annotations: toLines(s.pod_annotations),
    tolerations: toJSON(s.tolerations), affinity: toJSON(s.affinity),
    caKind: ca.config_map ? "config_map" : ca.secret ? "secret" : "", caName: ca.config_map || ca.secret || "", caKey: ca.key ?? "",
  };
}

function toSettings(f: Form): Settings {
  return {
    version: f.version.trim(), cpu: f.cpu.trim(), memory: f.memory.trim(), ttl: f.ttl.trim(), priority_class_name: f.priority.trim(),
    node_selector: fromLines("Node selector", f.nodeSelector), pod_labels: fromLines("Pod labels", f.labels), pod_annotations: fromLines("Pod annotations", f.annotations),
    tolerations: fromJSON("Tolerations", f.tolerations, true) as Settings["tolerations"],
    affinity: fromJSON("Affinity", f.affinity, false) as Settings["affinity"],
    ca_bundle: f.caKind ? { [f.caKind]: f.caName.trim(), key: f.caKey.trim() } : {},
  };
}

export default function Executor() {
  const toast = useToast();
  const [v, setV] = useState<View | null>(null);
  const [f, setF] = useState<Form | null>(null);
  const [busy, setBusy] = useState(false);
  const show = (nv: View) => {
    setV(nv);
    setF(toForm(nv.settings));
  };
  useEffect(() => {
    get<View>("/admin/api/executor").then(show).catch((e) => toast(errMsg(e), true));
  }, []);
  if (!v || !f) return <Empty>Loading…</Empty>;
  const set = (k: keyof Form) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  const kube = v.type === "kubernetes";
  const dirty = JSON.stringify(f) !== JSON.stringify(toForm(v.settings));
  const save = async () => {
    let body: Settings;
    try {
      body = toSettings(f);
    } catch (e) {
      toast(errMsg(e), true);
      return;
    }
    setBusy(true);
    try {
      show(await put<View>("/admin/api/executor", body));
      toast("Saved: the next review uses these settings");
    } catch (e) {
      toast(errMsg(e), true);
    } finally {
      setBusy(false);
    }
  };

  const fixed = Object.entries(v.fixed);
  return (
    <section className="grid max-w-4xl gap-4">
      <Card>
        <CardHeader>
          <CardTitle className="h2 flex flex-wrap items-center gap-2">
            Reviews run with {names[v.type] ?? v.type}
            {!v.licensed && <Badge variant="destructive">paused: needs a license</Badge>}
          </CardTitle>
          <CardDescription>
            {v.type === "local"
              ? "Each review runs the codereview binary on the console host. There is nothing to change here; switching to Docker or Kubernetes is done in the config file."
              : "Where reviews run and what they can reach is set in the config file (below, read-only). The settings further down are managed here and apply to the next review, without a restart."}
          </CardDescription>
        </CardHeader>
        {(fixed.length > 0 || v.image_repo) && (
          <CardContent>
            <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[max-content_1fr]">
              {v.image_repo && (
                <>
                  <dt className="text-muted-foreground">Image repository</dt>
                  <dd><code className="text-xs break-all">{v.image_repo}</code></dd>
                </>
              )}
              {fixed.map(([k, val]) => (
                <div key={k} className="contents">
                  <dt className="text-muted-foreground">{fixedLabels[k] ?? k}</dt>
                  <dd>{k === "env" || k === "service_account" || k === "namespace" ? <code className="text-xs break-all">{val}</code> : val}</dd>
                </div>
              ))}
            </dl>
          </CardContent>
        )}
      </Card>

      {v.editable && (
        <Card>
          <CardHeader>
            <CardTitle className="h2">Review {kube ? "pods" : "containers"}</CardTitle>
            <CardDescription>
              {v.source === "config" ? "Showing the config file's values; saving stores them in the console." : "Saved in the console; the config file's values for these are ignored."}
            </CardDescription>
          </CardHeader>
          <CardContent className="grid gap-5">
            <FormGrid>
              <Field label="Version" className="lg:col-span-3" hint={<Sub>The image tag (or a sha256: digest). The console needs code-review-bot {v.min_cli_version} or later. Now running <code className="break-all">{v.image}</code>.</Sub>}>
                <div className="flex min-w-0 items-center gap-1.5">
                  <code className="hidden shrink truncate text-xs text-muted-foreground sm:block">{v.image_repo}:</code>
                  <Input value={f.version} onChange={set("version")} placeholder="0.5.0" spellCheck={false} className="sm:max-w-48" />
                </div>
              </Field>
              <Field label="CPU" hint={<Sub>{kube ? "Request and limit, e.g. 500m or 1." : "docker --cpus, e.g. 1.5."} Empty = no limit.</Sub>}>
                <Input value={f.cpu} onChange={set("cpu")} placeholder={kube ? "500m" : "1"} />
              </Field>
              <Field label="Memory" hint={<Sub>{kube ? "Request and limit, e.g. 1Gi." : "docker --memory, e.g. 2g."} Empty = no limit.</Sub>}>
                <Input value={f.memory} onChange={set("memory")} placeholder={kube ? "1Gi" : "2g"} />
              </Field>
              {kube && (
                <Field label="Keep finished jobs for" hint={<Sub>For debugging with kubectl, e.g. 10m or 1h.</Sub>}>
                  <Input value={f.ttl} onChange={set("ttl")} placeholder="10m" />
                </Field>
              )}
            </FormGrid>

            {kube && (
              <>
                <FormGrid className="lg:grid-cols-2">
                  <Field label="Node selector" hint={<Sub>One key=value per line.</Sub>}>
                    <Textarea rows={3} value={f.nodeSelector} onChange={set("nodeSelector")} placeholder="workload=batch" spellCheck={false} className="font-mono text-xs" />
                  </Field>
                  <Field label="Priority class" hint={<Sub>An existing PriorityClass, e.g. for low-priority batch work.</Sub>}>
                    <Input value={f.priority} onChange={set("priority")} placeholder="batch-low" />
                  </Field>
                  <Field label="Pod labels" hint={<Sub>key=value per line. The console's own labels win on a clash.</Sub>}>
                    <Textarea rows={3} value={f.labels} onChange={set("labels")} placeholder="team=platform" spellCheck={false} className="font-mono text-xs" />
                  </Field>
                  <Field label="Pod annotations" hint={<Sub>key=value per line, e.g. karpenter.sh/do-not-disrupt=true.</Sub>}>
                    <Textarea rows={3} value={f.annotations} onChange={set("annotations")} spellCheck={false} className="font-mono text-xs" />
                  </Field>
                  <Field label="Tolerations (JSON)" hint={<Sub>A list in the Kubernetes format.</Sub>}>
                    <Textarea rows={5} value={f.tolerations} onChange={set("tolerations")} placeholder='[{"key": "spot", "operator": "Exists", "effect": "NoSchedule"}]' spellCheck={false} className="font-mono text-xs" />
                  </Field>
                  <Field label="Affinity (JSON)" hint={<Sub>An object in the Kubernetes format (nodeAffinity, podAntiAffinity…).</Sub>}>
                    <Textarea rows={5} value={f.affinity} onChange={set("affinity")} placeholder='{"nodeAffinity": {…}}' spellCheck={false} className="font-mono text-xs" />
                  </Field>
                </FormGrid>
                <FormGrid>
                  <Field label="Extra CA certificates" hint={<Sub>For a code host or model endpoint behind a private CA. Trusted in addition to the system CAs.</Sub>}>
                    <NativeSelect value={f.caKind} onChange={set("caKind")}>
                      <option value="">None</option>
                      <option value="config_map">From a ConfigMap</option>
                      <option value="secret">From a Secret</option>
                    </NativeSelect>
                  </Field>
                  {f.caKind && (
                    <>
                      <Field label={f.caKind === "secret" ? "Secret name" : "ConfigMap name"} hint={<Sub>In the {v.fixed.namespace} namespace.</Sub>}>
                        <Input value={f.caName} onChange={set("caName")} placeholder="corp-ca" />
                      </Field>
                      <Field label="Key" hint={<Sub>The PEM file inside it.</Sub>}>
                        <Input value={f.caKey} onChange={set("caKey")} placeholder="ca.crt" />
                      </Field>
                    </>
                  )}
                </FormGrid>
              </>
            )}
            <div className="flex gap-2">
              <Button onClick={save} disabled={busy || (!dirty && v.source === "console")}>Save settings</Button>
              {dirty && <Button variant="outline" onClick={() => setF(toForm(v.settings))}>Discard changes</Button>}
            </div>
          </CardContent>
        </Card>
      )}
      <Note>
        The executor type, the image repository, the cluster connection, namespace and service account stay in the config file: they decide what review jobs, which carry the code host tokens and model keys, can reach. Changing them needs a restart.
      </Note>
    </section>
  );
}
