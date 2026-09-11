import { useState } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "cn";
import { Outlet, Link, useRouterState, useNavigate } from "@tanstack/react-router";
import {
  LayoutDashboard,
  Server,
  History,
  KeyRound,
  Bot,
  Settings,
  ShieldCheck,
  Sun,
  Moon,
  Monitor,
  Menu,
  X,
  Activity,
  LogOut,
  ChevronLeft,
  ChevronRight,
} from "lucide-react";
import { useTheme } from "../../hooks/use-theme";
import { useSystemStats, useSession, useLogout } from "../../lib/api/query-hooks";
import { useWatchDashboard } from "../../lib/api/streaming-hooks";

const navItems = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard },
  { to: "/pools", label: "Runner Pools", icon: Server, badgeKey: "pools" },
  { to: "/history", label: "Job History", icon: History },
  { to: "/profiles", label: "Auth Profiles", icon: KeyRound },
  { to: "/renovate", label: "Renovate Bot", icon: Bot },
  { to: "/settings", label: "Settings", icon: Settings },
];

export function AppShell() {
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const { theme, setTheme } = useTheme();
  const routerState = useRouterState();
  const currentPath = routerState.location.pathname;

  const navigate = useNavigate();
  const logout = useLogout();
  const { data: stats } = useSystemStats();
  const { data: session } = useSession();
  const { isConnected } = useWatchDashboard({
    enabled: Boolean(session?.username),
  });

  const activeRunners = stats?.totalActiveRunners ?? 0;
  const idleRunners = stats?.totalIdleRunners ?? 0;
  const totalRunners = activeRunners + idleRunners;

  const currentNav = navItems.find((item) =>
    item.to === "/" ? currentPath === "/" : currentPath.startsWith(item.to),
  );

  const handleLogout = () => {
    logout();
    navigate({ to: "/login" });
  };

  return (
    <div className="flex min-h-screen bg-slate-50 text-slate-900 transition-colors dark:bg-slate-950 dark:text-slate-50">
      {/* Desktop Sidebar */}
      <aside
        className={`hidden flex-col border-r border-slate-200 bg-white transition-all duration-200 dark:border-slate-800 dark:bg-slate-900 md:flex ${
          sidebarCollapsed ? "w-20" : "w-64"
        }`}
      >
        {/* Brand */}
        <div className="flex h-16 items-center justify-between border-b border-slate-200 px-4 dark:border-slate-800">
          <div className="flex items-center gap-3 overflow-hidden">
            <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-blue-600 text-white shadow-sm">
              <ShieldCheck className="h-5 w-5" />
            </div>
            {!sidebarCollapsed && (
              <div className="truncate">
                <span className="text-base font-bold tracking-tight text-slate-900 dark:text-white">
                  Runnero
                </span>
                <span className="block text-[11px] font-medium uppercase tracking-wider text-blue-600 dark:text-blue-400">
                  Supervisor
                </span>
              </div>
            )}
          </div>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => setSidebarCollapsed(!sidebarCollapsed)}
            title={sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar"}
          >
            {sidebarCollapsed ? <ChevronRight /> : <ChevronLeft />}
          </Button>
        </div>

        {/* Navigation Items */}
        <nav className="flex-1 space-y-1 p-3">
          {navItems.map((item) => {
            const Icon = item.icon;
            const isActive =
              item.to === "/" ? currentPath === "/" : currentPath.startsWith(item.to);
            return (
              <Link
                key={item.to}
                to={item.to}
                title={sidebarCollapsed ? item.label : undefined}
                className={`flex items-center justify-between rounded-xl px-3 py-2.5 text-sm font-medium transition-colors ${
                  isActive
                    ? "bg-blue-50 font-semibold text-blue-600 dark:bg-blue-900/30 dark:text-blue-400"
                    : "text-slate-600 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-slate-800/60 dark:hover:text-slate-200"
                }`}
              >
                <div className="flex items-center gap-3">
                  <Icon
                    className={`h-4 w-4 shrink-0 ${isActive ? "text-blue-600 dark:text-blue-400" : ""}`}
                  />
                  {!sidebarCollapsed && <span>{item.label}</span>}
                </div>
                {!sidebarCollapsed && item.badgeKey === "pools" && totalRunners > 0 && (
                  <span className="rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                    {totalRunners}
                  </span>
                )}
              </Link>
            );
          })}
        </nav>

        {/* User / Footer */}
        <div className="border-t border-slate-200 p-3 dark:border-slate-800">
          <div className="flex items-center justify-between gap-2">
            {!sidebarCollapsed && (
              <div className="min-w-0 truncate text-xs">
                <span className="font-semibold text-slate-800 dark:text-slate-200">
                  {session?.username || "admin"}
                </span>
                <span className="block text-[10px] text-slate-400">Supervisor Admin</span>
              </div>
            )}
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={handleLogout}
              title="Sign Out"
              className="hover:text-destructive"
            >
              <LogOut />
            </Button>
          </div>
        </div>
      </aside>

      {/* Main Container */}
      <div className="flex flex-1 flex-col">
        {/* Top Header */}
        <header className="sticky top-0 z-30 flex h-16 items-center justify-between border-b border-slate-200 bg-white/80 px-4 backdrop-blur-md dark:border-slate-800 dark:bg-slate-900/80 md:px-8">
          <div className="flex items-center gap-4">
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={() => setMobileMenuOpen(!mobileMenuOpen)}
              className="md:hidden"
            >
              {mobileMenuOpen ? <X /> : <Menu />}
            </Button>

            {/* Breadcrumbs */}
            <div className="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
              <span className="font-medium text-slate-400">App</span>
              <span className="text-slate-300 dark:text-slate-600">/</span>
              <span className="font-semibold text-slate-900 dark:text-white">
                {currentNav?.label ?? "Dashboard"}
              </span>
            </div>

            {/* Health Status Pill */}
            <div className="hidden sm:flex items-center gap-1.5 rounded-full bg-emerald-50 px-2.5 py-1 text-xs font-semibold text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400">
              <span className="h-2 w-2 rounded-full bg-emerald-500 animate-pulse" />
              <span>Healthy</span>
            </div>
          </div>

          <div className="flex items-center gap-3">
            {/* Realtime Stream Status Pill */}
            <span
              className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium transition-colors ${
                isConnected
                  ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-400 border border-emerald-200 dark:border-emerald-800/60"
                  : "bg-amber-50 text-amber-700 dark:bg-amber-950/60 dark:text-amber-400 border border-amber-200 dark:border-amber-800/60"
              }`}
              title={isConnected ? "Realtime stream active" : "Reconnecting to realtime stream..."}
            >
              <span
                className={`h-1.5 w-1.5 rounded-full ${
                  isConnected ? "bg-emerald-500 animate-pulse" : "bg-amber-500"
                }`}
              />
              <span className="hidden sm:inline font-mono">
                {isConnected ? "Live" : "Connecting"}
              </span>
            </span>

            {/* Active / Idle Runners Counter */}
            <div className="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
              <span className="hidden sm:inline font-medium text-slate-900 dark:text-white">
                Runners:
              </span>
              <span className="inline-flex items-center gap-1 rounded-full bg-slate-100 px-2.5 py-1 text-xs font-semibold text-slate-700 dark:bg-slate-800 dark:text-slate-300">
                <Activity className="h-3 w-3 text-emerald-500" />
                <span>{activeRunners} active</span>
                <span className="text-slate-300 dark:text-slate-600">/</span>
                <span>{idleRunners} idle</span>
              </span>
            </div>

            {/* Theme Switcher */}
            <div className="flex items-center rounded-xl border border-slate-200 bg-slate-50 p-1 dark:border-slate-800 dark:bg-slate-950">
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => setTheme("light")}
                title="Light Theme"
                aria-pressed={theme === "light"}
                className={cn(theme === "light" && "bg-background text-foreground shadow-2xs")}
              >
                <Sun />
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => setTheme("dark")}
                title="Dark Theme"
                aria-pressed={theme === "dark"}
                className={cn(theme === "dark" && "bg-background text-foreground shadow-2xs")}
              >
                <Moon />
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => setTheme("system")}
                title="System Theme"
                aria-pressed={theme === "system"}
                className={cn(theme === "system" && "bg-background text-foreground shadow-2xs")}
              >
                <Monitor />
              </Button>
            </div>
          </div>
        </header>

        {/* Mobile Navigation Drawer */}
        {mobileMenuOpen && (
          <div className="border-b border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900 md:hidden">
            <nav className="space-y-1">
              {navItems.map((item) => {
                const Icon = item.icon;
                const isActive =
                  item.to === "/" ? currentPath === "/" : currentPath.startsWith(item.to);
                return (
                  <Link
                    key={item.to}
                    to={item.to}
                    onClick={() => setMobileMenuOpen(false)}
                    className={`flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium ${
                      isActive
                        ? "bg-blue-50 text-blue-600 dark:bg-blue-900/30 dark:text-blue-400"
                        : "text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
                    }`}
                  >
                    <Icon className="h-4 w-4" />
                    <span>{item.label}</span>
                  </Link>
                );
              })}
            </nav>
          </div>
        )}

        {/* Page Content View */}
        <main className="flex-1 p-6 md:p-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
