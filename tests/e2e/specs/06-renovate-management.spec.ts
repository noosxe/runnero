import { test, expect } from "../fixtures";

// v1.0.0 gating (RUN-289): Renovate surfaces stay visible but are disabled —
// the sidebar entry is unclickable and the route renders a disabled notice.
test.describe("Flow 06: Renovate Bot Management (gated for v1.0.0)", () => {
  test("renovate dashboard shows the disabled notice without interactive controls", async ({
    authedPage: page,
  }) => {
    await page.goto("/renovate");

    // Header stays (visible surface)…
    await expect(page.getByText("Renovate Bot Dashboard")).toBeVisible();
    // …but the interactive dashboard is replaced by the notice.
    await expect(page.getByTestId("renovate-disabled-notice")).toBeVisible();
    await expect(page.getByText(/Disabled for v1\.0\.0/)).toBeVisible();
    await expect(page.getByText("Configured Pools")).toHaveCount(0);
    await expect(page.getByRole("button", { name: /trigger/i })).toHaveCount(0);
  });

  test("sidebar renovate entry is visible but unclickable", async ({ authedPage: page }) => {
    await page.goto("/pools");

    // The gated entry renders as a span (not a link) carrying the hint in
    // its title attribute.
    const entry = page.locator('span[title^="Renovate Bot"]');
    await expect(entry).toBeVisible();
    await expect(entry).toHaveAttribute("aria-disabled", "true");

    // Clicking it must not navigate to /renovate. The disabled entry has
    // pointer-events: none, so the click is forced through (no navigation
    // target exists behind it — it is a span, not a link).
    await entry.click({ force: true });
    await expect(page).not.toHaveURL(/\/renovate/);
  });
});
