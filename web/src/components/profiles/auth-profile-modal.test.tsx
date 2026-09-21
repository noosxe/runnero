import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { AuthProfileModal } from "./auth-profile-modal";

// Query hooks are irrelevant for the method-toggle gating assertions; the
// modal only calls them on submit.
vi.mock("../../lib/api/query-hooks", () => ({
  useCreateAuthProfile: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useUpdateAuthProfile: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("../../lib/api/transport", () => ({}));

// v1.0.0 gating (RUN-289): Gitea/Forgejo PAT methods stay visible in the
// auth-profile method picker but cannot be selected.
describe("AuthProfileModal method gating (RUN-289)", () => {
  it("renders Gitea/Forgejo methods disabled while GitHub methods stay selectable", () => {
    render(<AuthProfileModal mode="create" onClose={vi.fn()} />);

    const gitea = screen.getByRole("button", { name: "Gitea PAT" });
    const forgejo = screen.getByRole("button", { name: "Forgejo PAT" });
    expect(gitea).toBeDisabled();
    expect(gitea).toHaveAttribute("title", expect.stringContaining("Disabled for v1.0.0"));
    expect(forgejo).toBeDisabled();
    expect(forgejo).toHaveAttribute("title", expect.stringContaining("Disabled for v1.0.0"));

    expect(screen.getByRole("button", { name: "GitHub PAT" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "GitHub App" })).toBeEnabled();
  });
});
