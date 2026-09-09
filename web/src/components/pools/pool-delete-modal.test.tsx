import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { PoolDeleteModal } from "./pool-delete-modal";

const mockMutateAsync = vi.fn();
const mockNavigate = vi.fn();

vi.mock("../../lib/api/query-hooks", () => ({
  useDeletePool: () => ({
    mutateAsync: mockMutateAsync,
    isPending: false,
  }),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
}));

function renderModal(overrides?: { busyCount?: number; idleCount?: number; maxLifetime?: number }) {
  return render(
    <PoolDeleteModal
      isOpen
      onClose={vi.fn()}
      poolId={42n}
      poolName="kraken-runners"
      busyCount={overrides?.busyCount ?? 0}
      idleCount={overrides?.idleCount ?? 2}
      maxRunnerLifetimeSeconds={overrides?.maxLifetime}
    />,
  );
}

function clickConfirm() {
  // The confirm button reads "Delete & Drain" or "Delete & Terminate" — grab
  // whichever is rendered.
  const btn =
    screen.queryByRole("button", { name: /delete & drain/i }) ??
    screen.getByRole("button", { name: /delete & terminate/i });
  fireEvent.click(btn);
}

describe("PoolDeleteModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockMutateAsync.mockResolvedValue({ success: true });
  });

  it("preselects graceful drain when busy runners exist (docs/25 §8.2)", () => {
    renderModal({ busyCount: 2 });
    expect(screen.getByRole("radio", { name: /drain/i })).toBeChecked();
    expect(screen.getByRole("radio", { name: /terminate everything/i })).not.toBeChecked();
  });

  it("preselects terminate when no busy runners exist (docs/25 §8.2)", () => {
    renderModal({ busyCount: 0, idleCount: 3 });
    expect(screen.getByRole("radio", { name: /terminate everything/i })).toBeChecked();
    expect(screen.getByRole("radio", { name: /drain/i })).not.toBeChecked();
  });

  it("submits drain_graceful=true with the pool id in drain mode", async () => {
    renderModal({ busyCount: 1 });
    clickConfirm();

    await waitFor(() => {
      expect(mockMutateAsync).toHaveBeenCalledWith({ id: 42n, drainGraceful: true });
    });
    expect(mockNavigate).toHaveBeenCalledWith({ to: "/pools" });
  });

  it("submits drain_graceful=false after switching to terminate mode", async () => {
    renderModal({ busyCount: 1 });
    fireEvent.click(screen.getByRole("radio", { name: /terminate everything/i }));
    clickConfirm();

    await waitFor(() => {
      expect(mockMutateAsync).toHaveBeenCalledWith({ id: 42n, drainGraceful: false });
    });
    expect(mockNavigate).toHaveBeenCalledWith({ to: "/pools" });
  });

  it("shows the idle/busy runner split in the copy", () => {
    renderModal({ busyCount: 2, idleCount: 5 });
    expect(screen.getAllByText(/5 idle/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/2 busy/).length).toBeGreaterThan(0);
  });

  it("mentions the pool's own lifetime in the drain copy when set", () => {
    renderModal({ busyCount: 1, maxLifetime: 7200 });
    expect(screen.getByText(/force-terminated after 7200s/)).toBeInTheDocument();
  });

  it("falls back to the default backstop copy when no lifetime is set", () => {
    renderModal({ busyCount: 1, maxLifetime: 0 });
    expect(screen.getByText(/6h \(default backstop\)/)).toBeInTheDocument();
  });

  it("shows an error and stays open when the mutation fails", async () => {
    mockMutateAsync.mockRejectedValueOnce(new Error("boom"));
    renderModal({ busyCount: 0 });
    clickConfirm();

    await waitFor(() => {
      expect(screen.getByText(/boom/)).toBeInTheDocument();
    });
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});
