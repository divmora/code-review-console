import { useEffect, useState } from "react";
import { Link, Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import { get, post, setCSRF } from "./api";
import type { Feature, Me } from "./types";
import { MeCtx, RefreshMeCtx, ToastProvider } from "./ui";
import Repositories from "./pages/Repositories";
import Reviews from "./pages/Reviews";
import Learnings from "./pages/Learnings";
import Hosts from "./pages/Hosts";
import Rules from "./pages/Rules";
import Webhooks from "./pages/Webhooks";
import Settings from "./pages/Settings";
import Audit from "./pages/Audit";
import License from "./pages/License";

// Each section has its own URL under /admin, so a reload lands on it.
const sections: { path: string; title: string; el: JSX.Element; paid?: Feature }[] = [
  { path: "repositories", title: "Repositories", el: <Repositories /> },
  { path: "reviews", title: "Reviews", el: <Reviews /> },
  { path: "learnings", title: "Learnings", el: <Learnings />, paid: "learnings" },
  { path: "hosts", title: "Code hosts", el: <Hosts /> },
  { path: "rules", title: "Rules", el: <Rules />, paid: "rules" },
  { path: "webhooks", title: "Webhooks", el: <Webhooks /> },
  { path: "settings", title: "Settings", el: <Settings /> },
  { path: "audit", title: "Audit log", el: <Audit />, paid: "audit" },
  { path: "license", title: "License", el: <License /> },
];

// LicenseBanner warns when a license is in its grace period, expired or
// not valid (a missing license is the normal free core: no banner).
function LicenseBanner({ me }: { me: Me }) {
  const l = me.license;
  if (l.state === "grace")
    return (
      <div className="banner" role="status">
        The Divmora license expired on {new Date(l.expires_at!).toLocaleDateString()}. Paid features stop on {new Date(l.grace_until!).toLocaleDateString()}. <Link to="/license">Renew</Link>
      </div>
    );
  if (l.state === "expired" || l.state === "invalid")
    return (
      <div className="banner bad" role="status">
        {l.state === "expired" ? "The Divmora license has expired" : "The Divmora license is not valid"}: paid features are off. <Link to="/license">License</Link>
      </div>
    );
  return null;
}

function ThemePicker() {
  const [theme, setTheme] = useState(() => {
    try {
      return localStorage.getItem("crc-theme") || "system";
    } catch {
      return "system";
    }
  });
  const change = (v: string) => {
    setTheme(v);
    if (v === "system") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", v);
    try {
      localStorage.setItem("crc-theme", v);
    } catch {
      /* private mode: the choice lasts for this page only */
    }
  };
  return (
    <label className="themepick">
      Theme{" "}
      <select id="theme" aria-label="Theme" value={theme} onChange={(e) => change(e.target.value)}>
        <option value="system">System</option>
        <option value="light">Light</option>
        <option value="dark">Dark</option>
      </select>
    </label>
  );
}

function Title() {
  const loc = useLocation();
  const s = sections.find((x) => loc.pathname.startsWith("/" + x.path));
  const title = s?.title ?? "Repositories";
  useEffect(() => {
    document.title = title + " · Code review admin";
  }, [title]);
  return <h1 id="pageTitle">{title}</h1>;
}

export default function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [err, setErr] = useState("");
  const loadMe = () =>
    get<Me>("/admin/api/me")
      .then((m) => {
        setCSRF(m.csrf);
        setMe(m);
      })
      .catch((e) => setErr(String(e.message || e)));
  useEffect(() => {
    loadMe();
  }, []);
  if (!me) return <main>{err ? <div className="empty">{err}</div> : <div className="empty">Loading…</div>}</main>;
  const logout = () => post("/admin/logout").finally(() => (location.href = "/admin/login"));
  return (
    <MeCtx.Provider value={me}>
     <RefreshMeCtx.Provider value={loadMe}>
      <ToastProvider>
        <div className="layout">
          <aside>
            <div className="brand">
              Code review<span>Divmora console</span>
            </div>
            <nav className="side" aria-label="Sections">
              {sections.map((s) => (
                <NavLink key={s.path} to={"/" + s.path} className={({ isActive }) => (isActive ? "on" : "")}>
                  {s.title}
                  {s.paid && !me.license.features.includes(s.paid) && <span className="paid" title="Needs a license">licensed</span>}
                </NavLink>
              ))}
            </nav>
            <div className="me">
              <span>Signed in as {me.name || me.email}</span>
              <a href="/dashboard">Status dashboard</a>
              <ThemePicker />
              <button className="btn" id="logout" onClick={logout}>
                Sign out
              </button>
            </div>
          </aside>
          <main>
            <header>
              <Title />
            </header>
            <LicenseBanner me={me} />
            <Routes>
              <Route path="/" element={<Navigate to="/repositories" replace />} />
              {sections.map((s) => (
                <Route key={s.path} path={"/" + s.path} element={s.el} />
              ))}
              <Route path="*" element={<Navigate to="/repositories" replace />} />
            </Routes>
          </main>
        </div>
      </ToastProvider>
     </RefreshMeCtx.Provider>
    </MeCtx.Provider>
  );
}
