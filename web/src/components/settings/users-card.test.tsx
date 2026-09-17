import { describe, it, expect, vi, beforeEach } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { UsersCard } from "./users-card";
import { Toaster } from "@/components/ui/toast";
import { UserInfoSchema, type UserInfo } from "../../gen/api_pb";

const baseUser = (overrides: Partial<UserInfo> = {}): UserInfo =>
  create(UserInfoSchema, {
    username: "admin",
    role: "admin",
    createdAt: timestampFromDate(new Date("2026-01-01T00:00:00Z")),
    ...overrides,
  });

let mockUsers: UserInfo[] = [];
let mockSession: { username: string; role: string } | null = { username: "admin", role: "admin" };
const mockCreateAsync = vi.fn();
const mockSetRoleAsync = vi.fn();
const mockSetPasswordAsync = vi.fn();
const mockDeleteAsync = vi.fn();

vi.mock("../../lib/api/query-hooks", () => ({
  useUsers: () => ({ data: mockUsers, isLoading: false }),
  useSession: () => ({
    data: mockSession ? { username: mockSession.username, role: mockSession.role } : null,
  }),
  useCreateUser: () => ({ mutateAsync: mockCreateAsync, isPending: false }),
  useSetUserRole: () => ({ mutateAsync: mockSetRoleAsync, isPending: false }),
  useSetUserPassword: () => ({ mutateAsync: mockSetPasswordAsync, isPending: false }),
  useDeleteUser: () => ({ mutateAsync: mockDeleteAsync, isPending: false }),
}));

describe("UsersCard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUsers = [baseUser(), baseUser({ username: "observer", role: "viewer" })];
  });

  it("lists users with role chips and a you badge on the caller", () => {
    render(<UsersCard />);

    expect(screen.getByText("admin")).toBeInTheDocument();
    expect(screen.getByText("observer")).toBeInTheDocument();
    expect(screen.getByText("Admin")).toBeInTheDocument();
    expect(screen.getByText("Viewer")).toBeInTheDocument();
    // The caller's row carries the "you" badge and a disabled delete.
    expect(screen.getByText("you")).toBeInTheDocument();
    expect(screen.getByLabelText("Delete admin")).toBeDisabled();
    expect(screen.getByLabelText("Delete observer")).toBeEnabled();
  });

  it("creates a user through the add dialog", async () => {
    mockCreateAsync.mockResolvedValueOnce({ user: baseUser() });
    render(<UsersCard />);

    fireEvent.click(screen.getByTestId("add-user-button"));
    fireEvent.change(screen.getByLabelText("Username"), { target: { value: "newbie" } });
    fireEvent.change(screen.getByLabelText("Initial password"), {
      target: { value: "long-enough-password" },
    });
    fireEvent.change(screen.getByLabelText("Confirm password"), {
      target: { value: "long-enough-password" },
    });
    fireEvent.click(screen.getByTestId("add-user-submit"));

    await waitFor(() => expect(mockCreateAsync).toHaveBeenCalled());
    expect(mockCreateAsync).toHaveBeenCalledWith({
      username: "newbie",
      password: "long-enough-password",
      role: "viewer",
    });
  });

  it("rejects mismatched dialog passwords client-side", async () => {
    render(<UsersCard />);

    fireEvent.click(screen.getByTestId("add-user-button"));
    fireEvent.change(screen.getByLabelText("Username"), { target: { value: "newbie" } });
    fireEvent.change(screen.getByLabelText("Initial password"), {
      target: { value: "long-enough-password" },
    });
    fireEvent.change(screen.getByLabelText("Confirm password"), {
      target: { value: "different-password" },
    });
    fireEvent.click(screen.getByTestId("add-user-submit"));

    await waitFor(() =>
      expect(screen.getByTestId("add-user-error")).toHaveTextContent(/do not match/i),
    );
    expect(mockCreateAsync).not.toHaveBeenCalled();
  });

  it("confirms before a role change and calls SetUserRole", async () => {
    render(<UsersCard />);

    fireEvent.click(screen.getByLabelText("Change role for observer"));
    fireEvent.click(screen.getByTestId("confirm-role-change"));

    await waitFor(() => expect(mockSetRoleAsync).toHaveBeenCalled());
    expect(mockSetRoleAsync).toHaveBeenCalledWith({
      username: "observer",
      role: "admin",
    });
  });

  it("surfaces a last-admin refusal from the server as a toast", async () => {
    mockSetRoleAsync.mockRejectedValueOnce(new Error("cannot remove the last admin"));
    // The toast manager is global state; the Toaster renders it.
    render(
      <>
        <UsersCard />
        <Toaster />
      </>,
    );

    fireEvent.click(screen.getByLabelText("Change role for observer"));
    fireEvent.click(screen.getByTestId("confirm-role-change"));

    await waitFor(() => expect(screen.getByText(/Cannot change role/i)).toBeInTheDocument());
  });

  it("resets a user password through the confirm dialog", async () => {
    mockSetPasswordAsync.mockResolvedValueOnce({ success: true, revokedSessions: 2n });
    render(<UsersCard />);

    fireEvent.click(screen.getByLabelText("Reset password for observer"));
    fireEvent.change(screen.getByLabelText("New password"), {
      target: { value: "brand-new-password" },
    });
    fireEvent.change(screen.getByLabelText("Confirm password"), {
      target: { value: "brand-new-password" },
    });
    fireEvent.click(screen.getByTestId("reset-password-submit"));

    await waitFor(() => expect(mockSetPasswordAsync).toHaveBeenCalled());
    expect(mockSetPasswordAsync).toHaveBeenCalledWith({
      username: "observer",
      password: "brand-new-password",
    });
  });

  it("deletes a user after confirmation", async () => {
    mockDeleteAsync.mockResolvedValueOnce({ success: true });
    render(<UsersCard />);

    fireEvent.click(screen.getByLabelText("Delete observer"));
    fireEvent.click(screen.getByTestId("confirm-delete-user"));

    await waitFor(() => expect(mockDeleteAsync).toHaveBeenCalled());
    expect(mockDeleteAsync).toHaveBeenCalledWith({ username: "observer" });
  });
});
