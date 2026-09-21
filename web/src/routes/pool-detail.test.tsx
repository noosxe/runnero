import { describe, it, expect, vi, beforeEach } from "vitest";
import { createRouterMock } from "@/test/router-mock";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { PoolDetailPage } from "./pool-detail";

const mockPool = {
  id: 10n,
  name: "arm64-prod-pool",
  provider: "github",
  targetUrls: ["https://github.com/noosxe/runnero"],
  scope: "repo",
  authProfileId: 10n,
  labels: ["self-hosted", "linux"],
  minIdleRunners: 1,
  maxConcurrency: 5,
  activeRunners: 2,
  cpuLimit: "4",
  memoryLimit: "8G",
  allowDocker: true,
  runnerImage: "ghcr.io/noosxe/runnero:latest",
  maxRunnerLifetimeSeconds: 7200,
};

const mockRunners = [
  {
    containerId: "cnt-alpha-1234567890",
    name: "runnero-arm64-alpha",
    poolName: "arm64-prod-pool",
    status: "busy",
    ipAddress: "172.18.0.4",
    uptimeSeconds: 125,
    spawnedAt: "2026-09-04T00:00:00Z",
    cpuLimit: "4",
    memoryLimit: "8G",
  },
  {
    containerId: "cnt-beta-1234567890",
    name: "runnero-arm64-beta",
    poolName: "arm64-prod-pool",
    status: "idle",
    ipAddress: "172.18.0.5",
    uptimeSeconds: 600,
    spawnedAt: "2026-09-04T00:00:00Z",
    cpuLimit: "4",
    memoryLimit: "8G",
  },
];

const mockTerminateMutateAsync = vi.fn();
const mockTriggerRenovateAsync = vi.fn();
const mockUpdatePoolAsync = vi.fn();
const mockCheckImageUpdateMutate = vi.fn();
const mockPullImageMutate = vi.fn();
let mockCheckUpdateState: {
  mutate: (id: bigint) => void;
  isPending: boolean;
  isSuccess: boolean;
  isError: boolean;
  data: any;
  error: any;
} = {
  mutate: mockCheckImageUpdateMutate,
  isPending: false,
  isSuccess: false,
  isError: false,
  data: undefined,
  error: null,
};
let mockImageUpdatesData: any[] = [];

vi.mock("../lib/api/query-hooks", () => ({
  useIsAdmin: () => true,
  usePools: () => ({
    data: [mockPool],
    isLoading: false,
  }),
  useRunners: () => ({
    data: mockRunners,
    isLoading: false,
  }),
  useTerminateRunner: () => ({
    mutateAsync: mockTerminateMutateAsync,
    isPending: false,
  }),
  useCheckImageUpdate: () => mockCheckUpdateState,
  usePullImage: () => ({
    mutate: mockPullImageMutate,
    isPending: false,
  }),
  useImageUpdates: () => ({
    data: mockImageUpdatesData,
    isLoading: false,
  }),
  useRenovateStatus: () => ({
    data: {
      lastRun: {
        id: 101n,
        poolId: 10n,
        status: "success",
        startedAt: "2026-09-04T00:00:00Z",
        completedAt: "2026-09-04T00:01:00Z",
        summary: "1 dependency update PR created",
      },
      nextScheduledRun: "2026-09-05T03:00:00Z",
    },
    isLoading: false,
  }),
  useRenovateHistory: () => ({
    data: {
      runs: [
        {
          id: 101n,
          poolId: 10n,
          status: "success",
          startedAt: "2026-09-04T00:00:00Z",
          completedAt: "2026-09-04T00:01:00Z",
          summary: "1 dependency update PR created",
        },
      ],
      totalCount: 1,
    },
    isLoading: false,
  }),
  useTriggerRenovateRun: () => ({
    mutateAsync: mockTriggerRenovateAsync,
    isPending: false,
  }),
  useCreatePool: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
  useDiscoverTargets: () => ({
    data: { targets: [], installUrl: "", installations: [] },
    isLoading: false,
    error: null,
    refetch: vi.fn(),
  }),
  useAuthProfiles: () => ({
    data: [{ id: 10n, name: "test-profile", authMethod: "pat" }],
    isLoading: false,
  }),
  useSession: () => ({
    data: { hostOs: "linux", hostArch: "amd64" },
    isLoading: false,
  }),
  useUpdatePool: () => ({
    mutateAsync: mockUpdatePoolAsync,
    isPending: false,
  }),
}));

vi.mock("../lib/api/streaming-hooks", () => ({
  useWatchRunners: () => ({
    isConnected: true,
  }),
  useStreamRunnerLogs: () => ({
    logs: [{ content: "Runner connected to GitHub\n" }],
    isConnected: true,
  }),
}));

vi.mock("@tanstack/react-router", () =>
  createRouterMock({
    useParams: () => ({ poolId: "10" }),
  }),
);

