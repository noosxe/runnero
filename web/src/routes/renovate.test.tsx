import { describe, it, expect, vi, beforeEach } from "vitest";
import { createRouterMock } from "@/test/router-mock";
import { render, screen } from "@testing-library/react";
import { RenovatePage } from "./renovate";
import { FEATURE_DISABLED_HINT } from "../lib/feature-gates";

const mockPools = [
  {
    id: 1n,
    name: "arm64-prod-pool",
    provider: "github",
    targetUrls: ["https://github.com/noosxe/runnero"],
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
    targetUrls: ["https://github.com/noosxe/runnero-staging"],
    renovate: {
      enabled: false,
      cronSchedule: "0 4 * * 0",
      image: "renovate/renovate:latest",
    },
  },
];

const mockTriggerAsync = vi.fn();

vi.mock("../lib/api/query-hooks", () => ({
  useIsAdmin: () => true,
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

vi.mock("@tanstack/react-router", () => createRouterMock());

describe("RenovatePage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // v1.0.0 gating (RUN-289): the page stays visible but its interactive
  // content is replaced by the disabled notice.
  it("renders the header with a disabled notice instead of the dashboard (RUN-289)", () => {
    render(<RenovatePage />);

    expect(screen.getByText("Renovate Bot Dashboard")).toBeInTheDocument();
    expect(screen.getByTestId("renovate-disabled-notice")).toBeInTheDocument();
    expect(screen.getByText(FEATURE_DISABLED_HINT)).toBeInTheDocument();

    // No interactive dashboard content leaks through.
    expect(screen.queryByText("Configured Pools")).not.toBeInTheDocument();
    expect(screen.queryAllByRole("button", { name: /trigger/i })).toHaveLength(0);
    expect(mockTriggerAsync).not.toHaveBeenCalled();
  });
});
