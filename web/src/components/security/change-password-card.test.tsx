import { describe, it, expect, vi, beforeEach } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ChangePasswordCard } from "./change-password-card";
import { ViolationsSchema } from "../../gen/buf/validate/validate_pb";

const mockMutateAsync = vi.fn();

vi.mock("../../lib/api/query-hooks", () => ({
  useChangePassword: () => ({
    mutateAsync: mockMutateAsync,
    isPending: false,
  }),
}));

/** Builds the Connect error the server sends for a wrong current password. */
function mismatchError() {
  return new ConnectError("current password is incorrect", Code.InvalidArgument, undefined, [
    {
      desc: ViolationsSchema,
      value: {
        violations: [
          {
            ruleId: "auth.password.current_mismatch",
            message: "current password is incorrect",
            field: { elements: [{ fieldName: "current_password" }] },
          },
        ],
      },
    },
  ]);
}

function fillForm(current: string, next: string, confirm: string) {
  fireEvent.change(screen.getByLabelText("Current password"), {
    target: { value: current },
  });
  fireEvent.change(screen.getByLabelText("New password"), {
    target: { value: next },
  });
  fireEvent.change(screen.getByLabelText("Confirm new password"), {
    target: { value: confirm },
  });
}

describe("ChangePasswordCard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders the three password fields and submit button", () => {
    render(<ChangePasswordCard />);

    expect(screen.getByLabelText("Current password")).toBeInTheDocument();
    expect(screen.getByLabelText("New password")).toBeInTheDocument();
    expect(screen.getByLabelText("Confirm new password")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /change password/i })).toBeInTheDocument();
  });

  it("blocks submit client-side when confirm does not match", async () => {
    render(<ChangePasswordCard />);

    fillForm("old-password-abc", "long-enough-password-1", "long-enough-password-2");
    fireEvent.click(screen.getByRole("button", { name: /change password/i }));

    expect(await screen.findByText("Passwords do not match")).toBeInTheDocument();
    expect(mockMutateAsync).not.toHaveBeenCalled();
  });

  it("surfaces the 12-character floor from the wire schema", async () => {
    render(<ChangePasswordCard />);

    fillForm("old-password-abc", "only11char", "only11char");
    fireEvent.click(screen.getByRole("button", { name: /change password/i }));

    // protovalidate-es previews the min_len=12 rule from proto/api.proto.
    const alert = await screen.findAllByRole("alert");
    const messages = alert.map((el) => el.textContent).join(" ");
    expect(messages).toMatch(/12/i);
    expect(mockMutateAsync).not.toHaveBeenCalled();
  });

  it("submits the wire values and reports revoked sessions on success", async () => {
    mockMutateAsync.mockResolvedValue({ success: true, revokedSessions: 2n });
    render(<ChangePasswordCard />);

    fillForm("old-password-abc", "long-enough-password-1", "long-enough-password-1");
    fireEvent.click(screen.getByRole("button", { name: /change password/i }));

    await waitFor(() =>
      expect(mockMutateAsync).toHaveBeenCalledWith({
        currentPassword: "old-password-abc",
        newPassword: "long-enough-password-1",
      }),
    );
    expect(await screen.findByRole("status")).toHaveTextContent(
      /2 other sessions were signed out/i,
    );
  });

  it("maps the wrong-current-password violation back to its field", async () => {
    mockMutateAsync.mockRejectedValue(mismatchError());
    render(<ChangePasswordCard />);

    fillForm("totally-wrong-guess", "long-enough-password-1", "long-enough-password-1");
    fireEvent.click(screen.getByRole("button", { name: /change password/i }));

    const alert = await screen.findAllByRole("alert");
    const messages = alert.map((el) => el.textContent).join(" ");
    expect(messages).toContain("current password is incorrect");
    const currentInput = screen.getByLabelText("Current password");
    expect(currentInput).toHaveAttribute("aria-invalid", "true");
  });
});
