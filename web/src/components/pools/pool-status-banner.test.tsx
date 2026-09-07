import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { PoolStatusBanner } from "./pool-status-banner";
import { PoolHealthStatus, PoolSchema } from "../../gen/api_pb";

describe("PoolStatusBanner", () => {
  it("renders intent and healthy state", () => {
    const pool = create(PoolSchema, {
      id: 1n,
      name: "test-pool",
      healthStatus: PoolHealthStatus.HEALTHY,
      currentIntent: "Monitoring queue for incoming workflow jobs",
      lastReconciledAt: new Date().toISOString(),
    });

    render(<PoolStatusBanner pool={pool} />);
    expect(screen.getByText("Operational State")).toBeInTheDocument();
    expect(screen.getByText("Healthy")).toBeInTheDocument();
    expect(screen.getByText("Monitoring queue for incoming workflow jobs")).toBeInTheDocument();
    expect(screen.getByText(/Reconciled/)).toBeInTheDocument();
  });

  it("renders default degraded intent when none provided", () => {
    const pool = create(PoolSchema, {
      id: 2n,
      name: "degraded-pool",
      healthStatus: PoolHealthStatus.DEGRADED,
    });

    render(<PoolStatusBanner pool={pool} />);
    expect(screen.getByText("Degraded")).toBeInTheDocument();
    expect(screen.getByText(/Reconciliation encountered errors/)).toBeInTheDocument();
  });
});
