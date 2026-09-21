import { test, expect, login } from "../fixtures";

// Flow 11: stale session cookie self-healing (RUN-244, docs/32 §3.3).
//
// Incident: a browser holding a session cookie whose row no longer exists
// (expired-and-pruned or revoked) used to get 401 "unknown session" on EVERY
// RPC — including the public ones (Login, GetOnboardingStatus) — locking the
// operator out of the app with no in-app recovery path. The interceptor now
// degrades an invalid cookie to anonymous access on public procedures, so the
// sign-in flow works without manually clearing cookies.
test.describe("Flow 11: Stale Session Cookie Self-Healing (RUN-244)", () => {
  test("a browser carrying a dead session cookie can still reach /login and sign in", async ({
    page,
  }) => {
    // Poison the browser context with a cookie whose session row does not
    // exist server-side.
    await page.context().addCookies([
      {
        name: "session_token",
        value: "stale-dead-token-run-244",
        domain: "e2e-supervisor",
        path: "/",
      },
    ]);

    // The guards must degrade the dead cookie to anonymous access: the login
    // form is reachable (not the wizard, not the guard error page).
    await page.goto("/login");
    await expect(page.getByLabel("Username")).toBeVisible();

    // Login itself must not 401 on the stale cookie (the pre-RUN-244 lockout).
    await login(page);
    await expect(page.getByTestId("user-nav-trigger")).toBeVisible();
  });
});
