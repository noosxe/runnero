import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Separator } from "@/components/ui/separator";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuGroup,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
} from "@/components/ui/sidebar";
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
  Activity,
  LogOut,
} from "lucide-react";
import { useTheme, type Theme } from "../../hooks/use-theme";
import { useSystemStats, useSession, useLogout } from "../../lib/api/query-hooks";
import { useWatchDashboard } from "../../lib/api/streaming-hooks";

const SIDEBAR_COLLAPSED_KEY = "runnero-sidebar-collapsed";

const navItems = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard },
  { to: "/pools", label: "Runner Pools", icon: Server, badgeKey: "pools" },
  { to: "/history", label: "Job History", icon: History },
  { to: "/profiles", label: "Auth Profiles", icon: KeyRound },
  { to: "/renovate", label: "Renovate Bot", icon: Bot },
  { to: "/settings", label: "Settings", icon: Settings },
];

function ShellMain() {
  const { theme, setTheme } = useTheme();
  const routerState = useRouterState();
  const currentPath = routerState.location.pathname;

  const { data: stats } = useSystemStats();
  const { data: session } = useSession();
  const { isConnected } = useWatchDashboard({
    enabled: Boolean(session?.username),
  });

  const currentNav = navItems.find((item) =>
    item.to === "/" ? currentPath === "/" : currentPath.startsWith(item.to),
  );

  const activeRunners = stats?.totalActiveRunners ?? 0;
  const idleRunners = stats?.totalIdleRunners ?? 0;
  return (
    <SidebarInset>
      {/* Top Header */}
      <header className="sticky top-0 z-30 flex h-16 shrink-0 items-center justify-between gap-3 border-b bg-background/80 px-4 backdrop-blur-md transition-[width,height] ease-linear group-has-data-[collapsible=icon]/sidebar-wrapper:h-12 md:px-8">
        <div className="flex items-center gap-2">
          <SidebarTrigger className="-ml-1" />
          <Separator orientation="vertical" className="mr-2 data-[orientation=vertical]:h-4" />

          {/* Breadcrumbs */}
          <Breadcrumb>
            <BreadcrumbList>
              <BreadcrumbItem>
                <span className="font-medium text-muted-foreground/70">App</span>
              </BreadcrumbItem>
              <BreadcrumbSeparator />
              <BreadcrumbItem>
                <BreadcrumbPage className="font-semibold">
                  {currentNav?.label ?? "Dashboard"}
                </BreadcrumbPage>
              </BreadcrumbItem>
            </BreadcrumbList>
          </Breadcrumb>

          {/* Health Status Pill */}
          <Badge className="hidden border-success/30 bg-success/10 font-semibold text-success sm:flex">
            <span className="size-2 animate-pulse rounded-full bg-success" />
            <span>Healthy</span>
          </Badge>
        </div>

        <div className="flex items-center gap-3">
          {/* Realtime Stream Status Pill */}
          <Badge
            className={cn(
              "border",
              isConnected
                ? "border-success/30 bg-success/10 text-success"
                : "border-warning/30 bg-warning/10 text-warning",
            )}
          >
            <span
              className={cn(
                "size-1.5 rounded-full",
                isConnected ? "animate-pulse bg-emerald-500" : "bg-amber-500",
              )}
            />
            <span className="hidden font-mono sm:inline">
              {isConnected ? "Live" : "Connecting"}
            </span>
          </Badge>

          {/* Active / Idle Runners Counter */}
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <span className="hidden font-medium text-foreground sm:inline">Runners:</span>
            <Badge variant="secondary" className="h-auto gap-1 py-1">
              <Activity className="text-success" />
              <span>{activeRunners} active</span>
              <span className="text-muted-foreground">/</span>
              <span>{idleRunners} idle</span>
            </Badge>
          </div>

          {/* Theme Switcher */}
          <ToggleGroup
            variant="outline"
            size="sm"
            spacing={0}
            value={[theme]}
            onValueChange={(value: readonly string[]) => {
              const next = value[value.length - 1];
              if (typeof next === "string" && next) setTheme(next as Theme);
            }}
          >
            <ToggleGroupItem value="light" aria-label="Light Theme">
              <Sun />
            </ToggleGroupItem>
            <ToggleGroupItem value="dark" aria-label="Dark Theme">
              <Moon />
            </ToggleGroupItem>
            <ToggleGroupItem value="system" aria-label="System Theme">
              <Monitor />
            </ToggleGroupItem>
          </ToggleGroup>
        </div>
      </header>

      {/* Page Content View */}
      <main className="flex-1 p-6 md:p-8">
        <Outlet />
      </main>
    </SidebarInset>
  );
}

