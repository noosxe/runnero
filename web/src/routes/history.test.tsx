import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { HistoryPage } from "./history";

const mockPools = [
  { id: 10n, name: "arm64-prod-pool" },
  { id: 20n, name: "gitea-ci-pool" },
];

const mockJobs = [
  {
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
  },
  {
    id: 102n,
    poolId: 20n,
    runnerName: "runnero-gitea-dind-99c01b",
    status: "failure",
    queuedAt: "2026-09-04T00:05:00Z",
    startedAt: "2026-09-04T00:05:05Z",
    completedAt: "2026-09-04T00:05:25Z",
    durationSeconds: 20.0,
    queueTimeSeconds: 5.0,
    poolName: "gitea-ci-pool",
  },
];

let mockJobHistoryParams: any = null;

vi.mock("../lib/api/query-hooks", () => ({
  usePools: () => ({
    data: mockPools,
    isLoading: false,
  }),
  useJobHistory: (params: any) => {
    mockJobHistoryParams = params;
    return {
      data: {
        jobs: mockJobs,
        totalCount: 2,
      },
      isLoading: false,
      isFetching: false,
    };
  },
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, onClick, ...props }: any) => (
    <a
      href={to}
      onClick={(e) => {
        e.preventDefault();
        onClick?.(e);
      }}
      {...props}
    >
      {children}
    </a>
  ),
  createLink:
    (Comp: any) =>
    ({ children, ...props }: any) => <Comp {...props}>{children}</Comp>,
}));

describe("HistoryPage", () => {
  beforeEach(() => {
    mockJobHistoryParams = null;
    vi.clearAllMocks();
  });

  it("renders job execution table with durations, queue wait times, and pool names", () => {
    render(<HistoryPage />);

    expect(screen.getByText("Job Execution History")).toBeInTheDocument();
    expect(screen.getByText("runnero-arm64-prod-a8f12c")).toBeInTheDocument();
    expect(screen.getByText("runnero-gitea-dind-99c01b")).toBeInTheDocument();
    expect(screen.getAllByText("arm64-prod-pool").length).toBeGreaterThan(0);
    expect(screen.getAllByText("gitea-ci-pool").length).toBeGreaterThan(0);
    expect(screen.getByText("2m 42s")).toBeInTheDocument();
    expect(screen.getByText("20s")).toBeInTheDocument();
    expect(screen.getByText("3.0s")).toBeInTheDocument();
    expect(screen.getByText("5.0s")).toBeInTheDocument();
  });

  it("handles search input filtering", () => {
    render(<HistoryPage />);

    const searchInput = screen.getByPlaceholderText("Search by runner name...");
    fireEvent.change(searchInput, { target: { value: "a8f12c" } });

    expect(mockJobHistoryParams.search).toBe("a8f12c");
    expect(mockJobHistoryParams.offset).toBe(0);
  });

  it("handles pool and status dropdown filtering", async () => {
    render(<HistoryPage />);

    const selects = screen.getAllByRole("combobox");
    const poolSelect = selects[0];
    const statusSelect = selects[1];
    const user = userEvent.setup();

    await user.click(poolSelect);
    await user.click(await screen.findByRole("option", { name: "arm64-prod-pool" }));
    await waitFor(() => expect(mockJobHistoryParams.poolId).toBe(10n));

    await user.click(statusSelect);
    await user.click(await screen.findByRole("option", { name: "Success" }));
    await waitFor(() => expect(mockJobHistoryParams.status).toBe("success"));
  });

  it("handles CSV export click", () => {
    const createObjectURLMock = vi.fn().mockReturnValue("blob:mock-url");
    const revokeObjectURLMock = vi.fn();
    window.URL.createObjectURL = createObjectURLMock;
    window.URL.revokeObjectURL = revokeObjectURLMock;
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    render(<HistoryPage />);

    const exportBtn = screen.getByRole("button", { name: /export csv/i });
    fireEvent.click(exportBtn);

    expect(createObjectURLMock).toHaveBeenCalled();
    expect(clickSpy).toHaveBeenCalled();
    clickSpy.mockRestore();
  });
});
