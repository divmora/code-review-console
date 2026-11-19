import { ComponentProps, createContext, ReactNode, useCallback, useContext, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { CircleAlert, CircleCheck, Lock } from "lucide-react";
import { cn } from "@/lib/utils";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card } from "@/components/ui/card";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import type { Feature, Me, Overrides } from "./types";

// ---- toast ----

type Toast = (msg: string, err?: boolean) => void;
const ToastCtx = createContext<Toast>(() => {});
export const useToast = () => useContext(ToastCtx);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [t, setT] = useState<{ msg: string; err: boolean; n: number } | null>(null);
  const timer = useRef<number>();
  const show = useCallback<Toast>((msg, err = false) => {
    setT((p) => ({ msg, err, n: (p?.n ?? 0) + 1 }));
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setT(null), 4000);
  }, []);
  return (
    <ToastCtx.Provider value={show}>
      {children}
      <div aria-live="polite" className="pointer-events-none fixed right-4 bottom-4 left-4 z-50 flex justify-end sm:left-auto">
        {t && (
          <div
            key={t.n}
            id="toast"
            role="status"
            className={cn(
              "pointer-events-auto flex max-w-md items-start gap-2 rounded-lg border bg-popover px-4 py-3 text-sm text-popover-foreground shadow-lg animate-in fade-in slide-in-from-bottom-2",
              t.err && "border-destructive/40",
            )}
          >
            {t.err ? <CircleAlert className="mt-0.5 size-4 shrink-0 text-destructive" /> : <CircleCheck className="mt-0.5 size-4 shrink-0 text-success" />}
            <span>{t.msg}</span>
          </div>
        )}
      </div>
    </ToastCtx.Provider>
  );
}

// errMsg turns a thrown value into a message for the toast.
export const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));

// ---- confirm dialog ----

type ConfirmOpts = { title: string; description?: ReactNode; action?: string; danger?: boolean };
const ConfirmCtx = createContext<(o: ConfirmOpts) => Promise<boolean>>(async () => false);
// useConfirm returns confirm(opts) → Promise<boolean>, shown as a dialog.
export const useConfirm = () => useContext(ConfirmCtx);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [opts, setOpts] = useState<ConfirmOpts | null>(null);
  const resolver = useRef<(ok: boolean) => void>();
  const confirm = useCallback((o: ConfirmOpts) => {
    setOpts(o);
    return new Promise<boolean>((res) => (resolver.current = res));
  }, []);
  const close = (ok: boolean) => {
    resolver.current?.(ok);
    setOpts(null);
  };
  return (
    <ConfirmCtx.Provider value={confirm}>
      {children}
      <AlertDialog open={!!opts} onOpenChange={(o) => !o && close(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{opts?.title}</AlertDialogTitle>
            {opts?.description && <AlertDialogDescription>{opts.description}</AlertDialogDescription>}
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => close(false)}>Cancel</AlertDialogCancel>
            <AlertDialogAction className={cn(opts?.danger && "bg-destructive text-white hover:bg-destructive/90")} onClick={() => close(true)}>
              {opts?.action ?? "Continue"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </ConfirmCtx.Provider>
  );
}

// ---- signed-in user ----

export const MeCtx = createContext<Me | null>(null);
export const useMe = () => useContext(MeCtx)!;
// RefreshMeCtx reloads /admin/api/me after a change that affects it (settings).
export const RefreshMeCtx = createContext<() => void>(() => {});
export const useRefreshMe = () => useContext(RefreshMeCtx);

// ---- license ----

export const featureTitles: Record<Feature, string> = {
  hosts: "Multiple code hosts and bot identities",
  rules: "Group and pattern rules",
  learnings: "Team learnings",
  executors: "Docker and Kubernetes executors",
  budgets: "Token budgets",
  metrics: "Metrics and long history",
  audit: "Audit log",
  branch_sync: "Branch-sync skip",
  sso_rules: "SSO domain and group rules",
};

// useFeature reports whether the installed license turns on a paid feature.
export function useFeature(f: Feature): boolean {
  return useMe().license.features.includes(f);
}

// Locked explains a paid feature the license does not cover.
export function Locked({ feature, children }: { feature: Feature; children?: ReactNode }) {
  return (
    <Alert role="note" className="locked border-dashed border-primary/60">
      <Lock className="text-primary" />
      <AlertTitle className="flex items-center gap-2">
        {featureTitles[feature]}
        <Badge variant="secondary">licensed</Badge>
      </AlertTitle>
      <AlertDescription>
        <p>
          {children ?? "This is part of the licensed Divmora console."}{" "}
          <Link className="font-medium text-primary underline-offset-4 hover:underline" to="/license">
            Install a license
          </Link>{" "}
          or contact{" "}
          <a className="font-medium text-primary underline-offset-4 hover:underline" href="mailto:licensing@divmora.com">
            licensing@divmora.com
          </a>
          .
        </p>
      </AlertDescription>
    </Alert>
  );
}

// LockNote marks a single paid setting inside a form.
export function LockNote({ feature }: { feature: Feature }) {
  return useFeature(feature) ? null : (
    <span className="locknote block text-xs leading-snug text-warning">
      <Lock className="mr-1 inline size-3 align-[-2px]" />
      Needs a license with {featureTitles[feature].toLowerCase()} ·{" "}
      <Link to="/license" className="underline underline-offset-2">
        License
      </Link>
    </span>
  );
}

// ---- layout pieces ----

// Panel is a card that holds a table (no padding; the table scrolls
// sideways on narrow screens).
export function Panel({ children, className }: { children: ReactNode; className?: string }) {
  return <Card className={cn("gap-0 overflow-hidden py-0", className)}>{children}</Card>;
}

// SidePanel opens an editor or details on the right (full width on phones).
export function SidePanel({ open, onClose, title, description, children, wide }: {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  wide?: boolean;
}) {
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className={cn("w-full gap-0 overflow-y-auto sm:max-w-xl", wide && "sm:max-w-3xl")}>
        <SheetHeader className="border-b pr-10">
          <SheetTitle>{title}</SheetTitle>
          {description && <SheetDescription>{description}</SheetDescription>}
        </SheetHeader>
        <div className="p-4">{children}</div>
      </SheetContent>
    </Sheet>
  );
}

