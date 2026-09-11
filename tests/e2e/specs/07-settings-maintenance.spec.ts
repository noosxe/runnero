import { test, expect } from "../fixtures";

test.describe("Flow 07: System Settings & Maintenance", () => {
  test("updates global scaling constraints and toggles theme", async ({ authedPage: page }) => {
    await page.goto("/settings");

    await expect(page.getByText(/Supervisor Settings & Administration/i)).toBeVisible();

    // Verify constraints inputs
    const runnersInput = page.getByLabel(/Global Runner Quota/i);
    await expect(runnersInput).toBeVisible();

    // Toggle theme in the AppShell header: the selector must visibly re-skin
    // the app. `dark:` utilities follow the html.dark class (web/src/index.css
    // @custom-variant), so the computed body background must actually change —
    // a class flip alone proved nothing while the variant was still wired to
    // the OS media query, which is why the selector appeared to do nothing.
    const body = page.locator("body");
    const bg = () => body.evaluate((el) => getComputedStyle(el).backgroundColor);
    const lightBg = await bg();

    await page.getByRole("button", { name: "Dark Theme" }).click();
    await expect(page.locator("html")).toHaveClass(/dark/);
    // body uses transition-colors: poll until the interpolated color settles
    await expect.poll(bg).not.toBe(lightBg);

    await page.getByRole("button", { name: "Light Theme" }).click();
    await expect(page.locator("html")).not.toHaveClass(/dark/);
    await expect.poll(bg).toBe(lightBg);
  });
});
