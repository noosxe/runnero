import { test, expect } from '../fixtures';

test.describe('Flow 06: Renovate Bot Management', () => {

  test('navigates to renovate dashboard and displays pool automation status', async ({ authedPage: page }) => {
    await page.goto('/renovate');

    await expect(page.getByText('Renovate Bot Dashboard')).toBeVisible();
    await expect(page.getByText('Configured Pools')).toBeVisible();
    await expect(page.getByText('Renovate Active')).toBeVisible();
  });
});
