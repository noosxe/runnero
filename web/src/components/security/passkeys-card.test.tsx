import { describe, it, expect, vi, beforeEach } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { PasskeysCard } from "./passkeys-card";
import { PasskeyInfoSchema, type PasskeyInfo } from "../../gen/api_pb";

const basePasskey = (overrides: Partial<PasskeyInfo> = {}): PasskeyInfo =>
  create(PasskeyInfoSchema, {
    id: 1n,
    name: "YubiKey 5C",
    createdAt: timestampFromDate(new Date("2026-01-05T09:00:00Z")),
    lastUsedAt: timestampFromDate(new Date("2026-01-20T18:30:00Z")),
    backupEligible: false,
    backupState: false,
    cloneWarning: false,
    ...overrides,
  });

let mockPasskeys: PasskeyInfo[] = [];
const mockEnrollAsync = vi.fn();
const mockRenameAsync = vi.fn();
const mockDeleteMutate = vi.fn();

vi.mock("../../lib/api/query-hooks", () => ({
  usePasskeys: () => ({ data: mockPasskeys, isLoading: false }),
  useEnrollPasskey: () => ({ mutateAsync: mockEnrollAsync, isPending: false }),
  useRenamePasskey: () => ({ mutateAsync: mockRenameAsync, isPending: false }),
  useDeletePasskey: () => ({ mutate: mockDeleteMutate, isPending: false }),
}));

describe("PasskeysCard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockPasskeys = [];
  });

  it("shows the empty state before any enrollment", () => {
    render(<PasskeysCard />);

    expect(screen.getByText(/No passkeys yet/i)).toBeInTheDocument();
    expect(screen.getByTestId("add-passkey-button")).toBeInTheDocument();
  });

  it("lists enrolled passkeys with sync signals", () => {
    mockPasskeys = [
      basePasskey(),
      basePasskey({
        id: 2n,
        name: "iPhone",
        backupEligible: true,
        backupState: true,
      }),
    ];
    render(<PasskeysCard />);

    expect(screen.getByText("YubiKey 5C")).toBeInTheDocument();
    expect(screen.getByText("iPhone")).toBeInTheDocument();
    // Device-bound vs synced chips (docs/34 section 3.2 custody vocabulary).
    expect(screen.getByText("Device-bound")).toBeInTheDocument();
    expect(screen.getByText("Synced")).toBeInTheDocument();
  });

  it("raises the clone-warning banner when a credential is flagged", () => {
    mockPasskeys = [basePasskey({ cloneWarning: true })];
    render(<PasskeysCard />);

    expect(screen.getByTestId("clone-warning-banner")).toBeInTheDocument();
    expect(screen.getByTestId("clone-warning-badge")).toBeInTheDocument();
  });

  it("rejects enrollment without the password re-check", async () => {
    render(<PasskeysCard />);

    fireEvent.click(screen.getByTestId("add-passkey-button"));
    fireEvent.click(screen.getByTestId("add-passkey-submit"));

    await waitFor(() => expect(screen.getByTestId("add-passkey-error")).toBeInTheDocument());
    expect(screen.getByTestId("add-passkey-error")).toHaveTextContent(
      /Confirm your current password/,
    );
    expect(mockEnrollAsync).not.toHaveBeenCalled();
  });

  it("enrolls with the current password and an optional label", async () => {
    mockEnrollAsync.mockResolvedValueOnce(basePasskey());
    render(<PasskeysCard />);

    fireEvent.click(screen.getByTestId("add-passkey-button"));
    fireEvent.input(screen.getByLabelText("Current password"), {
      target: { value: "AdminPassword123!" },
    });
    fireEvent.input(screen.getByLabelText("Name (optional)"), {
      target: { value: "Travel key" },
    });
    fireEvent.click(screen.getByTestId("add-passkey-submit"));

    await waitFor(() =>
      expect(mockEnrollAsync).toHaveBeenCalledWith({
        currentPassword: "AdminPassword123!",
        name: "Travel key",
      }),
    );
  });

  it("surfaces enrollment failures (wrong password) inside the dialog", async () => {
    mockEnrollAsync.mockRejectedValueOnce(
      new Error("invalid_argument: current password is incorrect"),
    );
    render(<PasskeysCard />);

    fireEvent.click(screen.getByTestId("add-passkey-button"));
    fireEvent.input(screen.getByLabelText("Current password"), {
      target: { value: "wrong" },
    });
    fireEvent.click(screen.getByTestId("add-passkey-submit"));

    await waitFor(() =>
      expect(screen.getByTestId("add-passkey-error")).toHaveTextContent(
        /current password is incorrect/,
      ),
    );
  });

  it("renames a passkey from its row", async () => {
    mockPasskeys = [basePasskey()];
    mockRenameAsync.mockResolvedValueOnce({});
    render(<PasskeysCard />);

    const row = screen.getByText("YubiKey 5C").closest("tr");
    expect(row).toBeTruthy();
    fireEvent.click(within(row!).getByRole("button", { name: "Rename" }));
    fireEvent.input(screen.getByLabelText("Name"), {
      target: { value: "Backup key" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(mockRenameAsync).toHaveBeenCalledWith({ id: 1n, name: "Backup key" }),
    );
  });

  it("removes a passkey only after confirmation", async () => {
    mockPasskeys = [basePasskey()];
    render(<PasskeysCard />);

    const row = screen.getByText("YubiKey 5C").closest("tr");
    fireEvent.click(within(row!).getByRole("button", { name: "Remove" }));
    expect(mockDeleteMutate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByTestId("confirm-remove-passkey"));
    await waitFor(() => expect(mockDeleteMutate).toHaveBeenCalledWith(1n));
  });
});
