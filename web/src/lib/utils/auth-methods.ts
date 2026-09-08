/**
 * Auth-method identifier mapping between UI selector ids and the wire format.
 *
 * The UI exposes provider-labelled PAT methods (`github_pat`, `gitea_pat`,
 * `forgejo_pat`) while `CreateAuthProfile`/`UpdateAuthProfile` accept the
 * proto-documented values `github_app`, `gitea_token`, `forgejo_token`, and
 * `pat` (a GitHub PAT). Map at the mutation boundary only — component state
 * keeps the UI ids.
 */

export type UiAuthMethod = "github_app" | "github_pat" | "gitea_pat" | "forgejo_pat";

export type WireAuthMethod = "github_app" | "pat" | "gitea_token" | "forgejo_token";

/** toWireAuthMethod maps a UI (or already-wire) auth-method id to the proto wire value. Idempotent. */
export function toWireAuthMethod(method: UiAuthMethod | WireAuthMethod | string): WireAuthMethod {
  switch (method) {
    case "github_app":
      return "github_app";
    case "gitea_pat":
    case "gitea_token":
      return "gitea_token";
    case "forgejo_pat":
    case "forgejo_token":
      return "forgejo_token";
    default: // "github_pat", "pat", anything unrecognized
      return "pat";
  }
}

/** fromWireAuthMethod maps a stored wire value back to the UI selector id for edit-mode prefill. */
export function fromWireAuthMethod(method: string): UiAuthMethod {
  switch (method) {
    case "github_app":
      return "github_app";
    case "gitea_token":
      return "gitea_pat";
    case "forgejo_token":
      return "forgejo_pat";
    default: // "pat", "github_pat"
      return "github_pat";
  }
}
