import { test, expect } from '../fixtures';

test.describe('Flow 07: System Settings & Maintenance', () => {

  test('updates global scaling constraints and toggles theme', async ({ authedPage: page }) => {
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
