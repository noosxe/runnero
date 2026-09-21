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
    expect(screen.getByTestId("pool-status-banner")).toBeInTheDocument();
    expect(screen.getByText("Operational State")).toBeInTheDocument();
    expect(screen.getByText("Healthy")).toBeInTheDocument();
    expect(screen.getByText("Monitoring queue for incoming workflow jobs")).toBeInTheDocument();
    expect(screen.getByText("Last Reconciled")).toBeInTheDocument();
    expect(screen.getByText("Reconcile Loop")).toBeInTheDocument();
    expect(screen.getByText("~10s cadence")).toBeInTheDocument();
  });

  it("renders provisioning default intent when none provided", () => {
    const pool = create(PoolSchema, {
      id: 3n,
      name: "warming-pool",
      healthStatus: PoolHealthStatus.PROVISIONING,
    });

    render(<PoolStatusBanner pool={pool} />);
    expect(screen.getByText("Provisioning")).toBeInTheDocument();
    expect(screen.getByText(/Spawning warm idle runners/)).toBeInTheDocument();
  });

  it("renders paused default intent when none provided", () => {
    const pool = create(PoolSchema, {
      id: 4n,
      name: "paused-pool",
      healthStatus: PoolHealthStatus.PAUSED,
    });

    render(<PoolStatusBanner pool={pool} />);
    expect(screen.getByText("Paused")).toBeInTheDocument();
    expect(screen.getByText(/Reconciliation is paused/)).toBeInTheDocument();
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
