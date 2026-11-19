import { useEffect, useState } from "react";
import { get, qs } from "../api";
import { ago } from "../format";
import { Empty, errMsg, StateBadge, useToast } from "../ui";

type Hook = {
  id: number; received_at: string; platform: string; host: string; project: string; event: string; kind: string;
  action: string; number: number; sender: string; outcome: string; reason: string; auth: string;
};
const outcomes = ["queued", "ignored", "duplicate", "rejected", "error"];
const badge = (o: string) => ({ queued: "on", rejected: "off", error: "off" } as Record<string, "on" | "off">)[o] || "idle";

export default function Webhooks() {
  const toast = useToast();
  const [hooks, setHooks] = useState<Hook[] | null>(null);
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [more, setMore] = useState(false);
  const [project, setProject] = useState("");
  const [outcome, setOutcome] = useState("");
  const load = (older?: boolean) => {
    const before = older && hooks?.length ? hooks[hooks.length - 1].id : undefined;
    get<{ events: Hook[]; counts_24h: Record<string, number> }>("/admin/api/webhooks" + qs({ limit: 100, project: project.trim(), outcome, before }))
      .then((d) => {
        setHooks((h) => (older ? (h || []).concat(d.events) : d.events));
        setCounts(d.counts_24h || {});
        setMore(d.events.length >= 100);
      })
      .catch((e) => toast(errMsg(e), true));
  };
  useEffect(() => {
    const t = window.setTimeout(() => load(), project ? 250 : 0);
    return () => window.clearTimeout(t);
  }, [project, outcome]);
  return (
    <section>
      <div className="bar">
        <input type="search" placeholder="Filter by project" aria-label="Filter webhooks by project" value={project} onChange={(e) => setProject(e.target.value)} />
        <select aria-label="Outcome" value={outcome} onChange={(e) => setOutcome(e.target.value)}>
          <option value="">All outcomes</option>
          {outcomes.map((o) => (
            <option key={o} value={o}>
              {o[0].toUpperCase() + o.slice(1)}
            </option>
          ))}
        </select>
        <span className="sub">Last 24 h: {outcomes.map((k) => (counts[k] || 0) + " " + k).join(" · ")}</span>
        <span className="spacer" />
        <button className="btn" onClick={() => load()}>
          Refresh
        </button>
      </div>
      <div className="panel">
        {!hooks ? (
          <Empty>Loading…</Empty>
        ) : !hooks.length ? (
          <Empty>No webhook deliveries match.</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>Project</th>
                <th>Event</th>
                <th>Outcome</th>
                <th>Reason</th>
                <th>Auth</th>
              </tr>
            </thead>
            <tbody>
              {hooks.map((e) => {
                const what = [e.kind || e.event, e.action].filter(Boolean).join(" · ") + (e.number ? " · !" + e.number : "");
                return (
                  <tr key={e.id}>
                    <td className="muted nowrap" title={new Date(e.received_at).toLocaleString()}>
                      {ago(e.received_at)}
                    </td>
                    <td>
                      <div>{e.project || "—"}</div>
                      <div className="plat">{e.platform + (e.host ? " · " + e.host : "")}</div>
                    </td>
                    <td>
                      <div>{what || "—"}</div>
                      <div className="plat">{e.event + (e.sender ? " · by " + e.sender : "")}</div>
                    </td>
                    <td>
                      <StateBadge kind={badge(e.outcome)}>{e.outcome}</StateBadge>
                    </td>
                    <td className="reason">{e.reason}</td>
                    <td className="muted">{e.auth}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
      {more && (
        <div className="bar mt">
          <button className="btn" onClick={() => load(true)}>
            Load older
          </button>
        </div>
      )}
      <p className="sub">
        Every webhook delivery and what the console did with it. <b>Ignored</b> is normal for events that should not start a review (approvals, label or reviewer changes, edits without new commits) and for repositories that are not enabled. <b>Rejected</b> means the signature or token did not match. History is kept for the days set in Settings (14 by default).
      </p>
    </section>
  );
}
