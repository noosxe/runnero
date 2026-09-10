import { test, expect } from '../fixtures';

test.describe('Flow 02: Onboarding Wizard & Configuration', () => {

  test('walks through git provider, safeguards, initial pool and completes onboarding', async ({ authedPage: page }) => {
    await page.goto('/onboarding');

    // Step 1: authenticate the existing admin (created in flow 01) when asked;
    // successful login auto-advances to Step 2. With the session already
    // active the wizard starts directly at the first incomplete step (Step 2
    // when no auth profile exists yet), so only advance when the button shows.
    const loginToContinue = page.getByRole('button', { name: /Log In to Continue Setup/i });
    if (await loginToContinue.isVisible()) {
      await page.getByLabel('Admin Username').fill('admin');
      await page.getByRole('textbox', { name: 'Admin Password' }).fill('AdminPassword123!');
      await loginToContinue.click();
    } else {
      const nextFromConfigured = page.getByRole('button', { name: /Next: Git Provider/i });
      if (await nextFromConfigured.isVisible()) {
        await nextFromConfigured.click();
      }
    }

    // Connect Git Provider using Personal Access Token (default method)
    await expect(page.getByText(/Step 2 of 5: Connect Git Provider/i)).toBeVisible();
    const tokenInput = page.getByLabel('Personal Access Token (PAT)');
    await tokenInput.fill('ghp_mock_token_abcdef1234567890');
    await page.getByRole('button', { name: /Next: Safeguards/i }).click();

    // Step 3: Safeguards
    await expect(page.getByText(/Step 3 of 5: Global Scaling Safeguards/i)).toBeVisible();
    await page.getByRole('button', { name: /Next: Initial Pool/i }).click();

    // Step 4: Initial Pool Setup
    await expect(page.getByText(/Step 4 of 5: Initial Runner Pool Setup/i)).toBeVisible();
    const repoUrlInput = page.getByLabel('Repository / Organization URL');
    await repoUrlInput.fill('https://github.com/test-org/test-repo');

    await page.getByRole('button', { name: /Next: Review & Launch/i }).click();

    // Step 5: Review & Launch
    await expect(page.getByText(/Step 5 of 5: Review & Launch Supervisor/i)).toBeVisible();
    await page.getByRole('button', { name: /Confirm & Launch Supervisor/i }).click();

    // After launch, should navigate to dashboard
    await page.waitForURL('/');
    await expect(page.getByText(/Dashboard Overview|Runner Dashboard/)).toBeVisible();
  });
});
