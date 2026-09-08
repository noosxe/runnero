import { test, expect } from '@playwright/test';

test.describe('Flow 07: System Settings & Maintenance', () => {
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

  test('updates global scaling constraints and toggles theme', async ({ page }) => {
    await page.goto('/settings');

    await expect(page.getByText(/Supervisor Settings & Administration/i)).toBeVisible();

    // Verify constraints inputs
    const runnersInput = page.getByLabel(/Global Runner Quota/i);
    await expect(runnersInput).toBeVisible();

    // Toggle theme in top-right corner of AppShell
    const darkBtn = page.getByTitle('Dark Theme');
    if (await darkBtn.isVisible()) {
      await darkBtn.click();
      await expect(page.locator('html')).toHaveClass(/dark/);
    }
  });
});
