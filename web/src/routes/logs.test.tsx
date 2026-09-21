import { describe, it, expect, vi, beforeEach } from "vitest";
import { createRouterMock } from "@/test/router-mock";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { LogsPage } from "./logs";
import type { SupervisorBootLog, RemovalRecordSummary } from "@/gen/api_pb";

const mockBoots = [
  {
    file: "boot-1737000100-cafe0001.ndjson",
    bootId: "cafe0001aaaa",
    startedAt: "2026-09-15T11:00:00Z",
    sizeBytes: 2048n,
    rotationSeq: 0,
    isCurrent: true,
  },
  {
    file: "boot-1736900000-beef0002-01.ndjson",
    bootId: "beef0002bbbb",
    startedAt: "2026-09-14T09:00:00Z",
    sizeBytes: 1048576n,
    rotationSeq: 1,
    isCurrent: false,
  },
] as SupervisorBootLog[];

const mockRecords = [
  {
    ts: "2026-09-15T10:30:00Z",
    bootId: "cafe0001aaaa",
    runnerId: "runner-id-123",
    runnerName: "runnero-pool1-abc123",
    poolId: 7n,
    poolName: "pool1",
    reason: "task-exit",
    providerBusy: true,
    deregError: "",
    exitCode: 137,
    captureOk: true,
    captureBytes: 4096n,
  },
  {
    ts: "2026-09-15T09:15:00Z",
    bootId: "",
    runnerId: "runner-id-456",
    runnerName: "runnero-pool2-def456",
    poolId: 8n,
    poolName: "pool2",
    reason: "manual",
    providerBusy: false,
    deregError: "rate limited",
    exitCode: -1,
    captureOk: false,
    captureBytes: 0n,
  },
] as RemovalRecordSummary[];

let mockRemovalFilters: Record<string, unknown> | null = null;

const mockNavigate = vi.fn();

vi.mock("@/lib/api/query-hooks", () => ({
  useIsAdmin: () => true,
  usePools: () => ({
    data: [
      { id: 7n, name: "pool1" },
      { id: 8n, name: "pool2" },
    ],
    isLoading: false,
  }),
  useSupervisorBoots: () => ({
    data: mockBoots,
    isLoading: false,
  }),
  useRemovalRecords: (filters: Record<string, unknown>) => {
    mockRemovalFilters = filters;
    return {
      data: {
        pages: [{ records: mockRecords, nextCursor: "" }],
        pageParams: [""],
      },
      isLoading: false,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
    };
  },
  useRunnerLogs: (_runnerId: string, enabled: boolean) => ({
    data: enabled
      ? [{ timestamp: "2026-09-15T10:30:00Z", stream: "stdout", content: "captured line" }]
      : undefined,
    isLoading: false,
  }),
}));

vi.mock("@/lib/api/streaming-hooks", () => ({
  useSupervisorBootReplay: (file: string, enabled: boolean) => ({
    logs:
      enabled && file
        ? [{ timestamp: "2026-09-15T11:00:00Z", stream: "stdout", content: "boot line" }]
        : [],
    isLoading: false,
    error: null,
  }),
  useStreamSupervisorLogFollow: () => ({
    logs: [],
    status: "idle",
    isConnected: false,
    isConnecting: false,
    isReconnecting: false,
    error: null,
  }),
}));

vi.mock("@tanstack/react-router", () => createRouterMock({ useNavigate: () => mockNavigate }));

function renderPage(
  opts: { tab?: "supervisor" | "removals" | "runners"; runner?: string; boot?: string } = {},
) {
  return render(<LogsPage tab={opts.tab ?? "supervisor"} runner={opts.runner} boot={opts.boot} />);
}

describe("LogsPage", () => {
  beforeEach(() => {
    mockRemovalFilters = null;
    mockNavigate.mockClear();
  });

  it("defaults to the supervisor tab and renders boot rows newest first", () => {
    renderPage();

    expect(screen.getByRole("heading", { name: "Logs" })).toBeInTheDocument();
    // Tabs are path segments (RUN-283): the strip links to /logs/<tab>.
    expect(screen.getByTestId("logs-tab-runners").getAttribute("href")).toBe("/logs/runners");
    expect(screen.getAllByTestId("logs-boot-row")).toHaveLength(2);
    // Newest boot (isCurrent) is listed first and carries the badge.
    expect(screen.getAllByTestId("logs-boot-row")[0].getAttribute("data-boot-file")).toBe(
      "boot-1737000100-cafe0001.ndjson",
    );
    expect(screen.getByTestId("logs-boot-current-badge")).toBeInTheDocument();
  });

  it("opens the boot viewer when a boot row is selected", () => {
    renderPage();

    fireEvent.click(screen.getAllByTestId("logs-boot-row")[1]);

    expect(screen.getByTestId("logs-tab-panel-supervisor").textContent).toContain(
      "boot-1736900000-beef0002-01.ndjson",
    );
    // Previous boot: no Follow toggle, historical terminal instead.
    expect(screen.queryByTestId("logs-follow-toggle")).not.toBeInTheDocument();
    expect(screen.getByText("Historical Archive")).toBeInTheDocument();
  });

  it("shows the Follow toggle for the current boot", () => {
    renderPage({ boot: "boot-1737000100-cafe0001.ndjson" });

    expect(screen.getByTestId("logs-follow-toggle")).toBeInTheDocument();
    expect(screen.getByTestId("logs-follow-toggle").getAttribute("aria-pressed")).toBe("false");
  });

  it("renders removal rows with reason badges and capture outcome", () => {
    renderPage({ tab: "removals" });

    expect(screen.getAllByTestId("logs-removal-row")).toHaveLength(2);
    expect(screen.getByText("task-exit")).toBeInTheDocument();
    expect(screen.getByText("manual")).toBeInTheDocument();
    expect(screen.getByText("busy")).toBeInTheDocument();
    expect(screen.getByText("4.0 KiB")).toBeInTheDocument();
    // Records without a boot id cannot offer View boot.
    const viewBootButtons = screen.getAllByTestId("logs-removal-view-boot");
    expect((viewBootButtons[1] as HTMLButtonElement).disabled).toBe(true);
  });

  it("applies filters to the removal records query", async () => {
    const user = userEvent.setup();
    renderPage({ tab: "removals" });

    await user.type(screen.getByTestId("logs-filter-runner"), "runner-id-123");
    fireEvent.click(screen.getByTestId("logs-filter-apply"));

    await waitFor(() => {
      expect(mockRemovalFilters).toMatchObject({ runnerId: "runner-id-123" });
    });
  });

  it("navigates to the runners tab via View capture with the record's runner id", () => {
    renderPage({ tab: "removals" });

    fireEvent.click(screen.getAllByTestId("logs-removal-view-capture")[0]);

    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/logs/runners",
      search: { runner: "runner-id-123" },
    });
  });

  it("navigates to the supervisor tab via View boot with the record's boot id", () => {
    renderPage({ tab: "removals" });

    fireEvent.click(screen.getAllByTestId("logs-removal-view-boot")[0]);

    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/logs/supervisor",
      search: { boot: "cafe0001aaaa" },
    });
  });

  it("deep-link prefill loads the runner capture", () => {
    renderPage({ tab: "runners", runner: "runner-id-123" });

    expect(screen.getByTestId("logs-runner-input")).toHaveValue("runner-id-123");
    expect(screen.getByText("captured line")).toBeInTheDocument();
  });
});
