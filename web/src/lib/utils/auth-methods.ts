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

/** toWireAuthMethod maps a UI auth-method id to the proto wire value. */
export function toWireAuthMethod(method: UiAuthMethod | string): WireAuthMethod {
  switch (method) {
    case "github_app":
      return "github_app";
    case "gitea_pat":
      return "gitea_token";
    case "forgejo_pat":
      return "forgejo_token";
    default:
      return "pat";
  }
}
