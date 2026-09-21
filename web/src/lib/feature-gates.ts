/**
 * v1.0.0 UI feature gating (RUN-289).
 *
 * Renovate automation and the Gitea / Forgejo providers have never been
 * validated against real platforms, so for v1.0.0 the UI disables every
 * surface that could configure or trigger them. Per the owner decision the
 * surfaces stay VISIBLE but unclickable/unusable ("disable, don't hide") —
 * cluttering is acceptable, silent disappearance is not.
 *
 * The backend is untouched: existing configurations keep rendering, and
 * flipping a flag here re-enables the surface for a later release without
 * any other change.
 */
export const FEATURES = {
  renovate: false,
  gitea: false,
  forgejo: false,
} as const;

/** Shared user-facing copy for every gated surface. */
export const FEATURE_DISABLED_HINT =
  "Disabled for v1.0.0 — not yet validated against real platforms.";

/** True when the auth-method/provider family is gated off for v1.0.0. */
export function isProviderGated(authMethod: string | undefined): boolean {
  if (!authMethod) return false;
  if (authMethod.startsWith("gitea")) return !FEATURES.gitea;
  if (authMethod.startsWith("forgejo")) return !FEATURES.forgejo;
  return false;
}
