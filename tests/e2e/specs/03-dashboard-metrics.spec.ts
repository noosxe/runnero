import { test, expect } from '@playwright/test';

test.describe('Flow 03: Dashboard & System Metrics', () => {
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

  test('displays key metric summary cards and latency widgets', async ({ page }) => {
    await page.goto('/');

    // Validate page header
    await expect(page.getByText(/Dashboard Overview|Runner Dashboard/)).toBeVisible();

    // Validate metrics cards
    await expect(page.getByText(/Jobs Executed/i)).toBeVisible();
    await expect(page.getByText(/Success Rate/i)).toBeVisible();
    await expect(page.getByText(/Queue Wait-Time Latency/i)).toBeVisible();
    await expect(page.getByText(/Execution Health & Ratio|Avg Job Runtime/i)).toBeVisible();

    // Validate Pools Summary section
    await expect(page.getByText(/Configured Runner Pools/i)).toBeVisible();
  });
});
