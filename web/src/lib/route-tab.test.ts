import { describe, expect, it } from "vitest";
import { resolveRouteTab } from "./route-tab";

describe("resolveRouteTab", () => {
  const TABS = ["runners", "config", "renovate"] as const;

  it("returns the raw value when it is in the allowed set", () => {
    expect(resolveRouteTab("config", TABS, "runners")).toBe("config");
  });

  it("falls back when the param is absent", () => {
    expect(resolveRouteTab(undefined, TABS, "runners")).toBe("runners");
  });

  it("falls back on unknown values instead of crashing", () => {
    expect(resolveRouteTab("javascript:alert(1)", TABS, "runners")).toBe("runners");
    expect(resolveRouteTab("", TABS, "runners")).toBe("runners");
    expect(resolveRouteTab("Runners", TABS, "runners")).toBe("runners");
  });

  it("clamps role-forbidden values via a role-scoped allowed list", () => {
    const viewerTabs = ["security", "instance"] as const;
    expect(resolveRouteTab("users", viewerTabs, "security")).toBe("security");
    expect(resolveRouteTab("instance", viewerTabs, "security")).toBe("instance");
  });
});
