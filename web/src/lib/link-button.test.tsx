import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";
import { LinkButton } from "./link-button";

/**
 * Renders `ui` as the index route of a minimal memory-history router so
 * LinkButton has the router context it needs.
 */
async function renderWithRouter(ui: React.ReactNode) {
  const rootRoute = createRootRoute({});
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => ui,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute]),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  return render(<RouterProvider router={router} />);
}

describe("LinkButton", () => {
  it("renders a real <a> carrying the Button styles, not a <button href>", async () => {
    const { container } = await renderWithRouter(
      <LinkButton to="/pools/$poolId" params={{ poolId: "42" }} variant="ghost" size="xs">
        Manage
      </LinkButton>,
    );

    const anchor = container.querySelector("a");
    expect(anchor).not.toBeNull();
    expect(anchor?.tagName).toBe("A");
    // The old createLink(Button) composition produced an invalid <button href>.
    expect(container.querySelector("button[href]")).toBeNull();
    // Anchor props land on the element…
    expect(anchor).toHaveAttribute("href", "/pools/42");
    // …and the Button props flow through Base UI's render composition.
    expect(anchor).toHaveAttribute("data-slot", "button");
    expect(anchor).toHaveClass("rounded-4xl");
    expect(anchor).toHaveTextContent("Manage");
  });

  it("merges className via tailwind-merge so color overrides replace variant colors", async () => {
    const { container } = await renderWithRouter(
      <LinkButton to="/profiles" className="bg-warning text-white">
        Configure
      </LinkButton>,
    );

    const anchor = container.querySelector("a");
    // The override wins and the default variant's colors are gone — with plain
    // class concatenation both would coexist and CSS source order would decide.
    // Compare class tokens, not substrings (hover:bg-primary/80 is legitimate).
    const classes = (anchor?.className ?? "").split(/\s+/);
    expect(classes).toContain("bg-warning");
    expect(classes).toContain("text-white");
    expect(classes).not.toContain("bg-primary");
    expect(classes).not.toContain("text-primary-foreground");
  });
});
