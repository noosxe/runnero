import type { ComponentType, MouseEvent, ReactNode } from "react";
import { vi } from "vitest";

/**
 * Standard mock surface for `@tanstack/react-router` in component tests.
 *
 * Every suite that renders route components should mock the router through
 * this helper instead of hand-rolling a partial object — the inconsistency
 * (e.g. a mock missing an export a component needs) is exactly what produced
 * RUN-183, where `LinkButton` consumers crashed with
 * `No "createLink" export is defined on the mock`.
 *
 * The default surface covers everything the codebase consumes today plus a
 * future-proof identity `createLink` (TanStack's custom-link composition),
 * so a later consumer that goes back to `createLink`-based components keeps
 * rendering without every suite growing its own stub again.
 *
 * Usage in a test file — plain suites:
 *
 * ```tsx
 * import { createRouterMock } from "@/test/router-mock";
 *
 * vi.mock("@tanstack/react-router", () => createRouterMock());
 * ```
 *
 * Suites that need to drive or assert hooks pass overrides (the values are
 * closed over by the factory, which runs lazily at first router import):
 *
 * ```tsx
 * const mockNavigate = vi.fn();
 * vi.mock("@tanstack/react-router", () =>
 *   createRouterMock({ useNavigate: () => mockNavigate }),
 * );
 * ```
 */
export function createRouterMock(
  overrides: {
    useNavigate?: () => unknown;
    useParams?: () => unknown;
    useSearch?: () => unknown;
  } = {},
) {
  return {
    /**
     * Router `<Link>` stub: a plain anchor carrying `to` as `href`, so link
     * semantics survive rendering. `preventDefault` mirrors real router
     * behavior (no actual navigation) while still forwarding `onClick` for
     * suites that assert click handlers.
     */
    Link: ({
      children,
      to,
      onClick,
      ...props
    }: {
      children?: ReactNode;
      to?: string;
      onClick?: (e: MouseEvent<HTMLAnchorElement>) => void;
    } & Record<string, unknown>) => (
      <a
        href={to}
        onClick={(e) => {
          e.preventDefault();
          onClick?.(e);
        }}
        {...props}
      >
        {children}
      </a>
    ),
    /**
     * TanStack custom-link composition, mocked as identity: the host
     * component renders as-is. Enough for render tests; not a behavioral
     * router (nothing here navigates).
     */
    createLink: (Comp: ComponentType) => Comp,
    useNavigate: overrides.useNavigate ?? (() => vi.fn()),
    useParams: overrides.useParams ?? (() => ({})),
    useSearch: overrides.useSearch ?? (() => ({})),
  };
}
