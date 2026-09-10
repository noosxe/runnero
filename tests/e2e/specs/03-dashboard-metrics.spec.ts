import { test, expect } from '../fixtures';

test.describe('Flow 03: Dashboard & System Metrics', () => {

  test('displays key metric summary cards and latency widgets', async ({ authedPage: page }) => {
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
