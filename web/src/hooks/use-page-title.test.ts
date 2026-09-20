import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { usePageTitle } from "./use-page-title";

describe("usePageTitle", () => {
  it("sets the document title with the Runnero suffix", () => {
    document.title = "stale";
    renderHook(() => usePageTitle("Dashboard"));
    expect(document.title).toBe("Dashboard · Runnero");
  });

  it("composes detail titles passed by the caller", () => {
    document.title = "stale";
    renderHook(() => usePageTitle("my-pool · Pools"));
    expect(document.title).toBe("my-pool · Pools · Runnero");
  });

  it("leaves the title alone while the entity is loading", () => {
    document.title = "Job History · Runnero";
    renderHook(() => usePageTitle(undefined));
    expect(document.title).toBe("Job History · Runnero");
  });

  it("updates when the title changes", () => {
    document.title = "stale";
    const { rerender } = renderHook(({ t }: { t: string | undefined }) => usePageTitle(t), {
      initialProps: { t: "Logs" },
    });
    rerender({ t: "Settings" });
    expect(document.title).toBe("Settings · Runnero");
  });
});
