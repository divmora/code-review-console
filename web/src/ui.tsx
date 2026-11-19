import { createContext, ReactNode, useCallback, useContext, useRef, useState } from "react";
import type { Me, Overrides } from "./types";

// ---- toast ----

type Toast = (msg: string, err?: boolean) => void;
const ToastCtx = createContext<Toast>(() => {});
export const useToast = () => useContext(ToastCtx);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [t, setT] = useState<{ msg: string; err: boolean } | null>(null);
  const timer = useRef<number>();
  const show = useCallback<Toast>((msg, err = false) => {
    setT({ msg, err });
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setT(null), 3500);
  }, []);
  return (
    <ToastCtx.Provider value={show}>
      {children}
      <div className={"toast" + (t ? " show" : "") + (t?.err ? " err" : "")} id="toast" role="status">
        {t?.msg}
      </div>
    </ToastCtx.Provider>
  );
}

// errMsg turns a thrown value into a message for the toast.
export const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));

// ---- signed-in user ----

export const MeCtx = createContext<Me | null>(null);
export const useMe = () => useContext(MeCtx)!;
// RefreshMeCtx reloads /admin/api/me after a change that affects it (settings).
export const RefreshMeCtx = createContext<() => void>(() => {});
export const useRefreshMe = () => useContext(RefreshMeCtx);

// ---- small pieces ----

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function StateBadge({ kind, children }: { kind: "on" | "off" | "idle"; children: ReactNode }) {
  return <span className={"state " + kind}>{children}</span>;
}

export function Chips({ items, empty }: { items: string[]; empty?: string }) {
  if (!items.length) return <span className="muted">{empty ?? "defaults"}</span>;
  return (
    <span>
      {items.map((c) => (
        <span className="chip" key={c}>
          {c}
        </span>
      ))}
    </span>
  );
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
export function Tri({ value, onChange, on, off, inherit = "Inherit", label }: {
  value: boolean | null | undefined; onChange: (v: boolean | null) => void; on: string; off: string; inherit?: string; label?: string;
}) {
  const v = value === null || value === undefined ? "" : String(value);
  return (
    <select aria-label={label} value={v} onChange={(e) => onChange(e.target.value === "" ? null : e.target.value === "true")}>
      <option value="">{inherit}</option>
      <option value="true">{on}</option>
      <option value="false">{off}</option>
    </select>
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
    <div>
      {extra}
      <div className="grid">
        <label className="f">
          Review profile
          <select value={profile} onChange={(e) => setProfile(e.target.value)}>
            <option value="">Inherit</option>
            <option value="chill">Chill</option>
            <option value="balanced">Balanced</option>
            <option value="assertive">Assertive</option>
          </select>
        </label>
        <label className="f">
          Max inline comments
          <input type="number" min={0} max={200} value={max} placeholder="Inherit" onChange={(e) => setMax(e.target.value)} />
        </label>
        <label className="f">
          Minimum severity
          <select value={sev} onChange={(e) => setSev(e.target.value)}>
            <option value="">Inherit</option>
            <option value="critical">Critical</option>
            <option value="major">Major</option>
            <option value="minor">Minor</option>
            <option value="nitpick">Nitpick</option>
          </select>
        </label>
        <label className="f">
          Model profile
          <select value={model} onChange={(e) => setModel(e.target.value)}>
            <option value="">{inheritModel}</option>
            {me.profiles.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </label>
        <label className="f">
          Auto review on push
          <Tri value={auto} onChange={setAuto} on="On" off="Off" />
        </label>
        <label className="f">
          Review draft PRs
          <Tri value={drafts} onChange={setDrafts} on="On" off="Off" />
        </label>
      </div>
      <label className="f">
        Extra review instructions
        <textarea maxLength={8000} placeholder="Appended to the repository's own instructions" value={instr} onChange={(e) => setInstr(e.target.value)} />
      </label>
      <div className="bar mt">
        <button className="btn primary" onClick={save}>
          {saveLabel}
        </button>
        {onCancel && (
          <button className="btn" onClick={onCancel}>
            Cancel
          </button>
        )}
        <span className="spacer" />
        {danger}
      </div>
    </div>
  );
}

// useAsync runs a loader and keeps its result; reload() runs it again.
export function useReload(): [number, () => void] {
  const [n, setN] = useState(0);
  return [n, useCallback(() => setN((x) => x + 1), [])];
}
