import { test, expect } from "../fixtures";

test.describe("Flow 10: Session Control (Sessions Tab & Logout)", () => {
  test("sessions tab lists the current session and logout invalidates it server-side", async ({
    authedPage: page,
  }) => {
    // The sessions surface lives on the account page since RUN-282
    // (docs/32 §7, docs/37).
    await page.goto("/account/sessions");
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
    await page.getByTestId("user-nav-trigger").click();
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

    await page.getByTestId("user-nav-trigger").click();
    await page.getByRole("menuitem", { name: /Sign Out/i }).click();
    await page.waitForURL(/login/);
    await expect(page.getByLabel("Username")).toBeVisible();
  });
});
