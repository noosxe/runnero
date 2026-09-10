import { test as base, expect, type Page } from '@playwright/test';

// Shared Playwright fixtures for the runnero E2E suite (RUN-135).
//
// Every spec previously duplicated the same login dance in `beforeEach`
// (goto /login → fill → Sign In → wait for the session); when that dance
// rotted, it had to be fixed eight times. It now lives here exactly once.
// Specs that need an authenticated page destructure `authedPage`; specs that
// additionally depend on onboarding state (the `default-pool` created by the
// wizard, flow 02) destructure `onboardedPage` and declare that dependency
// explicitly instead of relying on execution order.

export const ADMIN_USERNAME = 'admin';
export const ADMIN_PASSWORD = 'AdminPassword123!';
// PAT accepted by the mock Git provider (tests/e2e/mock/provider).
const MOCK_PROVIDER_PAT = 'ghp_mock_token_abcdef1234567890';

// login authenticates the pre-bootstrapped admin account and waits for the
// session to establish before any navigation can cancel the in-flight login
// POST. Safe to call redundantly: when `/login` redirects elsewhere (already
// authenticated, or the not-yet-onboarded system routes to `/onboarding`),
// the sign-in step is skipped. On an un-onboarded system callers need
// `ensureOnboarded` instead — the plain login form does not exist yet.
export async function login(page: Page): Promise<void> {
  await page.goto('/login');
  if (!page.url().includes('/login')) {
    return;
  }
  await page.getByLabel('Username').fill(ADMIN_USERNAME);
  await page.getByRole('textbox', { name: 'Password' }).fill(ADMIN_PASSWORD);
  await page.getByRole('button', { name: /Sign In/i }).click();
  await page.waitForURL((url) => !url.pathname.includes('/login'));
}

// completeOnboarding drives the configuration wizard to completion on a
// database where onboarding has not finished yet. Mirrors the wizard walk
// specified by flow 02, minus the mid-step documentation assertions. Callers
// must already be past Step 1 (bootstrap) or the wizard must show its
// "Log In to Continue Setup" re-authentication prompt.
async function completeOnboarding(page: Page): Promise<void> {
  // Authenticate the existing admin when the wizard asks; with the session
  // already active the wizard starts directly at the first incomplete step.
  const loginToContinue = page.getByRole('button', { name: /Log In to Continue Setup/i });
  const askedToLogIn = await loginToContinue
    .waitFor({ state: 'visible', timeout: 2_000 })
    .then(() => true)
    .catch(() => false);
  if (askedToLogIn) {
    await page.getByLabel('Admin Username').fill(ADMIN_USERNAME);
    await page.getByRole('textbox', { name: 'Admin Password' }).fill(ADMIN_PASSWORD);
    await loginToContinue.click();
  } else {
    const nextFromConfigured = page.getByRole('button', { name: /Next: Git Provider/i });
    if (await nextFromConfigured.isVisible()) {
      await nextFromConfigured.click();
    }
  }

  // Step 2: connect the mock Git provider via PAT.
  await expect(page.getByText(/Step 2 of 5: Connect Git Provider/i)).toBeVisible();
  await page.getByLabel('Personal Access Token (PAT)').fill(MOCK_PROVIDER_PAT);
  await page.getByRole('button', { name: /Next: Safeguards/i }).click();

  // Step 3: accept the default global safeguards.
  await expect(page.getByText(/Step 3 of 5: Global Scaling Safeguards/i)).toBeVisible();
  await page.getByRole('button', { name: /Next: Initial Pool/i }).click();

  // Step 4: initial pool pointing at the mock provider repository.
  await expect(page.getByText(/Step 4 of 5: Initial Runner Pool Setup/i)).toBeVisible();
  await page.getByLabel('Repository / Organization URL').fill('https://github.com/test-org/test-repo');
  await page.getByRole('button', { name: /Next: Review & Launch/i }).click();

  // Step 5: launch and land on the dashboard.
  await expect(page.getByText(/Step 5 of 5: Review & Launch Supervisor/i)).toBeVisible();
  await page.getByRole('button', { name: /Confirm & Launch Supervisor/i }).click();
  await page.waitForURL('/');
}

// ensureOnboarded leaves the page authenticated with onboarding completed
// (the `default-pool` artifact exists), regardless of database state. This is
// the explicit declaration of the dependency flows like 08 have on flow 02.
//
// Routing is by content, not URL: a fresh database renders the wizard's
// bootstrap step even under /login (the login form does not exist until an
// administrator exists), so a URL check alone races the client router.
export async function ensureOnboarded(page: Page): Promise<void> {
  await page.goto('/');

  const bootstrapPassword = page.getByLabel('Password (min 10 characters)');
  const loginUsername = page.getByLabel('Username');
  await bootstrapPassword.or(loginUsername).first().waitFor({ state: 'visible' });

  if (await bootstrapPassword.isVisible()) {
    // Fresh database: bootstrap the master administrator (wizard Step 1).
    await bootstrapPassword.fill(ADMIN_PASSWORD);
    await page.getByLabel('Confirm Password').fill(ADMIN_PASSWORD);
    await page.getByRole('button', { name: /Next: Git Provider/i }).click();
  } else {
    // Established system: sign in through the plain login form.
    await loginUsername.fill(ADMIN_USERNAME);
    await page.getByRole('textbox', { name: 'Password' }).fill(ADMIN_PASSWORD);
    await page.getByRole('button', { name: /Sign In/i }).click();
    await page.waitForURL((url) => !url.pathname.includes('/login'));
  }

  // Finish the wizard when onboarding has not completed yet.
  if (page.url().includes('/onboarding')) {
    await completeOnboarding(page);
  }

  await page.goto('/pools');
  await expect(page.getByText('default-pool', { exact: true })).toBeVisible();
}

export const test = base.extend<{
  authedPage: Page;
  onboardedPage: Page;
}>({
  // Page with an established admin session.
  authedPage: async ({ page }, use) => {
    await login(page);
    await use(page);
  },

  // Page with an established admin session AND a completed onboarding
  // (default pool present). Use for flows that operate on the pools the
  // onboarding wizard creates.
  onboardedPage: async ({ page }, use) => {
    await ensureOnboarded(page);
    await use(page);
  },
});

export { expect } from '@playwright/test';
