import { describe, it, expect, vi, beforeAll } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { AppRouter, router } from "./router";

// Mock the query hooks and fetchers to return instant authenticated state
vi.mock("./lib/api/query-hooks", () => ({
  fetchOnboardingStatus: vi.fn().mockResolvedValue({
    setupComplete: true,
    adminCreated: true,
    authProfileExists: true,
    poolExists: true,
  }),
  fetchSession: vi.fn().mockResolvedValue({
    username: "admin",
    isAdmin: true,
  }),
  useLogout: () => vi.fn(),
  useSystemStats: () => ({
    data: { totalActiveRunners: 3, totalIdleRunners: 2 },
    isLoading: false,
  }),
  useSession: () => ({
    data: { username: "admin", isAdmin: true },
    isLoading: false,
  }),
  usePools: () => ({
    data: [
      {
        id: 1n,
        name: "test-pool",
        provider: "github",
        repositoryUrl: "https://github.com/test/repo",
        activeRunners: 1,
        minIdleRunners: 1,
        maxConcurrency: 5,
      },
    ],
    isLoading: false,
  }),
  useJobHistory: () => ({
    data: { jobs: [], totalCount: 0 },
    isLoading: false,
  }),
  useImageUpdates: () => ({
    data: [],
    isLoading: false,
  }),
  useAuthProfiles: () => ({
    data: [{ id: 1n, name: "test-auth-profile" }],
    isLoading: false,
  }),
}));

describe("AppRouter", () => {
  // Load once per file: a second router.load() in a later test hangs the
  // suite (the promise never settles), so tests must only render.
  beforeAll(async () => {
    await router.load();
  });

  it("renders AppShell with navigation and dashboard overview", async () => {
    await router.load();
    render(<AppRouter />);

    await waitFor(() => {
      expect(screen.getByText("Runnero")).toBeInTheDocument();
      expect(screen.getByText("Supervisor")).toBeInTheDocument();
      expect(screen.getByText("Dashboard Overview")).toBeInTheDocument();
    });
  });

  it("opens the footer user menu with a Sign Out item", async () => {
    render(<AppRouter />);

    await waitFor(() => {
      expect(screen.getByText("Runnero")).toBeInTheDocument();
    });

    // The user row is a DropdownMenu trigger; opening it must not crash
    // (Menu.GroupLabel requires a Menu.Group context in Base UI).
    fireEvent.click(screen.getByRole("button", { name: /admin/i }));

    expect(await screen.findByRole("menuitem", { name: /sign out/i })).toBeVisible();
  });
});
