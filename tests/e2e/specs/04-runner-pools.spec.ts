import { test, expect } from '../fixtures';

test.describe('Flow 04: Runner Pools Management', () => {

  test('lists created pools and allows filtering', async ({ authedPage: page }) => {
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
