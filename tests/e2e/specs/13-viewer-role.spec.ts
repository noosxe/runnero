import { test, expect } from "../fixtures";
import { login } from "../fixtures";
import { attachVirtualAuthenticator } from "../virtual-authenticator";

// Flow 13: Viewer role (RUN-236, docs/35). The suite database persists
// across tests, so the accounts created here live for the whole spec; a
// fresh browser context per test provides the session isolation the story
// needs (admin and viewer never share a session).

const VIEWER_USERNAME = "e2e-viewer";
const VIEWER_PASSWORD = "e2e-viewer-password-1";

test("admin creates a viewer, who sees the read-only surface", async ({ page }) => {
  await login(page);

  // Create the viewer via the Users tab (admin-only surface).
  await page.goto("/settings");
  await page.getByRole("button", { name: "Users" }).click();
  await expect(page.getByTestId("users-card")).toBeVisible();
  await page.getByTestId("add-user-button").click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Username").fill(VIEWER_USERNAME);
  await dialog.getByLabel("Initial password").fill(VIEWER_PASSWORD);
  await dialog.getByLabel("Confirm password").fill(VIEWER_PASSWORD);
  await page.getByTestId("add-user-submit").click();
  // Scope to the card: the "user created" toast echoes the username too.
  await expect(
    page.getByTestId("users-card").getByRole("row", { name: new RegExp(VIEWER_USERNAME) }),
  ).toBeVisible();

  // Log out; sign in as the viewer.
  await page.getByTestId("user-nav-trigger").click();
  await page.getByRole("menuitem", { name: /Sign Out/i }).click();
  await page.waitForURL(/login/);
  await page.getByLabel("Username").fill(VIEWER_USERNAME);
  await page.getByRole("textbox", { name: "Password" }).fill(VIEWER_PASSWORD);
  await page.getByRole("button", { name: "Sign In", exact: true }).click();
  await page.waitForURL((url) => !url.pathname.includes("/login"));

  // The viewer's settings page is the Instance tab only (docs/35 §2.4, as
  // amended by RUN-282: personal surfaces moved to /account).
  await page.goto("/settings");
  await expect(page.getByTestId("instance-card")).toBeVisible();
  await expect(page.getByRole("button", { name: "Security" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Users" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Global Constraints" })).toHaveCount(0);

  // An admin-only deep link clamps back to instance for a viewer (RUN-257):
  // the param never renders a hidden admin surface.
  await page.goto("/settings?tab=users");
  await expect(page.getByTestId("instance-card")).toBeVisible();
  await expect(page.getByRole("button", { name: "Users" })).toHaveCount(0);

  // The account page is for every role; the passkeys card shows because the
  // E2E stack configures WebAuthn (RUN-282).
  await page.goto("/account/security");
  await expect(page.getByTestId("add-passkey-button")).toBeVisible();
  await page.goto("/account/sessions");
  await expect(page.getByText("Active Sessions")).toBeVisible();
  // Auth profiles are an entirely admin surface; the fetch is gated so no
  // admin-bucket RPC fires from the viewer session (docs/35 section 2.4).
  await page.goto("/profiles");
  await expect(page.getByTestId("admin-required")).toBeVisible();

  // Pools stay readable; the mutation affordances are gone.
  await page.goto("/pools");
  await expect(page.getByRole("heading", { name: "Pools" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Add Runner Pool/i })).toHaveCount(0);
});

test("promotes the viewer to admin; the change applies to their session", async ({ page }) => {
  await login(page);

  await page.goto("/settings");
  await page.getByRole("button", { name: "Users" }).click();
  await page.getByTestId("users-card").waitFor({ state: "visible" });
  const row = page.getByRole("row", { name: new RegExp(VIEWER_USERNAME) });
  await row.waitFor({ state: "visible" });
  await row.getByRole("button", { name: `Change role for ${VIEWER_USERNAME}` }).click();
  await page.getByTestId("confirm-role-change").click();
  // The role chip flips to Admin in the refreshed list (exact match: the
  // username "e2e-viewer" would otherwise substring-match case-insensitively).
  await expect(row.getByText("Admin", { exact: true })).toBeVisible();

  // Log out; the promoted user now sees the admin surface without any
  // server-side magic - the role is simply read live on every request.
  await page.getByTestId("user-nav-trigger").click();
  await page.getByRole("menuitem", { name: /Sign Out/i }).click();
  await page.waitForURL(/login/);
  await page.getByLabel("Username").fill(VIEWER_USERNAME);
  await page.getByRole("textbox", { name: "Password" }).fill(VIEWER_PASSWORD);
  await page.getByRole("button", { name: "Sign In", exact: true }).click();
  await page.waitForURL((url) => !url.pathname.includes("/login"));

  await page.goto("/settings");
  await expect(page.getByRole("button", { name: "Users" })).toBeVisible();
});

test("a viewer's passkey signs in at viewer role", async ({ page }) => {
  // Reset the promoted account back to viewer via the API surface the UI
  // uses: demote through the users tab as admin.
  await login(page);
  await page.goto("/settings");
  await page.getByRole("button", { name: "Users" }).click();
  const row = page.getByRole("row", { name: new RegExp(VIEWER_USERNAME) });
  await row.waitFor({ state: "visible" });
  await row.getByRole("button", { name: `Change role for ${VIEWER_USERNAME}` }).click();
  await page.getByTestId("confirm-role-change").click();
  await expect(row.getByText("Viewer", { exact: true })).toBeVisible();

  // Switch to the viewer's own context: log in and enroll a passkey.
  await page.getByTestId("user-nav-trigger").click();
  await page.getByRole("menuitem", { name: /Sign Out/i }).click();
  await page.waitForURL(/login/);
  await page.getByLabel("Username").fill(VIEWER_USERNAME);
  await page.getByRole("textbox", { name: "Password" }).fill(VIEWER_PASSWORD);
  await page.getByRole("button", { name: "Sign In", exact: true }).click();
  await page.waitForURL((url) => !url.pathname.includes("/login"));

  await attachVirtualAuthenticator(page);
  await page.goto("/account/security");
  await expect(page.getByTestId("add-passkey-button")).toBeVisible();
  await page.getByTestId("add-passkey-button").click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Current password").fill(VIEWER_PASSWORD);
  await dialog.getByLabel("Name (optional)").fill("viewer key");
  await page.getByTestId("add-passkey-submit").click();
  await expect(page.getByText("viewer key")).toBeVisible();

  // Passwordless login lands the viewer role (docs/35 §2.5): the Security
  // tab works, the Users tab does not exist.
  await page.getByTestId("user-nav-trigger").click();
  await page.getByRole("menuitem", { name: /Sign Out/i }).click();
  await page.waitForURL(/login/);
  await page.getByTestId("passkey-login-button").click();
  await page.waitForURL((url) => !url.pathname.includes("/login"));

  await page.goto("/settings");
  await expect(page.getByTestId("instance-card")).toBeVisible();
  await expect(page.getByRole("button", { name: "Users" })).toHaveCount(0);
  await page.goto("/account/sessions");
  await expect(page.getByText("Active Sessions")).toBeVisible();
});
