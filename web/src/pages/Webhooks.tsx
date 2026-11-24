import { useEffect, useState } from "react";
import { useQueryState } from "../urlstate";
import { RefreshCw, Search, X } from "lucide-react";
import { get, qs } from "../api";
import { ago, fmtDateTime, utcTitle } from "../format";
import { Empty, errMsg, NativeSelect, Note, Panel, StateBadge, Sub, Toolbar, useToast } from "../ui";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Hook = {
  id: number; received_at: string; platform: string; host: string; project: string; event: string; kind: string;
  action: string; number: number; sender: string; outcome: string; reason: string; auth: string; endpoint: string; source_ip?: string;
};
const outcomes = ["queued", "ignored", "duplicate", "rejected", "error"];
const badge = (o: string) => ({ queued: "on", rejected: "off", error: "off" } as Record<string, "on" | "off">)[o] || "idle";

export default function Webhooks() {
  const toast = useToast();
  const [hooks, setHooks] = useState<Hook[] | null>(null);
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [more, setMore] = useState(false);
  // endpoint: one webhook's deliveries (the link from Code hosts).
  const [q, setQ] = useQueryState({ project: "", outcome: "", endpoint: "" });
  const endpoint = q.endpoint;
  const [project, setProject] = useState(q.project);
  const outcome = q.outcome;
  const setOutcome = (v: string) => setQ({ outcome: v });
  // Webhook names by ID, to show which URL a delivery came in on.
  const [hookNames, setHookNames] = useState<Record<string, string>>({});
  useEffect(() => {
    get<{ hosts: { webhooks?: { id: string; name: string }[] }[] }>("/admin/api/hosts")
      .then((d) => setHookNames(Object.fromEntries(d.hosts.flatMap((h) => (h.webhooks || []).map((w) => [w.id, w.name])))))
      .catch(() => {});
  }, []);
  const load = (older?: boolean) => {
    const before = older && hooks?.length ? hooks[hooks.length - 1].id : undefined;
    get<{ events: Hook[]; counts_24h: Record<string, number> }>("/admin/api/webhooks" + qs({ limit: 100, project: project.trim(), outcome, endpoint, before }))
      .then((d) => {
        setHooks((h) => (older ? (h || []).concat(d.events) : d.events));
        setCounts(d.counts_24h || {});
        setMore(d.events.length >= 100);
      })
      .catch((e) => toast(errMsg(e), true));
  };
  useEffect(() => {
    const t = window.setTimeout(() => (load(), setQ({ project: project.trim() })), project ? 250 : 0);
    return () => window.clearTimeout(t);
  }, [project, outcome, endpoint]);
  useEffect(() => {
    if (q.project !== project.trim()) setProject(q.project); // the URL changed (a link, Back)
  }, [q.project]);
  return (
    <section className="grid gap-4">
      <Toolbar>
        <div className="relative w-full sm:w-64">
          <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
          <Input type="search" className="pl-8" placeholder="Filter by project" aria-label="Filter webhooks by project" value={project} onChange={(e) => setProject(e.target.value)} />
        </div>
        <NativeSelect className="w-auto" aria-label="Outcome" value={outcome} onChange={(e) => setOutcome(e.target.value)}>
          <option value="">All outcomes</option>
          {outcomes.map((o) => (
            <option key={o} value={o}>
              {o[0].toUpperCase() + o.slice(1)}
            </option>
          ))}
        </NativeSelect>
        {endpoint && (
          <Button variant="outline" size="sm" onClick={() => setQ({ endpoint: "" })} aria-label="Show all webhooks">
            Webhook: {hookNames[endpoint] || "unknown"} <X />
          </Button>
        )}
        <span className="text-sm text-muted-foreground">Last 24 h, all webhooks: {outcomes.map((k) => (counts[k] || 0) + " " + k).join(" · ")}</span>
        <span className="flex-1" />
        <Button variant="outline" onClick={() => load()}>
          <RefreshCw /> Refresh
        </Button>
      </Toolbar>
      <Panel>
        {!hooks ? (
          <Empty>Loading…</Empty>
        ) : !hooks.length ? (
          <Empty>No webhook deliveries match.</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">When</TableHead>
                <TableHead>Project</TableHead>
                <TableHead>Event</TableHead>
                <TableHead>Outcome</TableHead>
                <TableHead>Reason</TableHead>
                <TableHead className="pr-4">Via</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {hooks.map((e) => {
                const what = [e.kind || e.event, e.action].filter(Boolean).join(" · ") + (e.number ? " · !" + e.number : "");
                return (
                  <TableRow key={e.id}>
                    <TableCell className="pl-4 align-top text-muted-foreground" title={fmtDateTime(e.received_at, true) + " · " + utcTitle(e.received_at)}>
                      {ago(e.received_at)}
                    </TableCell>
                    <TableCell className="align-top">
                      <div className="font-medium">{e.project || "—"}</div>
                      <Sub>{e.platform + (e.host ? " · " + e.host : "")}</Sub>
                    </TableCell>
                    <TableCell className="align-top">
                      <div>{what || "—"}</div>
                      <Sub>{e.event + (e.sender ? " · by " + e.sender : "")}</Sub>
                    </TableCell>
                    <TableCell className="align-top">
                      <StateBadge kind={badge(e.outcome)}>{e.outcome}</StateBadge>
                    </TableCell>
                    <TableCell className="reason min-w-64 align-top text-sm whitespace-normal text-muted-foreground">{e.reason}</TableCell>
                    <TableCell className="pr-4 align-top text-muted-foreground">
                      <div>{e.endpoint ? hookNames[e.endpoint] || "unknown webhook" : "shared URL (removed)"}</div>
                      <Sub className={e.auth === "token-unsigned" ? "text-warning" : ""} title={e.auth === "token-unsigned" ? "Accepted by the secret token although a signing token is set: the delivery wasn't signed" : undefined}>
                        {e.auth === "token-unsigned" ? "secret token, not signed" : e.auth}{e.source_ip ? " · from " + e.source_ip : ""}
                      </Sub>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </Panel>
      {more && (
        <div>
          <Button variant="outline" onClick={() => load(true)}>
            Load older
          </Button>
        </div>
      )}
      <Note>
        Every webhook delivery and what the console did with it. <b>Ignored</b> is normal for events that should not start a review (approvals, label or reviewer changes, edits without new commits) and for repositories that are not enabled. <b>Rejected</b> means the signature or token did not match. History is kept for the days set in Settings (14 by default).
      </Note>
    </section>
  );
}