// Toolbar is a wrapping row of filters and actions above a list.
export function Toolbar({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("flex flex-wrap items-center gap-2", className)}>{children}</div>;
}

// Note is muted explanatory text under a section.
export function Note({ children, className }: { children: ReactNode; className?: string }) {
  return <p className={cn("max-w-4xl text-sm leading-relaxed text-muted-foreground", className)}>{children}</p>;
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="px-4 py-10 text-center text-sm text-muted-foreground">{children}</div>;
}

// Field is a labelled form control with an optional hint below it.
export function Field({ label, children, hint, className }: { label: ReactNode; children: ReactNode; hint?: ReactNode; className?: string }) {
  return (
    <div className={cn("grid content-start gap-1.5", className)}>
      <Label className="text-xs font-medium text-muted-foreground">{label}</Label>
      {children}
      {hint}
    </div>
  );
}

// FormGrid lays out fields in responsive columns.
export function FormGrid({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("grid gap-4 sm:grid-cols-2 lg:grid-cols-3", className)}>{children}</div>;
}

// NativeSelect is a styled native <select> (reliable for Inherit/On/Off
// choices, keyboard and screen readers).
export function NativeSelect({ className, ...props }: ComponentProps<"select">) {
  return (
    <select
      className={cn(
        "h-9 w-full min-w-0 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs outline-none transition-[color,box-shadow] dark:bg-input/30",
        "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

type Kind = "on" | "off" | "idle" | "warn";
const kindClass: Record<Kind, string> = {
  on: "border-transparent bg-success-soft text-success",
  off: "border-transparent bg-danger-soft text-destructive",
  idle: "border-transparent bg-muted text-muted-foreground",
  warn: "border-transparent bg-warning-soft text-warning",
};

export function StateBadge({ kind, children }: { kind: Kind; children: ReactNode }) {
  return <Badge className={cn("state", kind, kindClass[kind])}>{children}</Badge>;
}

export function Chips({ items, empty }: { items: string[]; empty?: string }) {
  if (!items.length) return <span className="text-sm text-muted-foreground">{empty ?? "defaults"}</span>;
  return (
    <span className="flex flex-wrap gap-1">
      {items.map((c) => (
        <Badge variant="outline" key={c} className="chip font-normal text-muted-foreground">
          {c}
        </Badge>
      ))}
    </span>
  );
}

// Sub is the small second line under a table cell's main text.
export function Sub({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("mt-0.5 text-xs text-muted-foreground", className)}>{children}</div>;
}

export function settingsChips(st: Overrides = {}): string[] {
  const out: string[] = [];
  if (st.profile) out.push(st.profile);
  if (st.max_comments) out.push("max " + st.max_comments);
  if (st.min_severity) out.push("≥ " + st.min_severity);
  if (st.model) out.push("model " + st.model);
  if (st.auto_review === false) out.push("auto review off");
  if (st.auto_review === true) out.push("auto review on");
  if (st.drafts === true) out.push("drafts");
  if (st.instructions) out.push("instructions");
  return out;
}

// Tri is an Inherit / A / B select bound to boolean | null.
export function Tri({ value, onChange, on, off, inherit = "Inherit", label, disabled }: {
  value: boolean | null | undefined; onChange: (v: boolean | null) => void; on: string; off: string; inherit?: string; label?: string; disabled?: boolean;
}) {
  const v = value === null || value === undefined ? "" : String(value);
  return (
    <NativeSelect aria-label={label} value={v} disabled={disabled} onChange={(e) => onChange(e.target.value === "" ? null : e.target.value === "true")}>
      <option value="">{inherit}</option>
      <option value="true">{on}</option>
      <option value="false">{off}</option>
    </NativeSelect>
  );
}

// ---- review settings editor (repositories, rules, global defaults) ----

export function SettingsEditor({ value, onSave, onCancel, extra, inheritModel = "Server default", saveLabel = "Save settings", danger }: {
  value?: Overrides;
  onSave: (s: Overrides) => void;
  onCancel?: () => void;
  extra?: ReactNode;
  inheritModel?: string;
  saveLabel?: string;
  danger?: ReactNode;
}) {
  const me = useMe();
  const st = value ?? {};
  const [profile, setProfile] = useState(st.profile ?? "");
  const [max, setMax] = useState(st.max_comments ? String(st.max_comments) : "");
  const [sev, setSev] = useState(st.min_severity ?? "");
  const [model, setModel] = useState(st.model ?? "");
  const [auto, setAuto] = useState<boolean | null>(st.auto_review ?? null);
  const [drafts, setDrafts] = useState<boolean | null>(st.drafts ?? null);
  const [instr, setInstr] = useState(st.instructions ?? "");
  const save = () => {
    const s: Overrides = {};
    if (profile) s.profile = profile;
    if (max) s.max_comments = parseInt(max, 10);
    if (sev) s.min_severity = sev;
    if (model) s.model = model;
    if (auto !== null) s.auto_review = auto;
    if (drafts !== null) s.drafts = drafts;
    if (instr.trim()) s.instructions = instr.trim();
    onSave(s);
  };
  return (
    <div className="grid gap-5">
      {extra}
      <FormGrid className="lg:grid-cols-2">
        <Field label="Review profile">
          <NativeSelect value={profile} onChange={(e) => setProfile(e.target.value)}>
            <option value="">Inherit</option>
            <option value="chill">Chill</option>
            <option value="balanced">Balanced</option>
            <option value="assertive">Assertive</option>
          </NativeSelect>
        </Field>
        <Field label="Max inline comments">
          <Input type="number" min={0} max={200} value={max} placeholder="Inherit" onChange={(e) => setMax(e.target.value)} />
        </Field>
        <Field label="Minimum severity">
          <NativeSelect value={sev} onChange={(e) => setSev(e.target.value)}>
            <option value="">Inherit</option>
            <option value="critical">Critical</option>
            <option value="major">Major</option>
            <option value="minor">Minor</option>
            <option value="nitpick">Nitpick</option>
          </NativeSelect>
        </Field>
        <Field label="Model profile">
          <NativeSelect value={model} onChange={(e) => setModel(e.target.value)}>
            <option value="">{inheritModel}</option>
            {me.profiles.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </NativeSelect>
        </Field>
        <Field label="Auto review on push">
          <Tri value={auto} onChange={setAuto} on="On" off="Off" />
        </Field>
        <Field label="Review draft PRs">
          <Tri value={drafts} onChange={setDrafts} on="On" off="Off" />
        </Field>
      </FormGrid>
      <Field label="Extra review instructions">
        <Textarea rows={4} maxLength={8000} placeholder="Appended to the repository's own instructions" value={instr} onChange={(e) => setInstr(e.target.value)} />
      </Field>
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={save}>{saveLabel}</Button>
        {onCancel && (
          <Button variant="outline" onClick={onCancel}>
            Cancel
          </Button>
        )}
        <span className="flex-1" />
        {danger}
      </div>
    </div>
  );
}

// useReload returns a counter and a function that bumps it (to re-run effects).
export function useReload(): [number, () => void] {
  const [n, setN] = useState(0);
  return [n, useCallback(() => setN((x) => x + 1), [])];
}
