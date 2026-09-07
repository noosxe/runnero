import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { PoolHealthBadge } from "./pool-health-badge";
import { PoolHealthStatus } from "../../gen/api_pb";

describe("PoolHealthBadge", () => {
  it("renders Healthy status by default", () => {
    render(<PoolHealthBadge />);
    expect(screen.getByText("Healthy")).toBeInTheDocument();
  });

  it("renders Healthy status explicitly", () => {
    render(<PoolHealthBadge status={PoolHealthStatus.HEALTHY} />);
    expect(screen.getByText("Healthy")).toBeInTheDocument();
  });

  it("renders Provisioning status", () => {
    render(<PoolHealthBadge status={PoolHealthStatus.PROVISIONING} />);
    expect(screen.getByText("Provisioning")).toBeInTheDocument();
  });

  it("renders Degraded status", () => {
    render(<PoolHealthBadge status={PoolHealthStatus.DEGRADED} />);
    expect(screen.getByText("Degraded")).toBeInTheDocument();
  });

  it("renders Paused status", () => {
    render(<PoolHealthBadge status={PoolHealthStatus.PAUSED} />);
    expect(screen.getByText("Paused")).toBeInTheDocument();
  });

  it("applies small size styling when size='sm'", () => {
    const { container } = render(<PoolHealthBadge status={PoolHealthStatus.HEALTHY} size="sm" />);
    expect(container.firstChild).toHaveClass("text-[10px]");
  });
});
