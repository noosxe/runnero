import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { PoolsPage } from "./pools";

const mockPools = [
  {
    id: 1n,
    name: "arm64-prod-pool",
    provider: "github",
    repositoryUrl: "https://github.com/noosxe/runnero",
    scope: "repo",
    minIdleRunners: 2,
    maxConcurrency: 10,
    activeRunners: 3,
    cpuLimit: "4",
    memoryLimit: "8G",
    allowDocker: true,
    runnerImage: "ghcr.io/noosxe/runnero:latest",
    maxRunnerLifetimeSeconds: 7200,
    targetUrls: [
      "https://github.com/noosxe/runnero",
      "https://github.com/noosxe/frontend",
      "https://github.com/noosxe/docs",
    ],
  },
  {
    id: 2n,
    name: "gitea-org-pool",
    provider: "gitea",
    repositoryUrl: "https://gitea.example.com/devops",
    scope: "org",
    minIdleRunners: 1,
    maxConcurrency: 5,
    activeRunners: 0,
    cpuLimit: "2",
    memoryLimit: "4G",
    allowDocker: false,
    runnerImage: "",
    maxRunnerLifetimeSeconds: 3600,
  },
];

let mockIsLoading = false;
let mockIsConnected = true;
let mockPoolsData: any = mockPools;
let mockAuthProfilesData: any = [{ id: 1n, name: "prod-profile" }];
let mockSessionData: any = { username: "admin", isAdmin: true, hostArch: "amd64", hostOs: "linux" };

vi.mock("../lib/api/query-hooks", () => ({
  usePools: () => ({
    data: mockIsLoading ? undefined : mockPoolsData,
    isLoading: mockIsLoading,
  }),
  useAuthProfiles: () => ({
    data: mockAuthProfilesData,
    isLoading: false,
  }),
  useSession: () => ({
    data: mockSessionData,
    isLoading: false,
  }),
  useCreatePool: () => ({
    mutateAsync: vi.fn().mockResolvedValue({ pool: { id: 999n } }),
    isPending: false,
  }),
  useUpdatePool: () => ({
    mutateAsync: vi.fn().mockResolvedValue({ pool: { id: 999n } }),
    isPending: false,
  }),
  useDiscoverTargets: () => ({
    data: [
      {
        name: "runnero",
        fullName: "noosxe/runnero",
        htmlUrl: "https://github.com/noosxe/runnero",
        description: "Lightweight runner",
        isPrivate: false,
        avatarUrl: "",
      },
    ],
    isLoading: false,
    error: null,
    refetch: vi.fn(),
  }),
}));

vi.mock("../lib/api/streaming-hooks", () => ({
  useWatchPools: () => ({
    isConnected: mockIsConnected,
  }),
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, ...props }: any) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}));

