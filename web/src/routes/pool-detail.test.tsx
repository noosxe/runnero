import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { PoolDetailPage } from "./pool-detail";

const mockPool = {
  id: 10n,
  name: "arm64-prod-pool",
  provider: "github",
  repositoryUrl: "https://github.com/noosxe/runnero",
  scope: "repo",
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

vi.mock("@tanstack/react-router", () => ({
  useParams: () => ({ poolId: "10" }),
  Link: ({ children, to, ...props }: any) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}));

describe("PoolDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders pool overview, live badges, and active runners table", () => {
    render(<PoolDetailPage />);

    expect(screen.getByText("arm64-prod-pool")).toBeInTheDocument();
    expect(screen.getByText("Live Orchestrator Stream")).toBeInTheDocument();
    expect(screen.getByText("Active Running Jobs")).toBeInTheDocument();
    expect(screen.getByText("cnt-alpha-12")).toBeInTheDocument();
    expect(screen.getByText("runnero-arm64-alpha")).toBeInTheDocument();
    expect(screen.getByText("busy")).toBeInTheDocument();
    expect(screen.getByText("idle")).toBeInTheDocument();
    expect(screen.getByText("2m 5s")).toBeInTheDocument();
  });

  it("opens live runner logs viewer modal", () => {
    render(<PoolDetailPage />);

    const logsButtons = screen.getAllByRole("button", { name: /logs/i });
    fireEvent.click(logsButtons[0]);

    expect(screen.getByText("Live Stream")).toBeInTheDocument();
    expect(screen.getByText("Runner connected to GitHub")).toBeInTheDocument();
  });

  it("opens terminate confirmation dialog and triggers terminate mutation", async () => {
    mockTerminateMutateAsync.mockResolvedValueOnce({});
    render(<PoolDetailPage />);

    const termButtons = screen.getAllByRole("button", { name: /terminate/i });
    fireEvent.click(termButtons[0]);

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

  it("switches to Renovate tab and triggers manual run", async () => {
    mockTriggerRenovateAsync.mockResolvedValueOnce({
      success: true,
      runId: 102n,
    });
    render(<PoolDetailPage />);

    const renovateTabBtn = screen.getByRole("button", { name: /renovate bot/i });
    fireEvent.click(renovateTabBtn);

    expect(screen.getByText("Renovate Status & Automation")).toBeInTheDocument();
    expect(screen.getAllByText("1 dependency update PR created")).toHaveLength(2);

    const triggerBtn = screen.getByRole("button", { name: /trigger renovate run/i });
    fireEvent.click(triggerBtn);

    await waitFor(() => {
      expect(mockTriggerRenovateAsync).toHaveBeenCalledWith(10n);
    });
  });

  it("triggers Check for Updates in Pool Configuration tab", async () => {
    render(<PoolDetailPage />);

    const configTabBtn = screen.getByRole("button", { name: /pool configuration/i });
    fireEvent.click(configTabBtn);

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

    render(<PoolDetailPage />);

    const configTabBtn = screen.getByRole("button", { name: /pool configuration/i });
    fireEvent.click(configTabBtn);

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

    render(<PoolDetailPage />);

    const configTabBtn = screen.getByRole("button", { name: /pool configuration/i });
    fireEvent.click(configTabBtn);

    expect(screen.getByText("Image is up-to-date with registry")).toBeInTheDocument();
  });
});
