import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { PoolDiagnosticsCard } from "./pool-diagnostics-card";
import { PoolHealthStatus, PoolSchema } from "../../gen/api_pb";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, ...props }: any) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}));

describe("PoolDiagnosticsCard", () => {
  it("renders null when pool is Healthy and has no lastError", () => {
    const pool = create(PoolSchema, {
      id: 1n,
      name: "healthy-pool",
      healthStatus: PoolHealthStatus.HEALTHY,
    });

    const { container } = render(<PoolDiagnosticsCard pool={pool} />);
    expect(container.firstChild).toBeNull();
  });

  it("renders alert when pool is Degraded with AUTH_DECRYPTION_FAILED", () => {
    const pool = create(PoolSchema, {
      id: 1n,
      name: "degraded-pool",
      healthStatus: PoolHealthStatus.DEGRADED,
      lastErrorCode: "AUTH_DECRYPTION_FAILED",
      lastError: "decryption failed: wrong key or corrupted ciphertext",
      lastErrorTimestamp: "2026-09-07T12:00:00Z",
    });

    render(<PoolDiagnosticsCard pool={pool} />);
    expect(screen.getByText("Reconciliation Failure Alert")).toBeInTheDocument();
    expect(screen.getByText("AUTH_DECRYPTION_FAILED")).toBeInTheDocument();
    expect(
      screen.getByText("decryption failed: wrong key or corrupted ciphertext"),
    ).toBeInTheDocument();
    expect(screen.getByText("Fix in Git Auth Profiles")).toBeInTheDocument();
    expect(
      screen.getByText(/supervisor master encryption key could not decrypt/),
    ).toBeInTheDocument();
  });

  it("renders remediation for GLOBAL_QUOTA_SATURATED", () => {
    const pool = create(PoolSchema, {
      id: 2n,
      name: "saturated-pool",
      healthStatus: PoolHealthStatus.DEGRADED,
      lastErrorCode: "GLOBAL_QUOTA_SATURATED",
      lastError: "global maximum runner limit reached: 20/20 active",
    });

    render(<PoolDiagnosticsCard pool={pool} />);
    expect(screen.getByText("GLOBAL_QUOTA_SATURATED")).toBeInTheDocument();
    expect(screen.getByText("Adjust Global Quotas")).toBeInTheDocument();
  });
});
