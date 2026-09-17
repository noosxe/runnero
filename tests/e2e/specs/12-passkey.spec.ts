import { test, expect } from "../fixtures";
import { ADMIN_PASSWORD, login } from "../fixtures";
import { attachVirtualAuthenticator } from "../virtual-authenticator";

/**
 * Flow 12: Passkey (WebAuthn) login (RUN-248, docs/34).
 *
 * The supervisor container is booted with SUPERVISOR_WEBAUTHN_RP_ID, and the
 * Playwright Chromium treats the insecure container origin as secure so
 * navigator.credentials works; ceremonies run against a CDP virtual
 * authenticator (../virtual-authenticator). Discoverable credentials are
 * stored inside the authenticator, so enroll + passkey-login happen in one
 * browser context.
 */
test.describe("Flow 12: Passkey (WebAuthn) Login", () => {
  test("enrolls a passkey in the security tab and signs in with it", async ({ page }) => {
    // Password session first; the same context (and virtual authenticator)
    // performs the passkey ceremony later in this test.
    await login(page);
    await attachVirtualAuthenticator(page);

    // Enroll through the Security tab: password re-check (docs/34 §4.2),
    // then the browser creation prompt fires automatically.
    await page.goto("/settings");
    await page.getByRole("button", { name: "Security" }).click();
    await expect(page.getByText("Active Sessions")).toBeVisible();

    await page.getByTestId("add-passkey-button").click();
    // Scope to the dialog: the change-password card also has a
    // "Current password" input on this tab.
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Current password").fill(ADMIN_PASSWORD);
    await dialog.getByLabel("Name (optional)").fill("E2E virtual key");
    await page.getByTestId("add-passkey-submit").click();

    // The list refreshes with the new credential and its custody signal
    // (a ctap2 virtual authenticator is device-bound, not synced).
    await expect(page.getByText("E2E virtual key")).toBeVisible();
    await expect(page.getByText("Device-bound")).toBeVisible();

    // Log out; the login screen now offers the passkey entry point
    // (GetOnboardingStatus.passkey_available).
    await page.getByText("Supervisor Admin").click();
    await page.getByRole("menuitem", { name: /Sign Out/i }).click();
    await page.waitForURL(/login/);
    await expect(page.getByTestId("passkey-login-button")).toBeVisible();

    // Discoverable ceremony: no username, no password - the credential
    // identifies the user (docs/34 §3.2).
    await page.getByTestId("passkey-login-button").click();
    await page.waitForURL((url) => !url.pathname.includes("/login"));

    // A real session was issued: the auth gate accepts /settings.
    await page.goto("/settings");
    await page.getByRole("button", { name: "Security" }).click();
    await expect(page.getByText("Active Sessions")).toBeVisible();
  });

  test("password fallback still signs in with a passkey enrolled", async ({ authedPage: page }) => {
    // The passkey from the previous test persists in the suite database;
    // the password path must be entirely unaffected (docs/34 §3.3: two
    // independent complete paths).
    await page.goto("/settings");
    await page.getByRole("button", { name: "Security" }).click();
    await expect(page.getByText("Active Sessions")).toBeVisible();
    await expect(page.getByText("E2E virtual key")).toBeVisible();
  });

  test("rejects an assertion for a removed credential", async ({ page }) => {
    await login(page);
    // This test's own virtual authenticator: it must hold the credential it
    // will later try to assert, or the browser never reaches the server.
    await attachVirtualAuthenticator(page);

    // Enroll, then remove the passkey server-side.
    await page.goto("/settings");
    await page.getByRole("button", { name: "Security" }).click();
    await page.getByTestId("add-passkey-button").click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Current password").fill(ADMIN_PASSWORD);
    await dialog.getByLabel("Name (optional)").fill("E2E disposable key");
    await page.getByTestId("add-passkey-submit").click();
    await expect(page.getByText("E2E disposable key")).toBeVisible();

    const row = page.getByRole("row", { name: /E2E disposable key/ });
    await row.getByRole("button", { name: "Remove" }).click();
    await page.getByTestId("confirm-remove-passkey").click();
    await expect(page.getByText("E2E disposable key")).toBeHidden();

    // The authenticator still holds the credential locally, but the server
    // no longer knows it: Finish must reject, and the login screen must
    // say so instead of stranding the user (docs/34 §5.4).
    await page.getByText("Supervisor Admin").click();
    await page.getByRole("menuitem", { name: /Sign Out/i }).click();
    await page.waitForURL(/login/);
    await page.getByTestId("passkey-login-button").click();
    await expect(page.getByText(/passkey assertion rejected/i)).toBeVisible();
    // Still logged out - no session was minted.
    await expect(page).toHaveURL(/login/);
  });
});
