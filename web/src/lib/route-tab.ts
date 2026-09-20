/**
 * Clamp a raw route search-param tab value to the allowed set (RUN-257,
 * RUN-258): tabbed routes keep their tab in the URL (`?tab=…`) so browser
 * back/forward and deep links work, mirroring the /logs pattern (docs/29:
 * "tabs are URL-driven"). A missing, unknown, or role-forbidden value falls back to
 * the route default instead of crashing or rendering a hidden surface.
 *
 * Pure function so the clamping rules are unit-testable without a router.
 */
export function resolveRouteTab<T extends string>(
  raw: string | undefined,
  allowed: readonly T[],
  fallback: T,
): T {
  return raw !== undefined && (allowed as readonly string[]).includes(raw) ? (raw as T) : fallback;
}
