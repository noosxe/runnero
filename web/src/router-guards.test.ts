import { createElement, type ReactNode } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  fetchOnboardingStatus,
  fetchSession,
  useCompleteOnboarding,
  useOnboardingStatus,
} from "./lib/api/query-hooks";
import { onboardingClient, authClient } from "./lib/api/transport";
import { loginRoute, onboardingRoute, authenticatedRoute, queryClient } from "./router";

describe("Route Guards & Redirect Matrix Logic", () => {
  let testQueryClient: QueryClient;

  beforeEach(() => {
    testQueryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
      },
    });
    queryClient.setDefaultOptions({
      queries: { retry: false },
    });
    queryClient.clear();
    vi.clearAllMocks();
  });

  describe("Query Fetchers", () => {
    it("Case 1: setupComplete is false -> uninitialized system", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockResolvedValue({
        setupComplete: false,
        adminCreated: false,
        authProfileExists: false,
        poolExists: false,
      } as any);

      const status = await fetchOnboardingStatus(testQueryClient);
      expect(status.setupComplete).toBe(false);
    });

    it("Case 2: setupComplete is true, unauthenticated session -> returns null session", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockResolvedValue({
        setupComplete: true,
        adminCreated: true,
        authProfileExists: true,
        poolExists: true,
      } as any);
      vi.spyOn(authClient, "getSession").mockRejectedValue(new Error("unauthenticated"));

      const status = await fetchOnboardingStatus(testQueryClient);
      const session = await fetchSession(testQueryClient);

      expect(status.setupComplete).toBe(true);
      expect(session).toBeNull();
    });

    it("Case 3: setupComplete is true, authenticated session -> returns active session", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockResolvedValue({
        setupComplete: true,
        adminCreated: true,
        authProfileExists: true,
        poolExists: true,
      } as any);
      vi.spyOn(authClient, "getSession").mockResolvedValue({
        username: "admin",
        isAdmin: true,
      } as any);

      const status = await fetchOnboardingStatus(testQueryClient);
      const session = await fetchSession(testQueryClient);

      expect(status.setupComplete).toBe(true);
      expect(session).not.toBeNull();
      expect(session?.username).toBe("admin");
    });
  });

  describe("Route beforeLoad Redirection Matrix", () => {
    it("Uninitialized system: redirects login and authenticated routes to /onboarding", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockResolvedValue({
        setupComplete: false,
        adminCreated: false,
        authProfileExists: false,
        poolExists: false,
      } as any);

      // loginRoute beforeLoad should redirect to /onboarding
      await expect(loginRoute.options.beforeLoad?.({} as any)).rejects.toMatchObject({
        options: { to: "/onboarding" },
      });

      // authenticatedRoute beforeLoad should redirect to /onboarding
      await expect(
        authenticatedRoute.options.beforeLoad?.({
          location: { pathname: "/pools" },
        } as any),
      ).rejects.toMatchObject({
        options: { to: "/onboarding" },
      });

      // onboardingRoute beforeLoad should succeed without redirection
      await expect(onboardingRoute.options.beforeLoad?.({} as any)).resolves.toBeUndefined();
    });

    it("Initialized but unauthenticated: redirects /onboarding and authenticated routes to /login", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockResolvedValue({
        setupComplete: true,
        adminCreated: true,
        authProfileExists: true,
        poolExists: true,
      } as any);
      vi.spyOn(authClient, "getSession").mockRejectedValue(new Error("unauthenticated"));

      // onboardingRoute beforeLoad should redirect to /login
      await expect(onboardingRoute.options.beforeLoad?.({} as any)).rejects.toMatchObject({
        options: { to: "/login" },
      });

      // authenticatedRoute beforeLoad should redirect to /login with target redirect query
      await expect(
        authenticatedRoute.options.beforeLoad?.({
          location: { pathname: "/pools" },
        } as any),
      ).rejects.toMatchObject({
        options: {
          to: "/login",
          search: { redirect: "/pools" },
        },
      });

      // loginRoute beforeLoad should succeed without redirection
      await expect(loginRoute.options.beforeLoad?.({} as any)).resolves.toBeUndefined();
    });

    it("Admin created but onboarding incomplete and unauthenticated: allows /login without redirection", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockResolvedValue({
        setupComplete: false,
        adminCreated: true,
        authProfileExists: false,
        poolExists: false,
        onboardingCompleted: false,
      } as any);
      vi.spyOn(authClient, "getSession").mockRejectedValue(new Error("unauthenticated"));

      // loginRoute beforeLoad should succeed, allowing operator to authenticate
      await expect(loginRoute.options.beforeLoad?.({} as any)).resolves.toBeUndefined();

      // onboardingRoute beforeLoad should also succeed
      await expect(onboardingRoute.options.beforeLoad?.({} as any)).resolves.toBeUndefined();
    });

    it("Initialized and authenticated: redirects /login and /onboarding to /", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockResolvedValue({
        setupComplete: true,
        adminCreated: true,
        authProfileExists: true,
        poolExists: true,
      } as any);
      vi.spyOn(authClient, "getSession").mockResolvedValue({
        username: "admin",
        isAdmin: true,
      } as any);

      // loginRoute beforeLoad should redirect to /
      await expect(loginRoute.options.beforeLoad?.({} as any)).rejects.toMatchObject({
        options: { to: "/" },
      });

      // onboardingRoute beforeLoad should redirect to /
      await expect(onboardingRoute.options.beforeLoad?.({} as any)).rejects.toMatchObject({
        options: { to: "/" },
      });

      // authenticatedRoute beforeLoad should return session and onboarding
      const context = await authenticatedRoute.options.beforeLoad?.({
        location: { pathname: "/pools" },
      } as any);
      expect(context).toMatchObject({
        session: { username: "admin" },
        onboarding: { setupComplete: true },
      });
    });

    it("Optional onboarding completion (admin created, zero pools, onboardingCompleted: true) allows access to authenticated routes", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockResolvedValue({
        setupComplete: true,
        adminCreated: true,
        authProfileExists: false,
        poolExists: false,
        onboardingCompleted: true,
      } as any);
      vi.spyOn(authClient, "getSession").mockResolvedValue({
        username: "admin",
        isAdmin: true,
      } as any);

      // onboardingRoute redirects to /
      await expect(onboardingRoute.options.beforeLoad?.({} as any)).rejects.toMatchObject({
        options: { to: "/" },
      });

      // authenticatedRoute beforeLoad succeeds with session and onboarding
      const context = await authenticatedRoute.options.beforeLoad?.({
        location: { pathname: "/" },
      } as any);
      expect(context).toMatchObject({
        session: { username: "admin" },
        onboarding: {
          setupComplete: true,
          adminCreated: true,
          poolExists: false,
          onboardingCompleted: true,
        },
      });
    });
  });

  describe("Guard error handling (RUN-243 fail-closed)", () => {
    it("fetchOnboardingStatus rejects on RPC failure — never synthesizes adminCreated=false", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockRejectedValue(
        new Error("connection lost"),
      );

      await expect(fetchOnboardingStatus(testQueryClient)).rejects.toThrow("connection lost");
    });

    it("guarded beforeLoads propagate the error instead of redirecting to /onboarding or /login", async () => {
      vi.spyOn(onboardingClient, "getOnboardingStatus").mockRejectedValue(
        new Error("connection lost"),
      );

      // A redirect rejection carries { options: { to } }; a propagated error is a
      // plain Error the router renders via GuardErrorPage.
      await expect(loginRoute.options.beforeLoad?.({} as any)).rejects.toThrow("connection lost");
      await expect(onboardingRoute.options.beforeLoad?.({} as any)).rejects.toThrow(
        "connection lost",
      );
      await expect(
        authenticatedRoute.options.beforeLoad?.({
          location: { pathname: "/pools" },
        } as any),
      ).rejects.toThrow("connection lost");
    });
  });

  // Flow 02 E2E flake: the wizard invalidates the cached status
  // (setupComplete=false) on CompleteOnboarding and navigates to "/" right
  // away, while the background refetch is still in flight. The guard must
  // honor the invalidation and wait for the fresh status instead of
  // redirecting back to /onboarding on the stale cache.
  describe("Guards after an invalidating mutation", () => {
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client: queryClient }, children);

    const incomplete = {
      setupComplete: false,
      adminCreated: true,
      authProfileExists: true,
      poolExists: false,
      onboardingCompleted: false,
    };

    async function mountWizardStatus() {
      const status = renderHook(() => useOnboardingStatus(), { wrapper });
      await waitFor(() => expect(status.result.current.data?.setupComplete).toBe(false));
      return status;
    }

    it("authenticated guard sees setupComplete=true right after CompleteOnboarding", async () => {
      const getStatus = vi
        .spyOn(onboardingClient, "getOnboardingStatus")
        .mockResolvedValue(incomplete as any);
      vi.spyOn(authClient, "getSession").mockResolvedValue({ username: "admin" } as any);
      vi.spyOn(onboardingClient, "completeOnboarding").mockResolvedValue({ success: true } as any);
      await mountWizardStatus();

      // The post-mutation refetch is slow: the guard runs before it lands.
      let landRefetch!: () => void;
      getStatus.mockImplementation(
        () =>
          new Promise((resolve) => {
            landRefetch = () =>
              resolve({ ...incomplete, setupComplete: true, onboardingCompleted: true } as any);
          }),
      );

      const complete = renderHook(() => useCompleteOnboarding(), { wrapper });
      await complete.result.current.mutateAsync();

      const guard = authenticatedRoute.options.beforeLoad?.({
        location: { pathname: "/" },
      } as any);
      landRefetch();

      await expect(guard).resolves.toMatchObject({ onboarding: { setupComplete: true } });
    });

    it("fails closed when the post-invalidation refetch errors", async () => {
      const getStatus = vi
        .spyOn(onboardingClient, "getOnboardingStatus")
        .mockResolvedValue(incomplete as any);
      vi.spyOn(onboardingClient, "completeOnboarding").mockResolvedValue({ success: true } as any);
      await mountWizardStatus();

      getStatus.mockRejectedValue(new Error("connection lost"));
      const complete = renderHook(() => useCompleteOnboarding(), { wrapper });
      await complete.result.current.mutateAsync();

      await expect(
        authenticatedRoute.options.beforeLoad?.({ location: { pathname: "/" } } as any),
      ).rejects.toThrow("connection lost");
    });
  });
});
