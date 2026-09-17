import { describe, it, expect, vi, beforeEach } from "vitest";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { SecurityTab } from "./security-tab";

const mockSessions = [
  {
    id: 1n,
    deviceLabel: "Firefox 130 on Linux",
    createdAt: timestampFromDate(new Date("2026-09-01T10:00:00Z")),
    lastSeenAt: timestampFromDate(new Date("2026-09-16T09:00:00Z")),
    expiresAt: timestampFromDate(new Date("2026-09-17T10:00:00Z")),
    absoluteExpiresAt: timestampFromDate(new Date("2026-10-01T10:00:00Z")),
    isCurrent: true,
  },
  {
    id: 2n,
    deviceLabel: "curl 8.5.0",
    createdAt: timestampFromDate(new Date("2026-09-10T12:00:00Z")),
    lastSeenAt: timestampFromDate(new Date("2026-09-15T08:00:00Z")),
    expiresAt: timestampFromDate(new Date("2026-09-18T12:00:00Z")),
    absoluteExpiresAt: timestampFromDate(new Date("2026-10-05T12:00:00Z")),
    isCurrent: false,
  },
];

const mockRevokeMutate = vi.fn();
const mockRevokeOthersMutate = vi.fn();
let mockPasskeyAvailable = false;

vi.mock("../../lib/api/query-hooks", () => ({
  useSessions: () => ({
    data: mockSessions,
    isLoading: false,
  }),
  useRevokeSession: () => ({
    mutate: mockRevokeMutate,
    isPending: false,
  }),
  useRevokeOtherSessions: () => ({
    mutate: mockRevokeOthersMutate,
    isPending: false,
  }),
  useChangePassword: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
  useOnboardingStatus: () => ({
    data: { passkeyAvailable: mockPasskeyAvailable },
  }),
  // PasskeysCard is a separate suite (passkeys-card.test.tsx); these stubs
  // only satisfy its imports when it renders here.
  usePasskeys: () => ({ data: [], isLoading: false }),
  useEnrollPasskey: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useRenamePasskey: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useDeletePasskey: () => ({ mutate: vi.fn(), isPending: false }),
}));

describe("SecurityTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockPasskeyAvailable = false;
  });

  it("hides the passkeys card when WebAuthn is not configured", () => {
    render(<SecurityTab />);

    expect(screen.queryByText("Passkeys")).not.toBeInTheDocument();
  });

  it("shows the passkeys card when WebAuthn is configured", () => {
    mockPasskeyAvailable = true;
    render(<SecurityTab />);

    expect(screen.getByText("Passkeys")).toBeInTheDocument();
  });

  it("renders every session with its device label", () => {
    render(<SecurityTab />);

    expect(screen.getByText("Firefox 130 on Linux")).toBeInTheDocument();
    expect(screen.getByText("curl 8.5.0")).toBeInTheDocument();
    expect(screen.getByText("Active Sessions")).toBeInTheDocument();
  });

  it("marks exactly the current session", () => {
    render(<SecurityTab />);

    expect(screen.getByText("Current session")).toBeInTheDocument();
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
  });

  it("revokes a single session from its row", async () => {
    render(<SecurityTab />);

    // Scope to the curl row: it is not the current session.
    const curlRow = screen.getByText("curl 8.5.0").closest("tr");
    expect(curlRow).toBeTruthy();

    fireEvent.click(within(curlRow!).getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(mockRevokeMutate).toHaveBeenCalledWith(2n));
    expect(mockRevokeMutate).not.toHaveBeenCalledWith(1n);
  });

  it("confirms before revoking all other sessions", async () => {
    render(<SecurityTab />);

    fireEvent.click(screen.getByText(/revoke all other sessions \(1\)/i));
    expect(mockRevokeOthersMutate).not.toHaveBeenCalled();

    // Cancel path keeps sessions intact.
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    fireEvent.click(screen.getByText(/revoke all other sessions \(1\)/i));
    fireEvent.click(screen.getByRole("button", { name: "Revoke others" }));
    await waitFor(() => expect(mockRevokeOthersMutate).toHaveBeenCalledOnce());
  });
});
