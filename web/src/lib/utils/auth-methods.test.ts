import { describe, it, expect } from "vitest";
import { toWireAuthMethod, fromWireAuthMethod } from "./auth-methods";

describe("auth-method mapping", () => {
  it("maps UI ids to wire values", () => {
    expect(toWireAuthMethod("github_pat")).toBe("pat");
    expect(toWireAuthMethod("gitea_pat")).toBe("gitea_token");
    expect(toWireAuthMethod("forgejo_pat")).toBe("forgejo_token");
    expect(toWireAuthMethod("github_app")).toBe("github_app");
  });

  it("is idempotent for already-wire values (edit-mode round-trip safety)", () => {
    expect(toWireAuthMethod("pat")).toBe("pat");
    expect(toWireAuthMethod("gitea_token")).toBe("gitea_token");
    expect(toWireAuthMethod("forgejo_token")).toBe("forgejo_token");
    expect(toWireAuthMethod("github_app")).toBe("github_app");
  });

  it("maps wire values back to UI ids for edit prefill", () => {
    expect(fromWireAuthMethod("pat")).toBe("github_pat");
    expect(fromWireAuthMethod("gitea_token")).toBe("gitea_pat");
    expect(fromWireAuthMethod("forgejo_token")).toBe("forgejo_pat");
    expect(fromWireAuthMethod("github_app")).toBe("github_app");
  });

  it("round-trips wire -> UI -> wire without drift", () => {
    for (const wire of ["pat", "gitea_token", "forgejo_token", "github_app"] as const) {
      expect(toWireAuthMethod(fromWireAuthMethod(wire))).toBe(wire);
    }
  });
});
