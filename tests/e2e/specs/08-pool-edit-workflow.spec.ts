import { test, expect } from '@playwright/test';

// Flow 08: Runner Pool Edit Workflow (docs/22 §7)
// Exercises the edit wizard end-to-end against the real supervisor stack:
// control-plane edit (min_idle), spawn-identity edit with idle recycle,
// rename, and the duplicate-name server rejection path.
test.describe('Flow 08: Runner Pool Edit Workflow', () => {
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
    await page.goto('/pools');
    // Default pool created by the onboarding flow (flow 02)
    await expect(page.getByText('default-pool', { exact: true })).toBeVisible();
  });

  test('edits min_idle (control-plane) without runner impact banner', async ({ page }) => {
    await page.getByRole('button', { name: 'Edit pool default-pool' }).click();

    await expect(page.getByText('Edit Runner Pool')).toBeVisible();
    // Prefilled identity
    await expect(page.getByLabel('Pool Name (Slug)')).toHaveValue('default-pool');

    await page.getByRole('button', { name: /Continue to Scope & Targets/i }).click();
    await expect(page.getByText(/Selected Targets/i)).toBeVisible();
    await page.getByRole('button', { name: /Continue to Specifications/i }).click();

    // Raise the warm-pool target (control-plane field, docs/22 §5.2)
    await page.getByLabel('Min Idle Warm Runners').fill('3');
    await page.getByRole('button', { name: /Review & Confirm/i }).click();

    await expect(page.getByText(/Changed Fields/i)).toBeVisible();
    // Control-plane-only edit: no recycle banner
    await expect(page.getByText(/recycled/i)).toHaveCount(0);

    await page.getByRole('button', { name: 'Save Changes' }).click();
    await expect(page.getByText('Edit Runner Pool')).toBeHidden();

    // The card reflects the new warm-pool target
    const idleStat = page.getByText('Idle Warm Target', { exact: true }).locator('..');
    await expect(idleStat.getByText('3', { exact: true })).toBeVisible();
  });

  test('shows the recycle banner for spawn-identity edits and saves', async ({ page }) => {
    await page.getByRole('button', { name: 'Edit pool default-pool' }).click();

    await page.getByRole('button', { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole('button', { name: /Continue to Specifications/i }).click();

    // Labels are spawn identity (docs/22 §5.2): editing them recycles idle runners
    await page.getByLabel('Runner Labels').fill('self-hosted,linux,e2e');
    await page.getByRole('button', { name: /Review & Confirm/i }).click();

    await expect(page.getByText(/will be recycled to apply the new configuration/i)).toBeVisible();
    await expect(page.getByText(/running jobs are not affected/i)).toBeVisible();

    await page.getByRole('button', { name: 'Save Changes' }).click();
    await expect(page.getByText('Edit Runner Pool')).toBeHidden();

    // Idle runners respawn to target under the new configuration
    await page.getByRole('link', { name: /View Pool Details/i }).first().click();
    await expect
      .poll(async () => page.getByText('idle', { exact: true }).count(), { timeout: 30_000 })
      .toBeGreaterThanOrEqual(1);
  });

  test('renames the pool through the wizard', async ({ page }) => {
    await page.getByRole('button', { name: 'Edit pool default-pool' }).click();

    await page.getByLabel('Pool Name (Slug)').fill('default-pool-renamed');
    await page.getByRole('button', { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole('button', { name: /Continue to Specifications/i }).click();
    await page.getByRole('button', { name: /Review & Confirm/i }).click();

    await expect(page.getByText(/Renaming only changes how the pool is displayed/i)).toBeVisible();

    await page.getByRole('button', { name: 'Save Changes' }).click();
    await expect(page.getByText('Edit Runner Pool')).toBeHidden();

    await expect(
      page.getByRole('heading', { name: 'default-pool-renamed', exact: true }),
    ).toBeVisible();

    // Rename back so the next test's beforeEach keeps finding the default pool
    await page.getByRole('button', { name: 'Edit pool default-pool-renamed' }).click();
    await page.getByLabel('Pool Name (Slug)').fill('default-pool');
    await page.getByRole('button', { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole('button', { name: /Continue to Specifications/i }).click();
    await page.getByRole('button', { name: /Review & Confirm/i }).click();
    await page.getByRole('button', { name: 'Save Changes' }).click();
    await expect(page.getByText('Edit Runner Pool')).toBeHidden();
    await expect(
      page.getByRole('heading', { name: 'default-pool', exact: true }),
    ).toBeVisible();
  });

  test('rejects renaming onto an existing pool name with a server error banner', async ({ page }) => {
    // Create a collision pool first
    await page.getByRole('button', { name: '+ Add Runner Pool' }).click();
    await page.getByLabel('Pool Name (Slug)').fill('collision-pool');
    await page.getByRole('button', { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole('button', { name: 'Select All Filtered' }).click();
    await page.getByRole('button', { name: /Continue to Specifications/i }).click();
    await page.getByRole('button', { name: /Review & Confirm/i }).click();
    await page.getByRole('button', { name: /Create Runner Pool/i }).click();
    await expect(page.getByText('Create Runner Pool Wizard')).toBeHidden();
    await expect(
      page.getByRole('heading', { name: 'collision-pool', exact: true }),
    ).toBeVisible();

    // Try to rename default-pool onto collision-pool's name
    await page.getByRole('button', { name: 'Edit pool default-pool' }).click();
    await page.getByLabel('Pool Name (Slug)').fill('collision-pool');
    await page.getByRole('button', { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole('button', { name: /Continue to Specifications/i }).click();
    await page.getByRole('button', { name: /Review & Confirm/i }).click();
    await page.getByRole('button', { name: 'Save Changes' }).click();

    // Server rejects with already_exists; the wizard surfaces the message and stays open
    await expect(page.getByText(/already exists/i)).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Edit Runner Pool')).toBeVisible();
  });
});
