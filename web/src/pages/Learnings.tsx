import { useEffect, useState } from "react";
import { ExternalLink, Plus, Search } from "lucide-react";
import { del, get, post, put } from "../api";
import { ago } from "../format";
import { Empty, errMsg, Field, FormGrid, Locked, NativeSelect, Note, Panel, SidePanel, Sub, Toolbar, useConfirm, useFeature, useToast } from "../ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";

type Learning = {
  id: number; platform: string; host: string; scope: string; text: string; path: string;
  status: "active" | "pending" | "disabled"; source: string; source_url: string; source_author: string; source_role?: string; updated_at: string;
};

const sourceLabel: Record<string, string> = { reply: "from a reply", command: "@bot remember", manual: "added here" };

function Editor({ l, onSave, onCancel, onDelete }: { l: Learning; onSave: (b: Partial<Learning>) => void; onCancel: () => void; onDelete: () => void }) {
  const [scope, setScope] = useState(l.scope);
  const [path, setPath] = useState(l.path);
  const [text, setText] = useState(l.text);
  return (
    <div className="grid gap-4">
      <Field label="Applies to">
        <Input value={scope} placeholder="empty = whole host · group · group/repo" onChange={(e) => setScope(e.target.value)} />
      </Field>
      <Field label="Only files matching">
        <Input value={path} placeholder="optional glob" onChange={(e) => setPath(e.target.value)} />
      </Field>
      <Field label="Learning">
        <Textarea rows={5} maxLength={600} value={text} onChange={(e) => setText(e.target.value)} />
      </Field>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => onSave({ scope: scope.trim(), path: path.trim(), text: text.trim() })}>Save</Button>
        <Button variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <span className="flex-1" />
        <Button variant="ghost" className="text-destructive hover:text-destructive" onClick={onDelete}>
          Delete
        </Button>
      </div>
    </div>
  );
}

