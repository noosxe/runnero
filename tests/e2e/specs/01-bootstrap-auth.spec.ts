import { test, expect } from '@playwright/test';

test.describe('Flow 01: System Bootstrap & Authentication', () => {
  test('uninitialized supervisor redirects to onboarding page and sets up admin', async ({ page }) => {
    // Navigate to root
    await page.goto('/');

    // Wait for redirect to onboarding
    await page.waitForURL(/\/onboarding/);
    await expect(page.getByText('System Onboarding')).toBeVisible();
    await expect(page.getByText(/Step 1 of 5: Create Master Administrator/i)).toBeVisible();

    // Fill in admin credentials
    const passwordInput = page.getByLabel('Password (min 10 characters)');
    const confirmInput = page.getByLabel('Confirm Password');

    await passwordInput.fill('AdminPassword123!');
    await confirmInput.fill('AdminPassword123!');

    // Click Next
    const nextBtn = page.getByRole('button', { name: /Next: Git Provider/i });
    await nextBtn.click();

    // Step 2 should now be visible
    await expect(page.getByText(/Step 2 of 5: Connect Git Provider/i)).toBeVisible();
  });

  test('login route protection redirects unauthenticated users', async ({ browser }) => {
    // Fresh context with no cookies
    const context = await browser.newContext();
    const page = await context.newPage();

    await page.goto('/settings');
    // Onboarding is not completed yet (flow 02 finishes it), so the auth gate
    // routes unauthenticated users to /onboarding instead of /settings.
    await page.waitForURL(/\/onboarding/);
    await expect(page.getByText('System Onboarding')).toBeVisible();

    await context.close();
  });

  test('valid credentials successfully log in and establish session', async ({ page }) => {
    await page.goto('/login');

    await page.getByLabel('Username').fill('admin');
    await page.getByRole('textbox', { name: 'Password' }).fill('AdminPassword123!');
    await page.getByRole('button', { name: /Sign In/i }).click();

    // After login, should land on dashboard or onboarding if setup not marked complete
    await expect(page).not.toHaveURL(/\/login/);
  });
});