function NavSidebar() {
  const routerState = useRouterState();
  const currentPath = routerState.location.pathname;

  const navigate = useNavigate();
  const logout = useLogout();
  const { data: stats } = useSystemStats();
  const { data: session } = useSession();

  const activeRunners = stats?.totalActiveRunners ?? 0;
  const idleRunners = stats?.totalIdleRunners ?? 0;
  const totalRunners = activeRunners + idleRunners;

  const displayName = session?.username || "admin";
  const initials = displayName.slice(0, 2).toUpperCase();

  const handleLogout = () => {
    logout();
    navigate({ to: "/login" });
  };

  return (
    <Sidebar collapsible="icon">
      {/* Brand */}
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" render={<div />}>
              <div className="flex aspect-square size-8 shrink-0 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
                <ShieldCheck className="size-4" />
              </div>
              <div className="grid flex-1 text-left leading-tight">
                <span className="truncate text-base font-bold tracking-tight text-foreground">
                  Runnero
                </span>
                <span className="truncate text-[11px] font-medium uppercase tracking-wider text-primary">
                  Supervisor
                </span>
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      {/* Navigation Items */}
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {navItems.map((item) => {
                const Icon = item.icon;
                const isActive =
                  item.to === "/" ? currentPath === "/" : currentPath.startsWith(item.to);
                return (
                  <SidebarMenuItem key={item.to}>
                    <SidebarMenuButton
                      isActive={isActive}
                      tooltip={item.label}
                      render={<Link to={item.to} aria-label={item.label} />}
                    >
                      <Icon />
                      <span>{item.label}</span>
                    </SidebarMenuButton>
                    {item.badgeKey === "pools" && totalRunners > 0 && (
                      <SidebarMenuBadge>{totalRunners}</SidebarMenuBadge>
                    )}
                  </SidebarMenuItem>
                );
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      {/* User / Footer */}
      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <DropdownMenu>
              <DropdownMenuTrigger render={<SidebarMenuButton size="lg" />}>
                <Avatar className="size-8 rounded-lg">
                  <AvatarFallback className="rounded-lg text-xs">{initials}</AvatarFallback>
                </Avatar>
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-semibold text-foreground">{displayName}</span>
                  <span className="truncate text-xs text-muted-foreground">Supervisor Admin</span>
                </div>
              </DropdownMenuTrigger>
              <DropdownMenuContent side="right" align="end" className="min-w-56">
                <DropdownMenuGroup>
                  <DropdownMenuLabel className="flex items-center gap-2">
                    <Avatar className="size-8 rounded-lg">
                      <AvatarFallback className="rounded-lg text-xs">{initials}</AvatarFallback>
                    </Avatar>
                    <div className="grid flex-1 text-left text-sm leading-tight">
                      <span className="truncate font-semibold">{displayName}</span>
                      <span className="truncate text-xs font-normal text-muted-foreground">
                        Supervisor Admin
                      </span>
                    </div>
                  </DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="destructive" onClick={handleLogout}>
                    <LogOut />
                    <span>Sign Out</span>
                  </DropdownMenuItem>
                </DropdownMenuGroup>
              </DropdownMenuContent>
            </DropdownMenu>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  );
}

export function AppShell() {
  // Collapsed state persists in localStorage (same pattern as use-theme).
  const [open, setOpen] = useState(() => localStorage.getItem(SIDEBAR_COLLAPSED_KEY) !== "true");

  const handleOpenChange = (value: boolean) => {
    setOpen(value);
    localStorage.setItem(SIDEBAR_COLLAPSED_KEY, String(!value));
  };

  return (
    <SidebarProvider open={open} onOpenChange={handleOpenChange}>
      <NavSidebar />
      <ShellMain />
    </SidebarProvider>
  );
}
