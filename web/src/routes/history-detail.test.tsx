import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { HistoryDetailPage } from "./history-detail";

const mockJob = {
  id: 101n,
  poolId: 10n,
  runnerName: "runnero-arm64-prod-a8f12c",
  status: "success",
  queuedAt: "2026-09-04T00:00:00Z",
  startedAt: "2026-09-04T00:00:03Z",
  completedAt: "2026-09-04T00:02:45Z",
  durationSeconds: 162.0,
  queueTimeSeconds: 3.0,
  poolName: "arm64-prod-pool",
};

const mockLogs = [
  {
    timestamp: "2026-09-04T00:00:03Z",
    stream: "stdout",
    content: "Connected to GitHub Actions API",
  },
  {
    timestamp: "2026-09-04T00:00:05Z",
    stream: "stdout",
    content: "Listening for Jobs...",
  },
];

vi.mock("@tanstack/react-router", () => ({
  useParams: () => ({ jobId: "101" }),
  Link: ({ children, to, ...props }: any) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}));

vi.mock("../lib/api/query-hooks", () => ({
  useJobRecord: () => ({
    data: mockJob,
    isLoading: false,
  }),
  useRunnerLogs: () => ({
    data: mockLogs,
    isLoading: false,
  }),
}));

vi.mock("../lib/api/streaming-hooks", () => ({
  useStreamRunnerLogs: () => ({
    logs: [],
    isConnected: false,
    isConnecting: false,
    clearLogs: vi.fn(),
  }),
}));

describe("HistoryDetailPage", () => {
  it("renders job execution summary metrics and historical log terminal", () => {
    render(<HistoryDetailPage />);

    expect(screen.getByText("runnero-arm64-prod-a8f12c")).toBeInTheDocument();
    expect(screen.getAllByText("arm64-prod-pool").length).toBeGreaterThan(0);
    expect(screen.getByText("2m 42s")).toBeInTheDocument();
    expect(screen.getByText("3.0s")).toBeInTheDocument();
    expect(screen.getByText("Historical Archive")).toBeInTheDocument();
    expect(screen.getByText("Connected to GitHub Actions API")).toBeInTheDocument();
    expect(screen.getByText("Listening for Jobs...")).toBeInTheDocument();
  });
});
