import { Link, Outlet, useLocation } from "@tanstack/react-router";
import { MonitorSmartphone, ShieldCheck } from "lucide-react";
import { usePageTitle } from "../hooks/use-page-title";
import { useSession } from "../lib/api/query-hooks";
import { cn } from "cn";

/**
 * Account page (RUN-282, docs/37): the caller's personal surfaces, reached
 * only from the sidebar footer menu. Tabs are path-based navigation — each
 * tab is its own route (/account/security, /account/sessions), so deep
 * links, refreshes, and back/forward restore the tab without query params.
 * Both tabs are available to every role.
 */
const ACCOUNT_TABS = [
  { to: "/account/security", label: "Security", icon: ShieldCheck },
  { to: "/account/sessions", label: "Sessions", icon: MonitorSmartphone },
] as const;

export function AccountPage() {
  usePageTitle("Account");
  const location = useLocation();
  const { data: session } = useSession();
  const roleLabel = session?.role === "admin" ? "Administrator" : "Viewer";

  return (
    <div className="flex flex-col gap-6">
      {/* Header */}
      <div>
        <h1 className="text-2xl font-bold tracking-tight text-foreground">Account</h1>
        <p className="text-sm text-muted-foreground">
          {session?.username ?? "Your account"} &middot; {roleLabel}
        </p>
      </div>

      {/* Navigation Tabs (path-based, RUN-282) */}
      <div className="flex border-b border-border">
        {ACCOUNT_TABS.map((tab) => (
          <Link
            key={tab.to}
            to={tab.to}
            aria-current={location.pathname === tab.to ? "page" : undefined}
            className={cn(
              "flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors",
              location.pathname === tab.to
                ? "border-primary/50 text-link"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            <tab.icon className="size-4" />
            <span>{tab.label}</span>
          </Link>
        ))}
      </div>

      <Outlet />
    </div>
  );
}
