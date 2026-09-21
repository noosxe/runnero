import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { AccountSecurityTab } from "./account-security";

let mockIsAdmin = true;
let mockPasskeyAvailable = false;

vi.mock("../lib/api/query-hooks", () => ({
  useIsAdmin: () => mockIsAdmin,
  useOnboardingStatus: () => ({ data: { passkeyAvailable: mockPasskeyAvailable } }),
  useChangePassword: () => ({ mutateAsync: vi.fn(), isPending: false }),
  usePasskeys: () => ({ data: [], isLoading: false }),
  useEnrollPasskey: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useRenamePasskey: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useDeletePasskey: () => ({ mutate: vi.fn(), isPending: false }),
}));

describe("AccountSecurityTab passkey visibility (RUN-282)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockIsAdmin = true;
    mockPasskeyAvailable = false;
  });

  it("shows the unconfigured Empty card to an admin", () => {
    render(<AccountSecurityTab />);

    expect(screen.getByText("Passkey support is not configured")).toBeInTheDocument();
    expect(screen.getByText(/SUPERVISOR_WEBAUTHN_RP_ID/)).toBeInTheDocument();
  });

  it("shows nothing for a viewer when WebAuthn is not configured", () => {
    mockIsAdmin = false;
    render(<AccountSecurityTab />);

    expect(screen.queryByText("Passkey support is not configured")).not.toBeInTheDocument();
    expect(screen.queryByText("Passkeys")).not.toBeInTheDocument();
  });

  it("shows the passkeys card for a viewer when WebAuthn is configured", () => {
    mockIsAdmin = false;
    mockPasskeyAvailable = true;
    render(<AccountSecurityTab />);

    expect(screen.getByText("Passkeys")).toBeInTheDocument();
    expect(screen.queryByText("Passkey support is not configured")).not.toBeInTheDocument();
  });

  it("shows the passkeys card for an admin when WebAuthn is configured", () => {
    mockPasskeyAvailable = true;
    render(<AccountSecurityTab />);

    expect(screen.getByText("Passkeys")).toBeInTheDocument();
    expect(screen.queryByText("Passkey support is not configured")).not.toBeInTheDocument();
  });

  it("always shows the change-password card", () => {
    render(<AccountSecurityTab />);

    expect(screen.getByText("Change Password")).toBeInTheDocument();
  });
});