describe("PoolsPage", () => {
  beforeEach(() => {
    mockIsLoading = false;
    mockIsConnected = true;
    mockPoolsData = mockPools;
    mockAuthProfilesData = [{ id: 1n, name: "prod-profile" }];
    vi.clearAllMocks();
  });

  it("renders pool cards with capacity utilization and quotas", () => {
    render(<PoolsPage />);

    expect(screen.getByText("Runner Pools")).toBeInTheDocument();
    expect(screen.getByText("Live Stream")).toBeInTheDocument();
    expect(screen.getByText("arm64-prod-pool")).toBeInTheDocument();
    expect(screen.getByText("gitea-org-pool")).toBeInTheDocument();
    expect(screen.getByText("3 / 10 (30%)")).toBeInTheDocument();
    expect(screen.getByText("4 CPU")).toBeInTheDocument();
    expect(screen.getByText("8G Mem")).toBeInTheDocument();
    expect(screen.getByText("Docker Enabled")).toBeInTheDocument();
  });

  it("filters pools by search term", () => {
    render(<PoolsPage />);

    const searchInput = screen.getByPlaceholderText("Search pools by name or target URL...");
    fireEvent.change(searchInput, { target: { value: "gitea" } });

    expect(screen.queryByText("arm64-prod-pool")).not.toBeInTheDocument();
    expect(screen.getByText("gitea-org-pool")).toBeInTheDocument();
  });

  it("filters pools by provider", () => {
    render(<PoolsPage />);

    const select = screen.getAllByRole("combobox")[0];
    fireEvent.change(select, { target: { value: "github" } });

    expect(screen.getByText("arm64-prod-pool")).toBeInTheDocument();
    expect(screen.queryByText("gitea-org-pool")).not.toBeInTheDocument();
  });

  it("displays warning banner when no auth profiles are configured", () => {
    mockAuthProfilesData = [];

    render(<PoolsPage />);

    expect(screen.getByText("Git Authentication Profile Required")).toBeInTheDocument();
    expect(screen.getByText(/Runner pools require upstream credentials/i)).toBeInTheDocument();
    expect(screen.getByText(/Configure Profile →/i)).toBeInTheDocument();
  });

  it("displays guided empty state when zero pools and no auth profiles exist", () => {
    mockPoolsData = [];
    mockAuthProfilesData = [];

    render(<PoolsPage />);

    expect(screen.getByText("No runner pools configured")).toBeInTheDocument();
    expect(
      screen.getByText(/No Git authentication profiles are configured yet/i),
    ).toBeInTheDocument();
    expect(screen.getByText("Configure Git Profile")).toBeInTheDocument();
  });

  it("displays add pool CTA when zero pools and auth profiles exist", () => {
    mockPoolsData = [];
    mockAuthProfilesData = [{ id: 1n, name: "default" }];

    render(<PoolsPage />);

    expect(screen.getByText("No runner pools configured")).toBeInTheDocument();
    expect(screen.getByText(/Git authentication profile is ready/i)).toBeInTheDocument();
    expect(screen.getAllByText("+ Add Runner Pool").length).toBeGreaterThan(0);
  });

  it("displays multi-target count badge when pool has multiple targets", () => {
    mockPoolsData = mockPools;
    render(<PoolsPage />);

    expect(screen.getByText("3 repos")).toBeInTheDocument();
  });

  it("shows no count badge for single-target pools", () => {
    mockPoolsData = mockPools;
    render(<PoolsPage />);

    // gitea-org-pool has one target (repositoryUrl fallback) and no badge
    expect(screen.queryByText("1 repos")).not.toBeInTheDocument();
    expect(screen.getByText("https://gitea.example.com/devops")).toBeInTheDocument();
  });

  it("filters pools by a secondary target URL", () => {
    mockPoolsData = mockPools;
    render(<PoolsPage />);

    const searchInput = screen.getByPlaceholderText("Search pools by name or target URL...");
    fireEvent.change(searchInput, { target: { value: "noosxe/docs" } });

    expect(screen.getByText("arm64-prod-pool")).toBeInTheDocument();
    expect(screen.queryByText("gitea-org-pool")).not.toBeInTheDocument();
  });

  it("suggests amd64 runner labels when supervisor hostArch is amd64", () => {
    mockPoolsData = mockPools;
    mockAuthProfilesData = [{ id: 1n, name: "prod-profile", authMethod: "github-pat" }];
    mockSessionData = { username: "admin", isAdmin: true, hostArch: "amd64", hostOs: "linux" };

    render(<PoolsPage />);

    const addButtons = screen.getAllByText("+ Add Runner Pool");
    fireEvent.click(addButtons[0]);

    // Step 1: Identity
    const poolNameInput = screen.getByLabelText(/Pool Name/i);
    fireEvent.change(poolNameInput, { target: { value: "my-ci-pool" } });
    fireEvent.click(screen.getByText("Continue to Scope & Targets"));

    // Step 2: Targets
    fireEvent.click(screen.getByText("Select All Filtered"));
    fireEvent.click(screen.getByText("Continue to Specifications"));

    // Step 3: Specs
    const labelsInput = screen.getByLabelText("Runner Labels") as HTMLInputElement;
    expect(labelsInput.value).toBe("self-hosted,linux,amd64");
  });
});
