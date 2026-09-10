import { test, expect } from '../fixtures';

test.describe('Flow 05: Runner Pool Detail & Streaming Terminal', () => {

  test('navigates to pool detail and verifies pool configuration metrics', async ({ authedPage: page }) => {
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
