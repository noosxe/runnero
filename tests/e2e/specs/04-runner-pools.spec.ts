import { test, expect } from '@playwright/test';

test.describe('Flow 04: Runner Pools Management', () => {
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

  test('lists created pools and allows filtering', async ({ page }) => {
    await page.goto('/pools');

    await expect(page.getByRole('heading', { name: 'Runner Pools' })).toBeVisible();

    // Verify search input
    const searchInput = page.getByPlaceholder(/Search pools by name/i);
    await expect(searchInput).toBeVisible();
    await searchInput.fill('default');

    // Default pool created in flow 02 should be listed
    await expect(page.getByText('default-pool')).toBeVisible();
  });
});
