import { useEffect, useState } from "react";
import { get, post } from "../api";
import { fmtDateTime, utcTitle } from "../format";
import { Empty, errMsg, Note, Panel, StateBadge, Sub, useMe, useRefreshMe, useToast } from "../ui";
import { Changes } from "./Audit";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "../ui";

type Request = {
  id: number; method: string; path: string; summary: string; preview: unknown; requested_by: string; requested_at: string;
  status: "pending" | "applying" | "applied" | "failed" | "rejected" | "cancelled" | "expired"; decided_by?: string; decided_at?: string; result?: string;
};

const kind = (s: Request["status"]) => (s === "applied" ? "on" : s === "pending" || s === "applying" ? "warn" : s === "failed" ? "off" : "idle");

// ChangeRequests lists the changes operators asked for. Admins approve
// (the change then runs as the operator, approved by them) or reject them;
// operators see their own and can cancel them. (The bot approving merge
// requests is something else: see Settings and Reviews.)
export default function ChangeRequests() {
  const toast = useToast();
  const me = useMe();
  const refreshMe = useRefreshMe();
  const [status, setStatus] = useState("pending");
  const [list, setList] = useState<Request[] | null>(null);
  const [reasons, setReasons] = useState<Record<number, string>>({});
  const [shown, setShown] = useState<{ summary: string; response: unknown } | null>(null);
  const admin = me.role === "admin";
  const load = (s = status) => get<Request[]>("/admin/api/approvals" + (s === "all" ? "" : "?status=" + s)).then(setList).catch((e) => toast(errMsg(e), true));
  useEffect(() => { load(); }, [status]);
  const act = async (r: Request, verb: "approve" | "reject" | "cancel") => {
    try {
      const res = await post<{ request?: Request; response?: unknown }>(`/admin/api/approvals/${r.id}/${verb}`, verb === "reject" ? { reason: reasons[r.id] || "" } : undefined);
      if (verb === "approve") {
        const st = res.request?.status;
        toast(st === "applied" ? "Applied: " + r.summary : "Failed: " + (res.request?.result || ""), st !== "applied");
        // A new webhook secret is shown once: to the approving admin.
        if (st === "applied" && res.response && JSON.stringify(res.response).includes('"secret"')) setShown({ summary: r.summary, response: res.response });
      } else {
        toast(verb === "reject" ? "Rejected" : "Cancelled");
      }
      load();
      refreshMe();
    } catch (e) {
      toast(errMsg(e), true);
    }
  };
  const visible = (list || []).filter((r) => admin || r.requested_by === me.email);
  return (
    <section className="grid gap-4">
      <Note>
        {admin
          ? "Operators change repositories, rules and learnings and run reviews directly. Their other changes (code hosts, webhooks, settings, models, the executor, the license) wait here for an admin. Approving runs the change as the operator, with the same checks, and the audit log names both of you. Requests expire after 7 days. Merge requests the bot approved are marked on Reviews."
          : "Changes to code hosts, webhooks, settings, models, the executor and the license wait for an admin. Your requests are listed here; they expire after 7 days."}
      </Note>
      {shown && (
        <Panel>
          <div className="grid gap-2 p-4">
            <div className="font-medium">{shown.summary}: copy the new secret now, it isn't shown again</div>
            <pre className="max-h-60 overflow-auto rounded-md bg-muted p-2 font-mono text-xs whitespace-pre-wrap break-all">{JSON.stringify(shown.response, null, 2)}</pre>
            <div><Button variant="outline" size="sm" onClick={() => setShown(null)}>Done</Button></div>
          </div>
        </Panel>
      )}
      <div className="flex items-center gap-2">
        <NativeSelect className="w-48" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="pending">Waiting</option>
          <option value="all">All requests</option>
        </NativeSelect>
      </div>
      <Panel>
        {!list ? (
          <Empty>Loading…</Empty>
        ) : !visible.length ? (
          <Empty>{status === "pending" ? "No change requests wait for an admin." : "No requests yet."}</Empty>
        ) : (
          <ul className="divide-y">
            {visible.map((r) => (
              <li key={r.id} className="grid gap-2 p-4 md:grid-cols-[1fr_minmax(16rem,28rem)]">
                <div className="grid gap-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{r.summary}</span>
                    <StateBadge kind={kind(r.status)}>{r.status === "pending" ? "waiting" : r.status}</StateBadge>
                  </div>
                  <Sub>
                    asked by {r.requested_by} · <span title={utcTitle(r.requested_at)}>{fmtDateTime(r.requested_at)}</span>
                    {r.decided_by && r.status !== "pending" && <> · {r.status} by {r.decided_by}{r.decided_at ? " " + fmtDateTime(r.decided_at) : ""}</>}
                  </Sub>
                  {r.result && r.status !== "applied" && <Sub className={r.status === "failed" ? "text-destructive" : ""}>{r.result}</Sub>}
                  {r.status === "pending" && (
                    <div className="mt-1 flex flex-wrap items-center gap-2">
                      {admin && <Button size="sm" onClick={() => act(r, "approve")}>Approve</Button>}
                      {admin && (
                        <>
                          <Input className="h-8 max-w-60" placeholder="Reason (optional)" value={reasons[r.id] || ""} onChange={(e) => setReasons({ ...reasons, [r.id]: e.target.value })} />
                          <Button size="sm" variant="outline" onClick={() => act(r, "reject")}>Reject</Button>
                        </>
                      )}
                      {r.requested_by === me.email && <Button size="sm" variant="ghost" onClick={() => act(r, "cancel")}>Cancel request</Button>}
                    </div>
                  )}
                </div>
                <div className="min-w-0"><Changes detail={r.preview} /></div>
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </section>
  );
}
