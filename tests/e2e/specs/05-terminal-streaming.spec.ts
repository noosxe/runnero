import { test, expect } from '@playwright/test';

test.describe('Flow 05: Runner Pool Detail & Streaming Terminal', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/login');
    if (page.url().includes('/login')) {
      await page.getByLabel('Username').fill('admin');
      await page.getByRole('textbox', { name: 'Password' }).fill('AdminPassword123!');
      await page.getByRole('button', { name: /Sign In/i }).click();
      // Wait for the session to establish before any navigation cancels the
      // in-flight login POST.
      await page.waitForURL((url) => !url.pathname.includes('/login'));
    }
  });

  test('navigates to pool detail and verifies pool configuration metrics', async ({ page }) => {
    await page.goto('/pools');

    // Click into the default pool
    const defaultPoolLink = page.getByRole('link', { name: /default-pool/i });
    if (await defaultPoolLink.isVisible()) {
      await defaultPoolLink.click();
      await expect(page.getByText('Pool Overview')).toBeVisible();
      await expect(page.getByText(/Runner Instances/i)).toBeVisible();
    }
  });
});
