import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { InstanceCard } from "./instance-card";

// The card reads version/host facts off the authenticated session payload
// (RUN-251); `null` exercises the loading skeleton.
let mockSession: {
  username: string;
  role: string;
  version: string;
  hostOs: string;
  hostArch: string;
} | null;

vi.mock("../../lib/api/query-hooks", () => ({
  useSession: () => ({ data: mockSession ? { ...mockSession } : null }),
}));

describe("InstanceCard", () => {
  beforeEach(() => {
    mockSession = {
      username: "admin",
      role: "admin",
      version: "v0.3.0-379-g2eb9bc2",
      hostOs: "linux",
      hostArch: "amd64",
    };
  });

  it("shows the full version and host facts from the session", () => {
    render(<InstanceCard />);

    expect(screen.getByTestId("instance-version")).toHaveTextContent("v0.3.0-379-g2eb9bc2");
    expect(screen.getByTestId("instance-host-os")).toHaveTextContent("linux");
    expect(screen.getByTestId("instance-host-arch")).toHaveTextContent("amd64");
  });

  it("renders 'unknown' for missing facts instead of blank rows", () => {
    mockSession!.version = "";
    render(<InstanceCard />);
    expect(screen.getByTestId("instance-version")).toHaveTextContent("unknown");
  });

  it("renders the loading skeleton while the session resolves", () => {
    mockSession = null;
    render(<InstanceCard />);
    expect(screen.getByTestId("instance-card-loading")).toBeInTheDocument();
  });
});
