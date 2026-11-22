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
  Server,
  SlidersHorizontal,
  Sun,
  Webhook,
  type LucideIcon,
} from "lucide-react";
import { get, post, setCSRF } from "./api";
import type { Feature, Me } from "./types";
import { ConfirmProvider, MeCtx, RefreshMeCtx, ToastProvider } from "./ui";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
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
import Audit from "./pages/Audit";
import License from "./pages/License";
import Models from "./pages/Models";
import Executor from "./pages/Executor";
import Setup from "./pages/Setup";

// Each section has its own URL under /admin, so a reload lands on it.
const sections: { path: string; title: string; icon: LucideIcon; el: JSX.Element; paid?: Feature }[] = [
  { path: "setup", title: "Setup", icon: Rocket, el: <Setup /> },
  { path: "repositories", title: "Repositories", icon: FolderGit2, el: <Repositories /> },
  { path: "reviews", title: "Reviews", icon: ChartColumn, el: <Reviews /> },
  { path: "learnings", title: "Learnings", icon: BookOpen, el: <Learnings />, paid: "learnings" },
  { path: "hosts", title: "Code hosts", icon: Server, el: <Hosts /> },
  { path: "models", title: "Models", icon: Bot, el: <Models /> },
  { path: "executor", title: "Executor", icon: Container, el: <Executor /> },
  { path: "rules", title: "Rules", icon: ListChecks, el: <Rules />, paid: "rules" },
  { path: "webhooks", title: "Webhooks", icon: Webhook, el: <Webhooks /> },
  { path: "settings", title: "Settings", icon: SlidersHorizontal, el: <Settings /> },
  { path: "audit", title: "Audit log", icon: ScrollText, el: <Audit />, paid: "audit" },
  { path: "license", title: "License", icon: KeyRound, el: <License /> },
];

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
            The Divmora license expired on {new Date(l.expires_at!).toLocaleDateString()}. Paid features stop on {new Date(l.grace_until!).toLocaleDateString()}.{" "}
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
              {sections.map((s) => {
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
                        {me.setup.done}/{me.setup.total}
                      </SidebarMenuBadge>
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
          <a className="underline-offset-2 hover:text-foreground hover:underline" href="/dashboard">
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

function Header({ me }: { me: Me }) {
  const loc = useLocation();
  const s = current(loc.pathname);
  const title = s?.title ?? "Repositories";
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
  if (!me) return <div className="grid min-h-svh place-items-center text-sm text-muted-foreground">{err || "Loading…"}</div>;
  return (
    <MeCtx.Provider value={me}>
      <RefreshMeCtx.Provider value={loadMe}>
        <TooltipProvider delayDuration={300}>
          <ToastProvider>
            <ConfirmProvider>
              <SidebarProvider>
                <AppSidebar me={me} />
                <SidebarInset className="min-w-0">
                  <Header me={me} />
                  <div className="mx-auto grid w-full max-w-7xl gap-4 p-4 md:p-6">
                    <LicenseBanner me={me} />
                    <Routes>
                      <Route path="/" element={<Navigate to={me.setup && !me.setup.complete ? "/setup" : "/repositories"} replace />} />
                      {sections.map((s) => (
                        <Route key={s.path} path={"/" + s.path} element={s.el} />
                      ))}
                      <Route path="*" element={<Navigate to="/repositories" replace />} />
                    </Routes>
                  </div>
                </SidebarInset>
              </SidebarProvider>
            </ConfirmProvider>
          </ToastProvider>
        </TooltipProvider>
      </RefreshMeCtx.Provider>
    </MeCtx.Provider>
  );
}
