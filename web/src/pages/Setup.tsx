import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { ArrowRight, Check, Circle, RefreshCw } from "lucide-react";
import { get } from "../api";
import { Empty, errMsg, Note, useRefreshMe, useToast } from "../ui";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

type Step = { id: string; title: string; help: string; done: boolean; optional?: boolean; detail?: string; link?: string };
type Status = { steps: Step[]; done: number; total: number; complete: boolean };

const linkLabel: Record<string, string> = {
  "/license": "Open License",
  "/hosts": "Open Code hosts",
  "/webhooks": "Open Webhooks",
  "/settings": "Open Settings",
  "/repositories": "Open Repositories",
  "/reviews": "Open Reviews",
};

// Setup is the guided checklist: each step is checked against live data,
// so it shows what is done, what is next, and why a step isn't done yet.
export default function Setup() {
  const toast = useToast();
  const refreshMe = useRefreshMe();
  const [st, setSt] = useState<Status | null>(null);
  const last = useRef("");
  const load = () =>
    get<Status>("/admin/api/setup")
      .then((s) => {
        setSt(s);
        const key = s.done + "/" + s.complete;
        if (last.current !== key) {
          if (last.current !== "") refreshMe(); // keep the sidebar badge in step
          last.current = key;
        }
      })
      .catch((e) => toast(errMsg(e), true));
  useEffect(() => {
    load();
    const t = window.setInterval(load, 15000); // pick up deliveries and reviews as they happen
    return () => window.clearInterval(t);
  }, []);
  if (!st) return <Empty>Loading…</Empty>;
  const next = st.steps.find((s) => !s.done && !s.optional);
  const pct = st.total ? Math.round((st.done / st.total) * 100) : 100;
  return (
    <section className="grid max-w-3xl gap-4">
      <Card>
        <CardHeader>
          <CardTitle className="h2">{st.complete ? "All set" : "Get your first review"}</CardTitle>
          <CardDescription>
            {st.complete
              ? "Everything needed for reviews is in place. This page stays here for when you add more."
              : `${st.done} of ${st.total} steps done. Each step is checked against what is configured right now.`}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3">
          <div className="h-2 overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label="Setup progress">
            <div className="h-full rounded-full bg-primary transition-all" style={{ width: pct + "%" }} />
          </div>
          <div>
            <Button variant="outline" size="sm" onClick={load}>
              <RefreshCw /> Check again
            </Button>
          </div>
        </CardContent>
      </Card>

      <ol className="grid gap-3">
        {st.steps.map((s, i) => {
          const isNext = next?.id === s.id;
          return (
            <li key={s.id}>
              <Card className={cn("gap-3 py-4", isNext && "border-primary ring-1 ring-primary/40")}>
                <CardContent className="flex gap-3 px-4">
                  <div className={cn("mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full border text-xs font-semibold", s.done ? "border-success bg-success-soft text-success" : isNext ? "border-primary text-primary" : "text-muted-foreground")} aria-hidden>
                    {s.done ? <Check className="size-4" /> : isNext ? i + 1 : <Circle className="size-3" />}
                  </div>
                  <div className="grid min-w-0 flex-1 gap-1.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{s.title}</span>
                      {s.optional && <Badge variant="outline" className="font-normal text-muted-foreground">optional</Badge>}
                      {isNext && <Badge>next</Badge>}
                      <span className="sr-only">{s.done ? "done" : "not done"}</span>
                    </div>
                    {s.detail && <div className={cn("text-sm", s.done ? "text-success" : /rejected|failed|expired|not valid|won't work/.test(s.detail) ? "text-destructive" : "text-muted-foreground")}>{s.detail}</div>}
                    {(!s.done || isNext) && <p className="text-sm leading-relaxed text-muted-foreground">{s.help}</p>}
                    {s.link && !s.done && (
                      <div>
                        <Button asChild size="sm" variant={isNext ? "default" : "outline"}>
                          <Link to={s.link}>
                            {linkLabel[s.link] || "Open"} <ArrowRight />
                          </Link>
                        </Button>
                      </div>
                    )}
                  </div>
                </CardContent>
              </Card>
            </li>
          );
        })}
      </ol>
      <Note>Steps update on their own every 15 seconds while this page is open, so you can set up the webhook on the code host and watch it turn green.</Note>
    </section>
  );
}
