import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterMock } from "@/test/router-mock";
import { SettingsPage } from "./settings";

const mockNavigate = vi.fn();

vi.mock("@tanstack/react-router", () => createRouterMock({ useNavigate: () => mockNavigate }));

beforeEach(() => mockNavigate.mockClear());

const mockSettings = [
  { key: "total_allowed_runners", value: "25", updatedAt: "2026-09-04T00:00:00Z" },
  { key: "total_idle_warm_pool", value: "6", updatedAt: "2026-09-04T00:00:00Z" },
  { key: "graceful_shutdown_timeout", value: "400", updatedAt: "2026-09-04T00:00:00Z" },
  { key: "job_retention_days", value: "45", updatedAt: "2026-09-04T00:00:00Z" },
];

const mockPools = [
  {
    id: 1n,
    name: "pool-arm64-prod",
    provider: "github",
    runnerImage: "ghcr.io/noosxe/runnero:v1.1.0",
  },
];

const mockUpdates = [
  {
    id: 101n,
    poolId: 1n,
    currentImage: "ghcr.io/noosxe/runnero:v1.1.0",
    latestDigest: "ghcr.io/noosxe/runnero:v1.2.0",
    status: "available",
  },
];

const mockSetMutate = vi.fn().mockResolvedValue({});
const mockCheckMutate = vi.fn().mockResolvedValue({});

let mockIsAdmin = true;

vi.mock("../lib/api/query-hooks", () => ({
  useIsAdmin: () => mockIsAdmin,
  useSession: () => ({ data: { username: "admin", role: mockIsAdmin ? "admin" : "viewer" } }),
  useUsers: () => ({
    data: [{ username: "admin", role: "admin", createdAt: undefined }],
    isLoading: false,
  }),
  useCreateUser: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useSetUserRole: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useSetUserPassword: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useDeleteUser: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useAppSettings: () => ({
    data: mockSettings,
    isLoading: false,
  }),
  useSetAppSetting: () => ({
    mutateAsync: mockSetMutate,
  }),
  usePools: () => ({
    data: mockPools,
    isLoading: false,
  }),
  useImageUpdates: () => ({
    data: mockUpdates,
    isLoading: false,
  }),
  useCheckImageUpdate: () => ({
    mutateAsync: mockCheckMutate,
    mutate: mockCheckMutate,
  }),
  usePullImage: () => ({
    mutateAsync: vi.fn(),
  }),
  useDismissImageUpdate: () => ({
    mutateAsync: vi.fn(),
  }),
}));

describe("SettingsPage", () => {
  it("renders global constraints form and allows modifying retention days", async () => {
    render(<SettingsPage tab="constraints" />);

    expect(screen.getByText("Supervisor Settings & Administration")).toBeInTheDocument();
    expect(screen.getByText("Global Runner Quota")).toBeInTheDocument();

    const retentionInput = screen.getByLabelText(/history retention period/i) as HTMLInputElement;
    expect(retentionInput.value).toBe("45");

    fireEvent.change(retentionInput, { target: { value: "60" } });
    expect(retentionInput.value).toBe("60");

    const saveBtn = screen.getByRole("button", { name: /save changes/i });
    fireEvent.click(saveBtn);

    await waitFor(() => {
      expect(mockSetMutate).toHaveBeenCalledWith(
        expect.objectContaining({
          key: "job_retention_days",
          value: "60",
        }),
      );
    });
  });

  it("blocks save when a constraint leaves its class C range (RUN-222)", async () => {
    render(<SettingsPage tab="constraints" />);

    const timeoutInput = screen.getByLabelText(/graceful drain timeout/i) as HTMLInputElement;
    await waitFor(() => expect(timeoutInput.value).toBe("400"));

    // Out of bounds: the save gate blocks and the violation lands inline.
    fireEvent.change(timeoutInput, { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    expect(mockSetMutate).not.toHaveBeenCalled();
    const inlineError = document.querySelector("#graceful_shutdown_timeout-error");
    expect(inlineError).not.toBeNull();
    expect(inlineError).toHaveTextContent(/between 30 and 3600/i);

    // Fixing the value clears the gate and the save goes through.
    fireEvent.change(timeoutInput, { target: { value: "600" } });
    fireEvent.blur(timeoutInput);
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => {
      expect(mockSetMutate).toHaveBeenCalledWith(
        expect.objectContaining({ key: "graceful_shutdown_timeout", value: "600" }),
      );
    });
  });

  it("links the runner image updates tab as a path segment (RUN-283)", () => {
    const { rerender } = render(<SettingsPage tab="constraints" />);

    const imagesTab = screen.getByRole("tab", { name: /runner image updates/i });
    expect(imagesTab.getAttribute("href")).toBe("/settings/images");

    // The router owns the tab: the route hands the component a fixed tab.
    rerender(<SettingsPage tab="images" />);
    expect(screen.getByText("Runner Image Update Management")).toBeInTheDocument();
    expect(screen.getByText("Pending Image Notifications")).toBeInTheDocument();
    expect(screen.getAllByText("pool-arm64-prod").length).toBeGreaterThan(0);
  });

  it("deep-links straight to the users tab for an admin (RUN-283)", () => {
    render(<SettingsPage tab="users" />);
    expect(screen.getByTestId("users-card")).toBeInTheDocument();
  });
});

// Role gating (RUN-236, docs/35 section 2.4, amended by RUN-282): a
// viewer's settings page is the Instance tab only — personal account
// surfaces live on /account; admins additionally see the admin tabs.
describe("SettingsPage role gating", () => {
  it("shows only the instance tab for a viewer", () => {
    mockIsAdmin = false;
    render(<SettingsPage tab="instance" />);

    expect(screen.getByTestId("instance-card")).toBeInTheDocument();
    expect(screen.queryByText("Global Constraints")).not.toBeInTheDocument();
    expect(screen.queryByText("Users")).not.toBeInTheDocument();
    mockIsAdmin = true;
  });

  // Route-level gating (RUN-283): the route layer redirects admin-only
  // deep links for viewers (covered by the E2E viewer flow), so the
  // component only ever receives a visible tab. This suite pins that an
  // admin deep link renders the users card.
  it("shows the users tab for an admin (deep link)", async () => {
    mockIsAdmin = true;
    render(<SettingsPage tab="users" />);
    await waitFor(() => expect(screen.getByTestId("users-card")).toBeInTheDocument());
  });
});