describe("PoolDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders pool overview, live badges, and active runners table", () => {
    render(<PoolDetailPage tab="runners" />);

    expect(screen.getByText("arm64-prod-pool")).toBeInTheDocument();
    expect(screen.getByText("Live Orchestrator Stream")).toBeInTheDocument();
    expect(screen.getByText("Active Running Jobs")).toBeInTheDocument();
    expect(screen.getByText("cnt-alpha-12")).toBeInTheDocument();
    expect(screen.getByText("runnero-arm64-alpha")).toBeInTheDocument();
    expect(screen.getByText("busy")).toBeInTheDocument();
    expect(screen.getByText("idle")).toBeInTheDocument();
    expect(screen.getByText("2m 5s")).toBeInTheDocument();
  });

  it("renders the tab strip as links owned by the router (RUN-285)", () => {
    const { rerender } = render(<PoolDetailPage tab="runners" />);

    // Each tab is a link to its path segment; the active one is marked.
    const configLink = screen.getByRole("tab", { name: /pool configuration/i });
    expect(configLink).toHaveAttribute("href", "/pools/$poolId/config");
    expect(screen.getByRole("tab", { name: /active containers/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(configLink).toHaveAttribute("aria-selected", "false");

    // The router owns the tab: rendering the config segment shows the content.
    rerender(<PoolDetailPage tab="config" />);
    expect(screen.getByText("Runner Container Image")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /pool configuration/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("opens live runner logs viewer modal", async () => {
    render(<PoolDetailPage tab="runners" />);

    fireEvent.click(screen.getAllByRole("button", { name: "Runner actions" })[0]);

    const logsItem = await screen.findByRole("menuitem", { name: /logs/i });
    fireEvent.click(logsItem);

    expect(screen.getByText("Live Stream")).toBeInTheDocument();
    expect(screen.getByText("Runner connected to GitHub")).toBeInTheDocument();
  });

  it("opens terminate confirmation dialog and triggers terminate mutation", async () => {
    mockTerminateMutateAsync.mockResolvedValueOnce({});
    render(<PoolDetailPage tab="runners" />);

    fireEvent.click(screen.getAllByRole("button", { name: "Runner actions" })[0]);

    const termItem = await screen.findByRole("menuitem", { name: /terminate/i });
    fireEvent.click(termItem);

    expect(screen.getByText("Terminate Runner Instance?")).toBeInTheDocument();

    const confirmBtn = screen.getByRole("button", {
      name: "Terminate Instance",
    });
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      expect(mockTerminateMutateAsync).toHaveBeenCalledWith({
        poolId: 10n,
        containerId: "cnt-alpha-1234567890",
      });
    });
  });

  // v1.0.0 gating (RUN-289): the Renovate tab stays visible but is not a
  // link, and the route renders the disabled notice instead of the tab body.
  it("renders the Renovate tab disabled with a notice instead of automation content (RUN-289)", () => {
    render(<PoolDetailPage tab="renovate" />);

    const renovateTab = screen.getByRole("tab", { name: /renovate bot/i });
    expect(renovateTab).toHaveAttribute("aria-disabled", "true");
    expect(renovateTab).toHaveAttribute("aria-selected", "false");

    expect(screen.getByTestId("renovate-disabled-notice")).toBeInTheDocument();
    expect(screen.queryByText("Renovate Status & Automation")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /trigger renovate run/i })).not.toBeInTheDocument();
    expect(mockTriggerRenovateAsync).not.toHaveBeenCalled();
  });

  it("triggers Check for Updates in Pool Configuration tab", async () => {
    render(<PoolDetailPage tab="config" />);

    expect(screen.getByText("Runner Container Image")).toBeInTheDocument();
    expect(screen.getByText("ghcr.io/noosxe/runnero:latest")).toBeInTheDocument();

    const checkBtn = screen.getByRole("button", { name: /check for updates/i });
    fireEvent.click(checkBtn);

    expect(mockCheckImageUpdateMutate).toHaveBeenCalledWith(10n);
  });

  it("displays update available feedback when remote digest differs and triggers pull", () => {
    mockCheckUpdateState = {
      ...mockCheckUpdateState,
      isSuccess: true,
      data: {
        updateAvailable: true,
        update: {
          poolId: 10n,
          currentDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
          latestDigest: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
        },
      },
    };

    render(<PoolDetailPage tab="config" />);

    expect(screen.getByText(/update available/i)).toBeInTheDocument();
    expect(screen.getByText(/sha256:22222222222/)).toBeInTheDocument();

    const pullBtn = screen.getByRole("button", { name: /pull update/i });
    fireEvent.click(pullBtn);

    expect(mockPullImageMutate).toHaveBeenCalledWith(10n);
  });

  it("displays up-to-date feedback when no update is available", () => {
    mockCheckUpdateState = {
      ...mockCheckUpdateState,
      isSuccess: true,
      data: {
        updateAvailable: false,
      },
    };

    render(<PoolDetailPage tab="config" />);

    expect(screen.getByText("Image is up-to-date with registry")).toBeInTheDocument();
  });

  it("lists every target in the configuration tab and badges the header", () => {
    const original = mockPool.targetUrls;
    mockPool.targetUrls = [
      "https://github.com/noosxe/runnero",
      "https://github.com/noosxe/frontend",
    ];
    try {
      render(<PoolDetailPage tab="config" />);

      // Header carries only the count badge; target URLs live in the
      // configuration tab
      expect(screen.getByText("2 repos")).toBeInTheDocument();

      expect(screen.getByText("Target Repositories")).toBeInTheDocument();
      // Every target is listed exactly once (header no longer repeats the first)
      expect(screen.getByText("https://github.com/noosxe/runnero")).toBeInTheDocument();
      expect(screen.getByText("https://github.com/noosxe/frontend")).toBeInTheDocument();
    } finally {
      mockPool.targetUrls = original;
    }
  });

  it("copies runner labels as a paste-ready runs-on list", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });

    render(<PoolDetailPage tab="config" />);

    fireEvent.click(screen.getByRole("button", { name: "Copy labels" }));

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith("self-hosted, linux");
    });
    expect(screen.getByText("Copied")).toBeInTheDocument();
  });
});
