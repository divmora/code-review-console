import { FormEvent, useEffect, useState } from "react";
import { get } from "../api";
import { fmtDateTime, utcTitle } from "../format";
import { Empty, errMsg, Field, Locked, Panel, shortAgent, Sub, useFeature, useToast } from "../ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Entry = { id: number; at: string; actor: string; action: string; target: string; detail: unknown; ip?: string; user_agent?: string };
type Page = { entries: Entry[]; next: number };
type Filter = { actor: string; action: string; target: string; q: string; from: string; to: string };

const empty: Filter = { actor: "", action: "", target: "", q: "", from: "", to: "" };

// Actions the console records, for the action filter's suggestions.
const actions = [
  "login", "login.denied", "logout", "session.*", "session.ended", "session.revoke", "setting.put",
  "host.*", "host.put", "host.delete", "webhook.*", "webhook.put", "webhook.delete", "repo.*", "rule.*",
  "learning.*", "model.*", "audit.export",
];

function query(f: Filter, before?: number) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(f)) if (v.trim()) q.set(k, v.trim());
  if (before) q.set("before", String(before));
  return q.toString();
}

// flat turns {"config": {"skip_tls_verify": true}} into {"config.skip_tls_verify": "true"}.
function flat(v: unknown, prefix = "", out: Record<string, string> = {}): Record<string, string> {
  if (v && typeof v === "object" && !Array.isArray(v)) {
    for (const [k, x] of Object.entries(v as Record<string, unknown>)) flat(x, prefix ? prefix + "." + k : k, out);
    if (!Object.keys(v as object).length && prefix) out[prefix] = "{}";
  } else if (prefix) {
    out[prefix] = v === undefined ? "" : typeof v === "string" ? v : JSON.stringify(v);
  }
  return out;
}

const secretLabel = (s: string) => {
  const [name, change] = s.split(":");
  return name.replace(/_/g, " ") + " " + (change || "changed");
};

// Changes shows what an entry changed: only the fields whose value differs
// (old → new), the secrets changed by name, or the values for other
// entries; the raw JSON is one click away.
function Changes({ detail }: { detail: unknown }) {
  const [raw, setRaw] = useState(false);
  const d = (detail && typeof detail === "object" ? detail : {}) as Record<string, unknown>;
  const hasDiff = "before" in d || "after" in d;
  const secrets = Array.isArray(d.secrets_changed) ? (d.secrets_changed as string[]) : [];
  let rows: { key: string; old?: string; now?: string }[] = [];
  let note = "";
  if (hasDiff) {
    const before = flat(d.before), after = flat(d.after);
    if (d.before == null && "after" in d) note = "created";
    if (!("after" in d)) note = "deleted";
    for (const k of Array.from(new Set([...Object.keys(before), ...Object.keys(after)])).sort()) {
      if (before[k] !== after[k]) rows.push({ key: k, old: d.before == null ? undefined : before[k], now: "after" in d ? after[k] : undefined });
    }
  } else {
    rows = Object.entries(flat(detail)).map(([key, now]) => ({ key, now }));
  }
  return (
    <div className="grid gap-1 text-xs">
      {note && <span className="text-muted-foreground">{note}</span>}
      {rows.length === 0 && !secrets.length && !note && <span className="text-muted-foreground">{hasDiff ? "no visible change" : "—"}</span>}
      {rows.slice(0, 20).map((r) => (
        <div key={r.key} className="break-all">
          <span className="font-medium">{r.key}</span>
          {": "}
          {r.old !== undefined && <span className="text-destructive line-through">{r.old || "∅"}</span>}
          {r.old !== undefined && r.now !== undefined && " → "}
          {r.now !== undefined && <span className={hasDiff ? "text-success" : ""}>{r.now || "∅"}</span>}
        </div>
      ))}
      {rows.length > 20 && <span className="text-muted-foreground">and {rows.length - 20} more (see raw)</span>}
      {secrets.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {secrets.map((s) => <Badge key={s} variant="outline" className="font-normal">{secretLabel(s)}</Badge>)}
        </div>
      )}
      <div>
        <button type="button" className="text-muted-foreground underline-offset-2 hover:underline" onClick={() => setRaw(!raw)}>{raw ? "Hide raw" : "Raw"}</button>
      </div>
      {raw && <pre className="max-h-60 overflow-auto rounded-md bg-muted p-2 font-mono whitespace-pre-wrap break-all">{JSON.stringify(detail, null, 2)}</pre>}
    </div>
  );
}

