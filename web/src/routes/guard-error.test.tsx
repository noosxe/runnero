import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { GuardErrorPage } from "./guard-error";

const invalidate = vi.fn().mockResolvedValue(undefined);
const removeQueries = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  useRouter: () => ({ invalidate }),
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ removeQueries }),
}));

vi.mock("../lib/api/query-hooks", () => ({
  useIsAdmin: () => true,
  queryKeys: { onboardingStatus: ["onboarding", "status"] as const },
}));

describe("GuardErrorPage (RUN-243 fail-closed guard error state)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders an explicit connection-error state with the RPC detail", () => {
    render(<GuardErrorPage error={new Error("connection lost")} reset={vi.fn()} />);

    expect(screen.getByText("Can’t reach the supervisor")).toBeInTheDocument();
    expect(screen.getByTestId("guard-error-detail")).toHaveTextContent("connection lost");
  });

  it("renders a generic detail for non-Error throws", () => {
    render(<GuardErrorPage error={undefined} reset={vi.fn()} />);

    expect(screen.getByTestId("guard-error-detail")).toHaveTextContent("Unknown connection error");
  });

  it("NEVER renders onboarding wizard content — a failed status check must not look like a fresh install", () => {
    render(<GuardErrorPage error={new Error("connection lost")} reset={vi.fn()} />);

    expect(screen.queryByText(/set up your supervisor/i)).toBeNull();
    expect(document.querySelector("input[type=password]")).toBeNull();
  });

  it("retry clears the cached guard query and re-runs the guards", async () => {
    render(<GuardErrorPage error={new Error("connection lost")} reset={vi.fn()} />);

    fireEvent.click(screen.getByTestId("guard-error-retry"));

    await waitFor(() => {
      expect(removeQueries).toHaveBeenCalledWith({
        queryKey: ["onboarding", "status"],
      });
      expect(invalidate).toHaveBeenCalled();
    });
  });
});
