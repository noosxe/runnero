import {
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  redirect,
} from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AppShell } from "./components/layout/app-shell";
import { DashboardPage } from "./routes/dashboard";
import { PoolsPage } from "./routes/pools";
import { LogsPage, type LogsPageSearch } from "./routes/logs";
import { PoolDetailPage, type PoolDetailPageSearch } from "./routes/pool-detail";
import { HistoryPage } from "./routes/history";
import { HistoryDetailPage } from "./routes/history-detail";
import { ProfilesPage } from "./routes/profiles";
import { RenovatePage } from "./routes/renovate";
import { AccountPage } from "./routes/account";
import { AccountSecurityTab } from "./routes/account-security";
import { AccountSessionsTab } from "./routes/account-sessions";
import { SettingsPage, type SettingsPageSearch } from "./routes/settings";
import { LoginPage } from "./routes/login";
import { OnboardingPage } from "./routes/onboarding";
import { GuardErrorPage } from "./routes/guard-error";
import { fetchOnboardingStatus, fetchSession } from "./lib/api/query-hooks";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

// Root Route
const rootRoute = createRootRoute({
  component: () => (
    <QueryClientProvider client={queryClient}>
      <Outlet />
    </QueryClientProvider>
  ),
});

// Public Routes
export const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  errorComponent: GuardErrorPage, // RUN-243: RPC failure fails closed, never into the wizard
  component: LoginPage,
  validateSearch: (search: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof search.redirect === "string" ? search.redirect : undefined,
  }),
  beforeLoad: async () => {
    const onboarding = await fetchOnboardingStatus(queryClient);
    if (!onboarding.adminCreated) {
      throw redirect({ to: "/onboarding" });
    }
    const session = await fetchSession(queryClient);
    if (session) {
      throw redirect({ to: onboarding.setupComplete ? "/" : "/onboarding" });
    }
  },
});

export const onboardingRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/onboarding",
  errorComponent: GuardErrorPage, // RUN-243: RPC failure fails closed, never into the wizard
  component: OnboardingPage,
  beforeLoad: async () => {
    const onboarding = await fetchOnboardingStatus(queryClient);
    if (onboarding.setupComplete) {
      const session = await fetchSession(queryClient);
      if (session) {
        throw redirect({ to: "/" });
      }
      throw redirect({ to: "/login" });
    }
  },
});

// Authenticated App Shell Layout
export const authenticatedRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "_authenticated",
  errorComponent: GuardErrorPage, // RUN-243: RPC failure fails closed, never into the wizard
  component: AppShell,
  beforeLoad: async ({ location }) => {
    const onboarding = await fetchOnboardingStatus(queryClient);
    if (!onboarding.setupComplete) {
      throw redirect({ to: "/onboarding" });
    }
    const session = await fetchSession(queryClient);
    if (!session) {
      throw redirect({
        to: "/login",
        search: {
          redirect: location.pathname !== "/" ? location.pathname : undefined,
        },
      });
    }
    return { session, onboarding };
  },
});

// Nested Authenticated Child Routes
const indexRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/",
  component: DashboardPage,
});

const poolsRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/pools",
  component: PoolsPage,
});

const poolDetailRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/pools/$poolId",
  // Tab is URL state (RUN-258): deep links like /pools/10?tab=config work
  // and back/forward walks the tab history. Values are clamped against the
  // tab list in PoolDetailPage (runners is the default).
  validateSearch: (search: Record<string, unknown>): PoolDetailPageSearch => ({
    tab: typeof search.tab === "string" ? search.tab : undefined,
  }),
  component: function PoolDetailRouteComponent() {
    const search = poolDetailRoute.useSearch();
    return <PoolDetailPage search={search} />;
  },
});

const historyRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/history",
  component: HistoryPage,
});

const historyDetailRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/history/$jobId",
  component: HistoryDetailPage,
});

const logsRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/logs",
  validateSearch: (search: Record<string, unknown>): LogsPageSearch => ({
    tab: typeof search.tab === "string" ? search.tab : undefined,
    runner: typeof search.runner === "string" ? search.runner : undefined,
    boot: typeof search.boot === "string" ? search.boot : undefined,
  }),
  component: function LogsRouteComponent() {
    const search = logsRoute.useSearch();
    return <LogsPage search={search} />;
  },
});

// Account (RUN-282, docs/37): personal surfaces behind the footer menu.
// Tabs are path segments, not ?tab= params — each tab is its own route so
// deep links, refresh, and back/forward restore it without clamping.
const accountRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/account",
  component: AccountPage,
});

const accountIndexRoute = createRoute({
  getParentRoute: () => accountRoute,
  path: "/",
  beforeLoad: async () => {
    // /account is not a surface of its own: canonicalize to the first tab.
    throw redirect({ to: "/account/security", replace: true });
  },
});

const accountSecurityRoute = createRoute({
  getParentRoute: () => accountRoute,
  path: "security",
  component: AccountSecurityTab,
});

const accountSessionsRoute = createRoute({
  getParentRoute: () => accountRoute,
  path: "sessions",
  component: AccountSessionsTab,
});

const profilesRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/profiles",
  component: ProfilesPage,
});

const renovateRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/renovate",
  component: RenovatePage,
});

const settingsRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: "/settings",
  // Tab is URL state (RUN-257): deep links like /settings?tab=users work
  // and back/forward walks the tab history. Values are clamped against the
  // role-scoped tab list in SettingsPage (admins default to constraints,
  // viewers to security).
  validateSearch: (search: Record<string, unknown>): SettingsPageSearch => ({
    tab: typeof search.tab === "string" ? search.tab : undefined,
  }),
  component: function SettingsRouteComponent() {
    const search = settingsRoute.useSearch();
    return <SettingsPage search={search} />;
  },
});

// Route Tree
const routeTree = rootRoute.addChildren([
  loginRoute,
  onboardingRoute,
  authenticatedRoute.addChildren([
    indexRoute,
    poolsRoute,
    poolDetailRoute,
    historyRoute,
    historyDetailRoute,
    logsRoute,
    profilesRoute,
    renovateRoute,
    accountRoute.addChildren([accountIndexRoute, accountSecurityRoute, accountSessionsRoute]),
    settingsRoute,
  ]),
]);

export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

export function AppRouter() {
  return <RouterProvider router={router} />;
}