export default function Audit() {
  const toast = useToast();
  const licensed = useFeature("audit");
  const [form, setForm] = useState<Filter>(empty);
  const [applied, setApplied] = useState<Filter>(empty);
  const [list, setList] = useState<Entry[] | null>(null);
  const [next, setNext] = useState(0);
  const [loading, setLoading] = useState(false);
  const load = (f: Filter, before?: number) => {
    setLoading(true);
    get<Page>("/admin/api/audit?" + query(f, before))
      .then((p) => {
        setList((cur) => (before && cur ? [...cur, ...p.entries] : p.entries));
        setNext(p.next);
      })
      .catch((e) => toast(errMsg(e), true))
      .finally(() => setLoading(false));
  };
  useEffect(() => {
    if (licensed) load(empty);
  }, [licensed]);
  if (!licensed)
    return (
      <section>
        <Locked feature="audit">Every admin change is still recorded; viewing the audit log is part of the licensed console.</Locked>
      </section>
    );
  const set = (k: keyof Filter) => (e: { target: { value: string } }) => setForm({ ...form, [k]: e.target.value });
  const apply = (e?: FormEvent) => {
    e?.preventDefault();
    setApplied(form);
    load(form);
  };
  const clear = () => {
    setForm(empty);
    setApplied(empty);
    load(empty);
  };
  const exportURL = (format: string) => "/admin/api/audit/export?" + query(applied) + (query(applied) ? "&" : "") + "format=" + format;
  return (
    <section className="grid gap-4">
      <Panel>
        <form onSubmit={apply} className="grid gap-3 p-4 sm:grid-cols-2 lg:grid-cols-6">
          <Field label="Who"><Input placeholder="email" value={form.actor} onChange={set("actor")} /></Field>
          <Field label="Action">
            <Input list="audit-actions" placeholder="host.* or login.denied" value={form.action} onChange={set("action")} />
            <datalist id="audit-actions">{actions.map((a) => <option key={a} value={a} />)}</datalist>
          </Field>
          <Field label="Target"><Input placeholder="gitlab:group/project" value={form.target} onChange={set("target")} /></Field>
          <Field label="Search"><Input placeholder="in targets and details" value={form.q} onChange={set("q")} /></Field>
          <Field label="From"><Input type="date" value={form.from} onChange={set("from")} /></Field>
          <Field label="To"><Input type="date" value={form.to} onChange={set("to")} /></Field>
          <div className="flex flex-wrap items-center gap-2 lg:col-span-6">
            <Button type="submit">Apply</Button>
            <Button type="button" variant="ghost" onClick={clear}>Clear</Button>
            <span className="flex-1" />
            <Button asChild variant="outline" size="sm"><a href={exportURL("csv")}>Export CSV</a></Button>
            <Button asChild variant="outline" size="sm"><a href={exportURL("jsonl")}>Export JSON Lines</a></Button>
          </div>
        </form>
      </Panel>
      <Panel>
        {!list ? (
          <Empty>Loading…</Empty>
        ) : !list.length ? (
          <Empty>{query(applied) ? "Nothing matches these filters." : "No admin activity yet."}</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">When</TableHead>
                <TableHead>Who</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Target</TableHead>
                <TableHead className="pr-4">Changes</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="pl-4 align-top whitespace-nowrap text-muted-foreground" title={utcTitle(e.at)}>{fmtDateTime(e.at, true)}</TableCell>
                  <TableCell className="align-top">
                    {e.actor}
                    {e.ip && <Sub title={e.user_agent}>{e.ip}{e.user_agent ? " · " + shortAgent(e.user_agent) : ""}</Sub>}
                  </TableCell>
                  <TableCell className="align-top"><code>{e.action}</code></TableCell>
                  <TableCell className="max-w-56 align-top break-all whitespace-normal">{e.target}</TableCell>
                  <TableCell className="max-w-xl min-w-64 pr-4 align-top whitespace-normal"><Changes detail={e.detail} /></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {next > 0 && (
          <div className="flex justify-center p-3">
            <Button variant="outline" disabled={loading} onClick={() => load(applied, next)}>{loading ? "Loading…" : "Older entries"}</Button>
          </div>
        )}
      </Panel>
    </section>
  );
}
