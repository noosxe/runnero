import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { DashboardPage } from "./dashboard";

const mockStats = {
  totalActiveRunners: 3,
  totalIdleRunners: 2,
  averageQueueTimeSeconds: 4.2,
  totalJobs24h: 142,
  successfulJobs24h: 139,
  failedJobs24h: 3,
  averageRuntimeSeconds: 192.0,
  successRatePercent: 97.9,
  knownOutcomeJobs: 142n,
  queueTimedJobs: 142n,
  queueLatencyTrend: [
    {
      timestamp: "2026-09-04T00:00:00Z",
      avgQueueSeconds: 3.5,
      avgRuntimeSeconds: 180.0,
      totalJobs: 20,
      successfulJobs: 20,
      failedJobs: 0,
    },
  ],
};

const mockPools = [
  {
    id: 1n,
    name: "pool-arm64-prod",
    provider: "github",
    repositoryUrl: "https://github.com/org/repo",
    activeRunners: 2,
    minIdleRunners: 1,
    maxConcurrency: 10,
  },
];

const mockHistory = {
  jobs: [
    {
      id: 101n,
      runnerName: "runnero-arm64-prod-a8f12c",
      status: "success",
      durationSeconds: 165.0,
      queueTimeSeconds: 3.2,
      completedAt: "2026-09-04T01:00:00Z",
    },
  ],
};

let currentStats: any = mockStats;
let currentPools: any = mockPools;
let currentHistory: any = mockHistory;
let currentUpdates: any = [];
let currentAuthProfiles: any = [{ id: 1n, name: "default-profile" }];
let isStatsLoading = false;
let isPoolsLoading = false;

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, ...props }: any) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}));

vi.mock("../lib/api/query-hooks", () => ({
  useSystemStats: () => ({
    data: currentStats,
    isLoading: isStatsLoading,
  }),
  usePools: () => ({
    data: currentPools,
    isLoading: isPoolsLoading,
  }),
  useAuthProfiles: () => ({
    data: currentAuthProfiles,
    isLoading: false,
  }),
  useJobHistory: () => ({
    data: currentHistory,
    isLoading: false,
  }),
  useImageUpdates: () => ({
    data: currentUpdates,
    isLoading: false,
  }),
  usePullImage: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
  useDismissImageUpdate: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
}));

describe("DashboardPage", () => {
  beforeEach(() => {
    currentStats = mockStats;
    currentPools = mockPools;
    currentHistory = mockHistory;
    currentUpdates = [];
    currentAuthProfiles = [{ id: 1n, name: "default-profile" }];
    isStatsLoading = false;
    isPoolsLoading = false;
  });

  it("renders KPI cards, analytics graphs, runner pools, and recent executions", () => {
    render(<DashboardPage />);

    expect(screen.getByText("Dashboard Overview")).toBeInTheDocument();
    expect(screen.getByText("3 active")).toBeInTheDocument();
    expect(screen.getByText("142")).toBeInTheDocument();
    expect(screen.getAllByText("97.9%").length).toBeGreaterThan(0);
    expect(screen.getAllByText("3m 12s").length).toBeGreaterThan(0);
    expect(screen.getByText("Avg queue wait: 4.2s")).toBeInTheDocument();
    expect(screen.getByText("139 passed / 3 failed")).toBeInTheDocument();
    expect(screen.getByText("pool-arm64-prod")).toBeInTheDocument();
    expect(screen.getByText("runnero-arm64-prod-a8f12c")).toBeInTheDocument();
  });

  it("degrades job metrics truthfully when denominators are empty (docs/21 §5.6)", () => {
    currentStats = {
      ...mockStats,
      totalJobs24h: 5,
      successfulJobs24h: 5,
      failedJobs24h: 0,
      successRatePercent: 100,
      knownOutcomeJobs: 0n,
      queueTimedJobs: 0n,
    };

    render(<DashboardPage />);

    // Known-outcome denominator empty: no fabricated 100% — an explicit "—".
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
    expect(screen.getAllByText("No concluded jobs in window").length).toBeGreaterThan(0);
    expect(screen.getByText("Queue timing requires webhooks")).toBeInTheDocument();
    // ...while the raw totals stay truthful.
    expect(screen.getAllByText("5").length).toBeGreaterThan(0);
  });

  it("renders empty state when no runner pools or job executions exist", () => {
    currentPools = [];
    currentHistory = { jobs: [] };
    currentStats = {
      totalActiveRunners: 0,
      totalIdleRunners: 0,
      averageQueueTimeSeconds: 0,
      totalJobs24h: 0,
      successfulJobs24h: 0,
      failedJobs24h: 0,
      averageRuntimeSeconds: 0,
      successRatePercent: 100,
      queueLatencyTrend: [],
    };

    render(<DashboardPage />);

    expect(screen.getByText("No runner pools configured yet")).toBeInTheDocument();
    expect(screen.getByText("Create First Pool")).toBeInTheDocument();
    expect(screen.getByText("No executions recorded in the last 24h.")).toBeInTheDocument();
    expect(screen.getByText("0 active")).toBeInTheDocument();
    expect(screen.getByText("0 warm idle standby")).toBeInTheDocument();
  });

  it("renders setup wizard CTA in empty state when no auth profiles exist", () => {
    currentPools = [];
    currentAuthProfiles = [];
    currentHistory = { jobs: [] };

    render(<DashboardPage />);

    expect(screen.getByText("No runner pools configured yet")).toBeInTheDocument();
    expect(screen.getByText("Connect Git Provider")).toBeInTheDocument();
  });

  it("renders loading indicators when data is fetching", () => {
    isStatsLoading = true;
    isPoolsLoading = true;

    render(<DashboardPage />);

    expect(screen.getByText("Loading pools...")).toBeInTheDocument();
    expect(screen.getAllByText("...").length).toBeGreaterThan(0);
  });

  it("renders pending image update notification when updates exist", () => {
    currentUpdates = [
      {
        id: 101n,
        poolId: 1n,
        poolName: "pool-arm64-prod",
        currentDigest: "sha256:1111",
        latestDigest: "sha256:2222",
        checkedAt: "2026-09-04T00:00:00Z",
      },
    ];

    render(<DashboardPage />);

    expect(screen.getByText(/Runner Image Update Available/i)).toBeInTheDocument();
  });
});
