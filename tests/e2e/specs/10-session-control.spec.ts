import { test, expect } from "../fixtures";

test.describe("Flow 10: Session Control (Security Tab & Logout)", () => {
  test("security tab lists the current session and logout invalidates it server-side", async ({
    authedPage: page,
  }) => {
    await page.goto("/settings");

    // Open the Security tab (RUN-232, docs/32 §7).
    await page.getByRole("button", { name: "Security" }).click();
    await expect(page.getByText("Active Sessions")).toBeVisible();

    // Every listed session carries a parsed device label (the Playwright
    // UA reports Chrome on some base OS, so only the browser prefix is
    // pinned), and exactly one row is marked current - .first() because
    // earlier suite flows leave their logins in the table too.
    await expect(page.getByText(/Chrome \d+ on/).first()).toBeVisible();
    await expect(page.getByText("Current session", { exact: true })).toBeVisible();

    // Logout through the user menu: the Logout RPC deletes the session row
    // and expires the cookie server-side (docs/32 §3.5) - not just a
    // client-side cache clear.
    await page.getByText("Supervisor Admin").click();
    await page.getByRole("menuitem", { name: /Sign Out/i }).click();
    await page.waitForURL(/login/);

    // The dead cookie can no longer reach /settings: the auth gate must
    // bounce back to /login rather than render the page.
    await page.goto("/settings");
    await page.waitForURL(/login/);
  });

  test("sign out flows through the user menu and lands on the login form", async ({
    authedPage: page,
  }) => {
    await page.goto("/");

    await page.getByText("Supervisor Admin").click();
    await page.getByRole("menuitem", { name: /Sign Out/i }).click();
    await page.waitForURL(/login/);
    await expect(page.getByLabel("Username")).toBeVisible();
  });
});
