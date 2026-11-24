import { useEffect, useState } from "react";
import { Link, Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import {
  BookOpen,
  Bot,
  ChartColumn,
  Container,
  FolderGit2,
  KeyRound,
  ListChecks,
  Rocket,
  LogOut,
  Monitor,
  Moon,
  ScrollText,
  ClipboardCheck,
  Server,
  SlidersHorizontal,
  Sun,
  TrendingUp,
  LayoutDashboard,
  Webhook,
  type LucideIcon,
} from "lucide-react";
import { get, post, setCSRF } from "./api";
import type { Feature, Me } from "./types";
import { fmtDate } from "./format";
import { ConfirmProvider, MeCtx, RefreshMeCtx, Section, ToastProvider } from "./ui";
import { UnsavedBar } from "./unsaved";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Separator } from "@/components/ui/separator";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Logo } from "./components/Logo";
import Repositories from "./pages/Repositories";
import Reviews from "./pages/Reviews";
import Learnings from "./pages/Learnings";
import Hosts from "./pages/Hosts";
import Rules from "./pages/Rules";
import Webhooks from "./pages/Webhooks";
import Settings from "./pages/Settings";
import ChangeRequests from "./pages/ChangeRequests";
import Audit from "./pages/Audit";
import License from "./pages/License";
import Models from "./pages/Models";
import Executor from "./pages/Executor";
import Setup from "./pages/Setup";
import Impact from "./pages/Impact";
import Dashboards from "./pages/Dashboards";

// Each section has its own URL under /admin, so a reload lands on it.
const sections: { path: string; title: string; icon: LucideIcon; el: JSX.Element; paid?: Feature }[] = [
  { path: "setup", title: "Setup", icon: Rocket, el: <Setup /> },
  { path: "repositories", title: "Repositories", icon: FolderGit2, el: <Repositories /> },
  { path: "reviews", title: "Reviews", icon: ChartColumn, el: <Reviews /> },
  { path: "impact", title: "Impact", icon: TrendingUp, el: <Impact /> },
  { path: "dashboards", title: "Dashboards", icon: LayoutDashboard, el: <Dashboards /> },
  { path: "learnings", title: "Learnings", icon: BookOpen, el: <Learnings />, paid: "learnings" },
  { path: "hosts", title: "Code hosts", icon: Server, el: <Hosts /> },
  { path: "models", title: "Models", icon: Bot, el: <Models /> },
  { path: "executor", title: "Executor", icon: Container, el: <Executor /> },
  { path: "rules", title: "Rules", icon: ListChecks, el: <Rules />, paid: "rules" },
  { path: "webhooks", title: "Webhooks", icon: Webhook, el: <Webhooks /> },
  { path: "settings", title: "Settings", icon: SlidersHorizontal, el: <Settings /> },
  { path: "change-requests", title: "Change requests", icon: ClipboardCheck, el: <ChangeRequests /> },
  { path: "audit", title: "Audit log", icon: ScrollText, el: <Audit />, paid: "audit" },
  { path: "license", title: "License", icon: KeyRound, el: <License /> },
];

// ExpiryBanner warns from two weeks before the license or a code host
// token expires (and once a token has expired), with the date and what
// stops working.
function ExpiryBanner({ me }: { me: Me }) {
  const list = me.expiry_warnings || [];
  if (!list.length) return null;
  const bad = list.some((w) => w.severity === "bad");
  return (
    <Alert role="status" variant={bad ? "destructive" : "default"} className={bad ? "banner bad" : "banner border-warning/40 bg-warning-soft text-warning"}>
      <AlertDescription className={bad ? "" : "text-warning"}>
        {list.map((w) => (
          <p key={w.kind + (w.host_id || "")}>
            {w.message}{" "}
            <Link className="font-semibold underline underline-offset-2" to={w.kind === "license" ? "/license" : "/hosts"}>
              {w.kind === "license" ? "License" : "Code hosts"}
            </Link>
          </p>
        ))}
      </AlertDescription>
    </Alert>
  );
}

