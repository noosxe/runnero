import { test, expect } from '@playwright/test';

test.describe('Flow 06: Renovate Bot Management', () => {
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

  test('navigates to renovate dashboard and displays pool automation status', async ({ page }) => {
    await page.goto('/renovate');

    await expect(page.getByText('Renovate Bot Dashboard')).toBeVisible();
    await expect(page.getByText('Configured Pools')).toBeVisible();
    await expect(page.getByText('Renovate Active')).toBeVisible();
  });
});
