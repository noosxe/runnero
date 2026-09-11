import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { RenovatePage } from "./renovate";
import { Toaster } from "@/components/ui/toast";

const mockPools = [
  {
    id: 1n,
    name: "arm64-prod-pool",
    provider: "github",
    repositoryUrl: "https://github.com/noosxe/runnero",
    renovate: {
      enabled: true,
      cronSchedule: "0 3 * * 1",
      image: "renovate/renovate:latest",
    },
  },
  {
    id: 2n,
    name: "amd64-staging-pool",
    provider: "github",
    repositoryUrl: "https://github.com/noosxe/runnero-staging",
    renovate: {
      enabled: false,
      cronSchedule: "0 4 * * 0",
      image: "renovate/renovate:latest",
    },
  },
];

const mockTriggerAsync = vi.fn();

vi.mock("../lib/api/query-hooks", () => ({
  usePools: () => ({
    data: mockPools,
    isLoading: false,
  }),
  useRenovateStatus: (poolId: bigint) => ({
    data:
      poolId === 1n
        ? {
            lastRun: {
              id: 101n,
              poolId: 1n,
              status: "success",
              completedAt: "2026-09-04T00:01:00Z",
              summary: "Updated 2 packages",
            },
            nextScheduledRun: "2026-09-08T03:00:00Z",
          }
        : undefined,
    isLoading: false,
  }),
  useTriggerRenovateRun: () => ({
    mutateAsync: mockTriggerAsync,
    isPending: false,
  }),
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, ...props }: any) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
  createLink:
    (Comp: any) =>
    ({ children, ...props }: any) => <Comp {...props}>{children}</Comp>,
}));

describe("RenovatePage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders Renovate dashboard with metrics, pool status, and trigger controls", async () => {
    mockTriggerAsync.mockResolvedValueOnce({
      success: true,
      runId: 105n,
    });

    render(
      <>
        <RenovatePage />
        <Toaster />
      </>,
    );

    expect(screen.getByText("Renovate Bot Dashboard")).toBeInTheDocument();
    expect(screen.getByText("Configured Pools")).toBeInTheDocument();
    expect(screen.getByText("Renovate Active")).toBeInTheDocument();
    expect(screen.getByText("arm64-prod-pool")).toBeInTheDocument();
    expect(screen.getByText("amd64-staging-pool")).toBeInTheDocument();
    expect(screen.getByText("Enabled")).toBeInTheDocument();
    expect(screen.getByText("Disabled")).toBeInTheDocument();

    const triggerButtons = screen.getAllByRole("button", { name: /trigger/i });
    fireEvent.click(triggerButtons[0]);

    await waitFor(() => {
      expect(mockTriggerAsync).toHaveBeenCalledWith(1n);
    });

    // Success surfaces through the toast system.
    expect(await screen.findByText("Run #105 triggered")).toBeVisible();
  });
});
