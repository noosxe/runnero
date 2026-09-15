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
import { PoolDetailPage } from "./routes/pool-detail";
import { HistoryPage } from "./routes/history";
import { HistoryDetailPage } from "./routes/history-detail";
import { ProfilesPage } from "./routes/profiles";
import { RenovatePage } from "./routes/renovate";
import { SettingsPage } from "./routes/settings";
import { LoginPage } from "./routes/login";
import { OnboardingPage } from "./routes/onboarding";
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
  component: PoolDetailPage,
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
  component: SettingsPage,
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
