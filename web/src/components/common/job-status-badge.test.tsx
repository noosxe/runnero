import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { JobStatusBadge } from "./job-status-badge";

describe("JobStatusBadge", () => {
  it("renders every lifecycle status from the DB CHECK vocabulary", () => {
    // internal/db/migrations/004_job_history_lifecycle.sql
    for (const status of [
      "queued",
      "running",
      "success",
      "failure",
      "cancelled",
      "timeout",
      "completed",
      "interrupted",
    ]) {
      render(<JobStatusBadge status={status} />);
      expect(screen.getByText(status)).toBeInTheDocument();
    }
  });

  it.each([
    ["success", "text-success"],
    ["failure", "text-destructive"],
    ["timeout", "text-destructive"],
    ["queued", "text-warning"],
    ["running", "text-primary"],
    ["interrupted", "text-notice"],
  ])("colors %s with the semantic token family", (status, tokenClass) => {
    const { container } = render(<JobStatusBadge status={status} />);
    expect(container.firstElementChild?.className).toContain(tokenClass);
  });

  it.each(["completed", "cancelled"])("colors %s neutral gray", (status) => {
    const { container } = render(<JobStatusBadge status={status} />);
    const cls = container.firstElementChild?.className ?? "";
    expect(cls).toContain("bg-muted");
    expect(cls).toContain("text-muted-foreground");
  });

  it("falls back to neutral gray for unknown statuses, showing the raw value", () => {
    const { container } = render(<JobStatusBadge status="on_fire" />);
    const cls = container.firstElementChild?.className ?? "";
    expect(cls).toContain("bg-muted");
    expect(screen.getByText("on_fire")).toBeInTheDocument();
  });
});