// LicenseBanner warns when a license is in its grace period, expired or
// not valid (a missing license is the normal free core: no banner).
function LicenseBanner({ me }: { me: Me }) {
  const l = me.license;
  if (me.reviews_paused)
    return (
      <Alert role="status" variant="destructive" className="banner bad">
        <AlertDescription>
          <p>
            {me.reviews_paused[0].toUpperCase() + me.reviews_paused.slice(1)}.{" "}
            <Link className="font-semibold underline underline-offset-2" to="/license">
              Add a license
            </Link>
          </p>
        </AlertDescription>
      </Alert>
    );
  if (l.state === "grace")
    return (
      <Alert role="status" className="banner border-warning/40 bg-warning-soft text-warning">
        <AlertDescription className="text-warning">
          <p>
            The Divmora license expired on {fmtDate(l.expires_at!)}. Paid features stop on {fmtDate(l.grace_until!)}.{" "}
            <Link className="font-semibold underline underline-offset-2" to="/license">
              Renew
            </Link>
          </p>
        </AlertDescription>
      </Alert>
    );
  if (l.state === "expired" || l.state === "invalid")
    return (
      <Alert role="status" variant="destructive" className="banner bad">
        <AlertDescription>
          <p>
            {l.state === "expired" ? "The Divmora license has expired" : "The Divmora license is not valid"}: paid features are off.{" "}
            <Link className="font-semibold underline underline-offset-2" to="/license">
              License
            </Link>
          </p>
        </AlertDescription>
      </Alert>
    );
  return null;
}

