import { useEffect, useState } from "react";
import { get } from "../api";
import { Empty, errMsg, Locked, Panel, useFeature, useToast } from "../ui";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Entry = { id: number; at: string; actor: string; action: string; target: string; detail: unknown };

export default function Audit() {
  const toast = useToast();
  const [list, setList] = useState<Entry[] | null>(null);
  const licensed = useFeature("audit");
  useEffect(() => {
    if (licensed) get<Entry[]>("/admin/api/audit").then((l) => setList(l || [])).catch((e) => toast(errMsg(e), true));
  }, [licensed]);
  if (!licensed)
    return (
      <section>
        <Locked feature="audit">Every admin change is still recorded; viewing the audit log is part of the licensed console.</Locked>
      </section>
    );
  return (
    <section className="grid gap-4">
      <Panel>
        {!list ? (
          <Empty>Loading…</Empty>
        ) : !list.length ? (
          <Empty>No admin activity yet.</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">When</TableHead>
                <TableHead>Who</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Target</TableHead>
                <TableHead className="pr-4">Details</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((e, i) => (
                <TableRow key={e.id ?? i}>
                  <TableCell className="pl-4 align-top text-muted-foreground">{new Date(e.at).toLocaleString()}</TableCell>
                  <TableCell className="align-top">{e.actor}</TableCell>
                  <TableCell className="align-top">
                    <code>{e.action}</code>
                  </TableCell>
                  <TableCell className="align-top">{e.target}</TableCell>
                  <TableCell className="max-w-xl pr-4 align-top whitespace-normal">
                    <pre className="detail max-h-40 overflow-auto rounded-md bg-muted p-2 font-mono text-xs whitespace-pre-wrap break-all">{JSON.stringify(e.detail)}</pre>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
    </section>
  );
}
