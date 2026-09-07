import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright E2E configuration for runnero supervisor.
 * Connects to the containerized supervisor daemon running on port 8090.
 */
export default defineConfig({
  testDir: './specs',
  fullyParallel: false, // Run user flows sequentially to maintain deterministic database state
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  reporter: [
    ['list'],
    ['html', { outputFolder: 'playwright-report', open: 'never' }],
  ],
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL || 'http://e2e-supervisor:8090',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    viewport: { width: 1280, height: 720 },
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
