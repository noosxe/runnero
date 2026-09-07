import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { SettingsPage } from "./settings";

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

vi.mock("../lib/api/query-hooks", () => ({
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
    render(<SettingsPage />);

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

  it("switches to runner image updates tab and lists pending notifications and pools", () => {
    render(<SettingsPage />);

    const imagesTab = screen.getByRole("button", { name: /runner image updates/i });
    fireEvent.click(imagesTab);

    expect(screen.getByText("Runner Image Update Management")).toBeInTheDocument();
    expect(screen.getByText("Pending Image Notifications")).toBeInTheDocument();
    expect(screen.getAllByText("pool-arm64-prod").length).toBeGreaterThan(0);
  });
});