export default function Learnings() {
  const toast = useToast();
  const confirm = useConfirm();
  const licensed = useFeature("learnings");
  const [list, setList] = useState<Learning[] | null>(null);
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [editing, setEditing] = useState<number | null>(null);
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ platform: "gitlab", host: "", scope: "", path: "", text: "" });

  useEffect(() => {
    get<{ learnings: Learning[] }>("/admin/api/learnings").then((d) => setList(d.learnings || [])).catch((e) => toast(errMsg(e), true));
  }, []);
  const save = (l: Learning, patch: Partial<Learning>) =>
    put<Learning>("/admin/api/learnings/" + l.id, { scope: l.scope, text: l.text, path: l.path, status: l.status, ...patch }).then((x) => {
      setList((ls) => (ls || []).map((y) => (y.id === x.id ? x : y)));
      return x;
    });
  const add = () =>
    post<Learning>("/admin/api/learnings", form)
      .then((l) => {
        setList((ls) => (ls || []).concat([l]));
        setForm({ ...form, text: "" });
        setAdding(false);
        toast("Learning added");
      })
      .catch((e) => toast(errMsg(e), true));
  const remove = async (l: Learning) => {
    if (!(await confirm({ title: "Delete this learning?", description: l.text, action: "Delete", danger: true }))) return;
    del("/admin/api/learnings/" + l.id)
      .then(() => {
        setList((ls) => (ls || []).filter((x) => x.id !== l.id));
        setEditing(null);
        toast("Deleted");
      })
      .catch((e) => toast(errMsg(e), true));
  };

  const counts = { active: 0, pending: 0, disabled: 0 };
  (list || []).forEach((l) => (counts[l.status] += 1));
  const shown = (list || []).filter((l) => (!status || l.status === status) && (!q || (l.text + " " + l.scope + " " + l.host + " " + l.path).toLowerCase().includes(q.toLowerCase())));
  const edited = list?.find((l) => l.id === editing) ?? null;

  return (
    <section className="grid gap-4">
      {!licensed && (
        <Locked feature="learnings">
          Capturing team decisions from review replies, <code>@bot remember</code>, and applying them to reviews are part of the licensed console. Without a license nothing new is learned and existing learnings are not applied.
        </Locked>
      )}
      <Toolbar>
        <div className="relative w-full sm:w-64">
          <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
          <Input type="search" className="pl-8" placeholder="Search learnings" aria-label="Search learnings" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <NativeSelect className="w-auto" aria-label="Status" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">Any status</option>
          <option value="active">Active</option>
          <option value="pending">Waiting for approval</option>
          <option value="disabled">Disabled</option>
        </NativeSelect>
        <span className="text-sm text-muted-foreground">{list && `${counts.active} active · ${counts.pending} waiting · ${counts.disabled} disabled`}</span>
        <span className="flex-1" />
        {licensed && (
          <Button variant="outline" onClick={() => setAdding(true)}>
            <Plus /> Add learning
          </Button>
        )}
      </Toolbar>

      <Panel>
        {!list ? (
          <Empty>Loading…</Empty>
        ) : !shown.length ? (
          <Empty>{list.length ? "No learnings match." : "No learnings yet. Reply to one of the bot's comments with a team decision, or use @bot remember."}</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">Applies to</TableHead>
                <TableHead>Learning</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Source</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {shown.map((l) => (
                <TableRow key={l.id}>
                  <TableCell className="pl-4 align-top">
                    <div>{l.scope ? <code>{l.scope}</code> : <b>Whole host</b>}</div>
                    <Sub>
                      {l.platform} · {l.host}
                    </Sub>
                  </TableCell>
                  <TableCell className="min-w-64 align-top whitespace-normal">
                    <div>{l.text}</div>
                    {l.path && (
                      <Badge variant="outline" className="mt-1 font-normal text-muted-foreground">
                        files: {l.path}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="w-36 align-top">
                    <NativeSelect
                      aria-label="Status"
                      value={l.status}
                      disabled={!licensed}
                      onChange={(e) => save(l, { status: e.target.value as Learning["status"] }).then((x) => toast("Learning " + x.status)).catch((x) => toast(errMsg(x), true))}
                    >
                      <option value="active">Active</option>
                      <option value="pending">Waiting</option>
                      <option value="disabled">Disabled</option>
                    </NativeSelect>
                  </TableCell>
                  <TableCell className="align-top text-muted-foreground">
                    <div>{(sourceLabel[l.source] || l.source) + (l.source_author ? " · " + l.source_author : "") + (l.source_role ? " (" + l.source_role + ")" : "")}</div>
                    {l.source_url && (
                      <a className="inline-flex items-center gap-1 text-xs hover:text-foreground hover:underline" href={l.source_url} target="_blank" rel="noopener noreferrer">
                        open PR <ExternalLink className="size-3" />
                      </a>
                    )}
                    <Sub>{ago(l.updated_at)}</Sub>
                  </TableCell>
                  <TableCell className="pr-4 text-right align-top">
                    {licensed ? (
                      <Button variant="ghost" size="sm" onClick={() => setEditing(l.id)}>
                        Edit
                      </Button>
                    ) : (
                      <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => remove(l)}>
                        Delete
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>

      <Note>
        Learnings are team decisions from review discussions. When someone replies to one of the bot's comments, the bot reacts 👀, asks the model whether the reply states a rule for future reviews, and if so saves it here (✅ on GitLab, 🎉 on GitHub). <code>@bot remember &lt;rule&gt;</code> saves one directly. Each starts scoped to its repository; widen <b>Applies to</b> to a group or the whole host so one standard covers many repositories. Matching active learnings go with every review: the reviewer follows them and the verifier drops findings they rule out.
      </Note>

      <SidePanel open={!!edited} onClose={() => setEditing(null)} title="Edit learning" description={edited ? `${edited.platform} · ${edited.host}` : undefined}>
        {edited && (
          <Editor
            key={edited.id}
            l={edited}
            onCancel={() => setEditing(null)}
            onDelete={() => remove(edited)}
            onSave={(p) =>
              save(edited, p)
                .then(() => {
                  setEditing(null);
                  toast("Saved");
                })
                .catch((x) => toast(errMsg(x), true))
            }
          />
        )}
      </SidePanel>

      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>Add learning</DialogTitle>
            <DialogDescription>A rule future reviews should follow.</DialogDescription>
          </DialogHeader>
          <FormGrid className="lg:grid-cols-2">
            <Field label="Platform">
              <NativeSelect value={form.platform} onChange={(e) => setForm({ ...form, platform: e.target.value })}>
                <option value="gitlab">GitLab</option>
                <option value="github">GitHub</option>
                <option value="bitbucket">Bitbucket</option>
              </NativeSelect>
            </Field>
            <Field label="Host">
              <Input placeholder="gitlab.example.com" value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} />
            </Field>
            <Field label="Applies to">
              <Input placeholder="empty = whole host · group · group/repo" value={form.scope} onChange={(e) => setForm({ ...form, scope: e.target.value })} />
            </Field>
            <Field label="Only files matching">
              <Input placeholder="optional glob, e.g. api/legacy/**" value={form.path} onChange={(e) => setForm({ ...form, path: e.target.value })} />
            </Field>
          </FormGrid>
          <Field label="Learning">
            <Textarea rows={4} maxLength={600} placeholder="e.g. Account numbers are compared without leading zeros on purpose." value={form.text} onChange={(e) => setForm({ ...form, text: e.target.value })} />
          </Field>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAdding(false)}>
              Cancel
            </Button>
            <Button onClick={add}>Add learning</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
