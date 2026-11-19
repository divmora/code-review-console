import { useEffect, useState } from "react";
import { get } from "../api";
import { Empty, errMsg, useToast } from "../ui";

type Entry = { id: number; at: string; actor: string; action: string; target: string; detail: unknown };

export default function Audit() {
  const toast = useToast();
  const [list, setList] = useState<Entry[] | null>(null);
  useEffect(() => {
    get<Entry[]>("/admin/api/audit").then((l) => setList(l || [])).catch((e) => toast(errMsg(e), true));
  }, []);
  return (
    <section>
      <div className="panel">
        {!list ? (
          <Empty>Loading…</Empty>
        ) : !list.length ? (
          <Empty>No admin activity yet.</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>Who</th>
                <th>Action</th>
                <th>Target</th>
                <th>Details</th>
              </tr>
            </thead>
            <tbody>
              {list.map((e, i) => (
                <tr key={e.id ?? i}>
                  <td className="muted">{new Date(e.at).toLocaleString()}</td>
                  <td>{e.actor}</td>
                  <td>{e.action}</td>
                  <td>{e.target}</td>
                  <td>
                    <pre className="detail">{JSON.stringify(e.detail)}</pre>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </section>
  );
}