function ThemeMenu() {
  const [theme, setTheme] = useState(() => {
    try {
      return localStorage.getItem("crc-theme") || "system";
    } catch {
      return "system";
    }
  });
  const change = (v: string) => {
    setTheme(v);
    try {
      localStorage.setItem("crc-theme", v);
    } catch {
      /* private mode: the choice lasts for this page only */
    }
    const apply = (window as unknown as { __crcApplyTheme?: () => void }).__crcApplyTheme;
    if (apply) apply();
    else document.documentElement.classList.toggle("dark", v === "dark");
  };
  const Icon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" id="theme" aria-label="Theme">
          <Icon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>Theme</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuRadioGroup value={theme} onValueChange={change}>
          <DropdownMenuRadioItem value="system">System</DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="light">Light</DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="dark">Dark</DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function current(pathname: string) {
  return sections.find((x) => pathname.startsWith("/" + x.path));
}

function AppSidebar({ me }: { me: Me }) {
  const loc = useLocation();
  const { setOpenMobile } = useSidebar();
  const logout = () => post("/admin/logout").finally(() => (location.href = "/admin/login"));
  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <div className="flex items-center gap-2 px-1 py-1.5">
          <Logo />
          <div className="grid leading-tight group-data-[collapsible=icon]:hidden">
            <span className="truncate text-sm font-semibold">Code review</span>
            <span className="truncate text-xs text-muted-foreground">Divmora console</span>
          </div>
        </div>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu className="side" aria-label="Sections">
              {sections.filter((s) => s.path !== "change-requests" || me.role === "operator" || (me.role === "admin" && (me.license.features.includes("sso_rules") || !!me.pending_approvals))).map((s) => {
                const locked = s.paid && !me.license.features.includes(s.paid);
                const active = current(loc.pathname)?.path === s.path;
                return (
                  <SidebarMenuItem key={s.path}>
                    <SidebarMenuButton asChild isActive={active} tooltip={s.title}>
                      <NavLink to={"/" + s.path} className={active ? "on" : ""} onClick={() => setOpenMobile(false)}>
                        <s.icon />
                        <span>{s.title}</span>
                      </NavLink>
                    </SidebarMenuButton>
                    {s.path === "setup" && me.setup && !me.setup.complete && (
                      <SidebarMenuBadge className="text-[10px] font-normal text-muted-foreground" title="Setup steps done">
                        <span title={`${me.setup.done} of ${me.setup.total} required setup steps done`}>{me.setup.done}/{me.setup.total}</span>
                      </SidebarMenuBadge>
                    )}
                    {s.path === "change-requests" && !!me.pending_approvals && (
                      <SidebarMenuBadge className="text-[10px]" title={me.pending_approvals + " waiting for an admin"}>{me.pending_approvals}</SidebarMenuBadge>
                    )}
                    {locked && (
                      <SidebarMenuBadge className="paid text-[10px] font-normal text-muted-foreground" title="Needs a license">
                        licensed
                      </SidebarMenuBadge>
                    )}
                  </SidebarMenuItem>
                );
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter>
        <div className="grid gap-1 px-2 pb-1 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
          <span className="truncate">Signed in as {me.name || me.email}</span>
          <a className="underline-offset-2 hover:text-foreground hover:underline" href="/admin/dashboard">
            Status dashboard
          </a>
          {me.version && <span>Version {me.version}</span>}
        </div>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton id="logout" onClick={logout} tooltip="Sign out">
              <LogOut />
              <span>Sign out</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  );
}

// NotFoundPage is shown for an unknown /admin/<section> (the server answers
// 404 with the panel, so the sidebar stays).
function NotFoundPage() {
  const loc = useLocation();
  return (
    <Card className="max-w-xl">
      <CardHeader>
        <CardTitle className="h2">Page not found</CardTitle>
        <CardDescription>
          There is no <code>/admin{loc.pathname}</code> page. Pick a section in the sidebar, or go to the start.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button asChild>
          <Link to="/">Go to the start</Link>
        </Button>
      </CardContent>
    </Card>
  );
}

// SignOutPage answers a typed or bookmarked /admin/logout: signing out is
// a POST (CSRF), so it asks with a button.
function SignOutPage() {
  const signOut = () => post("/admin/logout").finally(() => (location.href = "/admin/login"));
  return (
    <Card className="max-w-xl">
      <CardHeader>
        <CardTitle className="h2">Sign out?</CardTitle>
        <CardDescription>You'll need to sign in again to use the admin panel.</CardDescription>
      </CardHeader>
      <CardContent className="flex gap-2">
        <Button onClick={signOut}>Sign out</Button>
        <Button asChild variant="outline">
          <Link to="/">Stay signed in</Link>
        </Button>
      </CardContent>
    </Card>
  );
}

// SignedInPage is where signing in from another tab lands after a save
// found the session had ended.
function SignedInPage() {
  return (
    <Card className="max-w-xl">
      <CardHeader>
        <CardTitle className="h2">You're signed in again</CardTitle>
        <CardDescription>Close this tab and go back to the page with your changes, then save them.</CardDescription>
      </CardHeader>
    </Card>
  );
}

function Header({ me }: { me: Me }) {
  const loc = useLocation();
  const s = current(loc.pathname);
  const title = s?.title ?? (loc.pathname === "/logout" ? "Sign out" : loc.pathname === "/signed-in" ? "Signed in" : "Page not found");
  useEffect(() => {
    document.title = title + " · Code review admin";
  }, [title]);
  return (
    <header className="sticky top-0 z-10 flex h-14 shrink-0 items-center gap-2 border-b bg-background/80 px-4 backdrop-blur">
      <SidebarTrigger className="-ml-1" />
      <Separator orientation="vertical" className="mr-1 data-[orientation=vertical]:h-4" />
      <h1 id="pageTitle" className="text-base font-semibold">
        {title}
      </h1>
      {s?.paid && !me.license.features.includes(s.paid) && <Badge variant="secondary">licensed</Badge>}
      <span className="flex-1" />
      <ThemeMenu />
    </header>
  );
}

// WIDE is where the sidebar has room to show its labels. Narrower (a split
// screen, a pane beside an editor) it collapses to icons, so tables keep
// their width (issue #130); the toggle still opens it, until the window
// crosses the breakpoint again.
const WIDE = "(min-width: 1024px)";

function useSidebarOpen(): [boolean, (open: boolean) => void] {
  const [open, setOpen] = useState(() => window.matchMedia(WIDE).matches);
  useEffect(() => {
    const mql = window.matchMedia(WIDE);
    const onChange = () => setOpen(mql.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);
  return [open, setOpen];
}

export default function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [sidebarOpen, setSidebarOpen] = useSidebarOpen();
  const [err, setErr] = useState("");
  const loadMe = () =>
    get<Me>("/admin/api/me")
      .then((m) => {
        setCSRF(m.csrf);
        setMe(m);
      })
      .catch((e) => setErr(String(e.message || e)));
  // signedOut: a save found the session ended; the page keeps its edits
  // until the admin signs in again in another tab and comes back.
  const [signedOut, setSignedOut] = useState(false);
  useEffect(() => {
    loadMe();
    // A change sent for approval updates the Change requests count.
    const onPending = () => loadMe();
    const onSignedOut = () => setSignedOut(true);
    window.addEventListener("crc-pending", onPending);
    window.addEventListener("crc-signed-out", onSignedOut);
    return () => {
      window.removeEventListener("crc-pending", onPending);
      window.removeEventListener("crc-signed-out", onSignedOut);
    };
  }, []);
  useEffect(() => {
    if (!signedOut) return;
    // Back from signing in: take the new session (and its CSRF token).
    const onFocus = () =>
      get<Me>("/admin/api/me")
        .then((m) => {
          setCSRF(m.csrf);
          setMe(m);
          setSignedOut(false);
        })
        .catch(() => {});
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [signedOut]);
  if (!me) return <div className="grid min-h-svh place-items-center text-sm text-muted-foreground">{err || "Loading…"}</div>;
  return (
    <MeCtx.Provider value={me}>
      <RefreshMeCtx.Provider value={loadMe}>
        <TooltipProvider delayDuration={300}>
          <ToastProvider>
            <ConfirmProvider>
              <SidebarProvider open={sidebarOpen} onOpenChange={setSidebarOpen}>
                <AppSidebar me={me} />
                <SidebarInset className="min-w-0">
                  <Header me={me} />
                  <div className="mx-auto grid w-full max-w-7xl gap-4 p-4 md:p-6">
                    {signedOut && (
                      <Alert role="alert" className="banner border-warning/60">
                        <AlertDescription className="flex flex-wrap items-center gap-3">
                          <span>Your session ended. Sign in again in a new tab, then come back here and save: your changes are kept on this page.</span>
                          <Button size="sm" variant="outline" asChild>
                            <a href={"/admin/login?next=" + encodeURIComponent("/admin/signed-in")} target="_blank" rel="noopener">
                              Sign in in a new tab
                            </a>
                          </Button>
                        </AlertDescription>
                      </Alert>
                    )}
                    <LicenseBanner me={me} />
                    <ExpiryBanner me={me} />
                    {me.role === "viewer" && (
                      <Alert role="status" className="banner">
                        <AlertDescription>You have read-only access: you can see every page, and changes are refused. Ask an admin to change something.</AlertDescription>
                      </Alert>
                    )}
                    <Routes>
                      <Route path="/" element={<Navigate to={me.setup && !me.setup.complete ? "/setup" : "/repositories"} replace />} />
                      {sections.map((s) => (
                        <Route key={s.path} path={"/" + s.path} element={<Section key={s.path} name={s.title}>{s.el}</Section>} />
                      ))}
                      <Route path="/approvals" element={<Navigate to="/change-requests" replace />} />
                      <Route path="/logout" element={<SignOutPage />} />
                      <Route path="/signed-in" element={<SignedInPage />} />
                      <Route path="*" element={<NotFoundPage />} />
                    </Routes>
                  </div>
                  <UnsavedBar />
                </SidebarInset>
              </SidebarProvider>
            </ConfirmProvider>
          </ToastProvider>
        </TooltipProvider>
      </RefreshMeCtx.Provider>
    </MeCtx.Provider>
  );
}
