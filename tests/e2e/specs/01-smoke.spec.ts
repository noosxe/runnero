import { test, expect } from '@playwright/test';

test.describe('E2E Infrastructure Smoke Test', () => {
  test('supervisor healthz responds successfully', async ({ request }) => {
    const response = await request.get('/healthz');
    expect(response.status()).toBe(200);
    const body = await response.json();
    expect(body.status).toBe('healthy');
  });

  test('supervisor root serves embedded SPA index page', async ({ page }) => {
    await page.goto('/');
    // Check that title or root app shell element is mounted
    await expect(page).toHaveTitle(/AIO Supervisor|GitHub Runner|Login|Setup/i);
  });
});
