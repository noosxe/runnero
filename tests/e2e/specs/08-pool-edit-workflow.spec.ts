import { type APIRequestContext, type Page } from "@playwright/test";

import { test, expect } from "../fixtures";

// Admin control surface of the mock Git provider (tests/e2e/mock/provider):
// seeds the registered-runners registry the supervisor polls for busy-state
// sync (docs/19) and the ghost sweep (docs/20).
const MOCK_PROVIDER_URL = process.env.MOCK_PROVIDER_URL ?? "http://e2e-mock-provider:8095";

// Flow 08: Runner Pool Edit Workflow (docs/22 §7)
// Exercises the edit wizard end-to-end against the real supervisor stack:
// control-plane edit (min_idle), spawn-identity edit with idle recycle,
// rename, and the duplicate-name server rejection path. The final test
// simulates busy runner state through the mock provider's runners listing
// (docs/19) and verifies the recycle spares busy runners (docs/22 §5.2).
test.describe("Flow 08: Runner Pool Edit Workflow", () => {
  test("edits min_idle (control-plane) without runner impact banner", async ({
    onboardedPage: page,
  }) => {
    await page.getByRole("button", { name: "Edit pool default-pool" }).click();

    await expect(page.getByText("Edit Runner Pool")).toBeVisible();
    // Prefilled identity
    await expect(page.getByLabel("Pool Name (Slug)")).toHaveValue("default-pool");

    await page.getByRole("button", { name: /Continue to Scope & Targets/i }).click();
    await expect(page.getByText(/Selected Targets/i)).toBeVisible();
    await page.getByRole("button", { name: /Continue to Specifications/i }).click();

    // Raise the warm-pool target (control-plane field, docs/22 §5.2)
    await page.getByLabel("Min Idle Warm Runners").fill("3");
    await page.getByRole("button", { name: /Review & Confirm/i }).click();

    await expect(page.getByText(/Changed Fields/i)).toBeVisible();
    // Control-plane-only edit: no recycle banner
    await expect(page.getByText(/recycled/i)).toHaveCount(0);

    await page.getByRole("button", { name: "Save Changes" }).click();
    await expect(page.getByText("Edit Runner Pool")).toBeHidden();

    // The card reflects the new warm-pool target
    const idleStat = page.getByText("Idle Warm Target", { exact: true }).locator("..");
    await expect(idleStat.getByText("3", { exact: true })).toBeVisible();
  });

  test("shows the recycle banner for spawn-identity edits and saves", async ({
    onboardedPage: page,
  }) => {
    await page.getByRole("button", { name: "Edit pool default-pool" }).click();

    await page.getByRole("button", { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole("button", { name: /Continue to Specifications/i }).click();

    // Labels are spawn identity (docs/22 §5.2): editing them recycles idle runners
    await page.getByLabel("Runner Labels").fill("self-hosted,linux,e2e");
    await page.getByRole("button", { name: /Review & Confirm/i }).click();

    await expect(page.getByText(/will be recycled to apply the new configuration/i)).toBeVisible();
    await expect(page.getByText(/running jobs are not affected/i)).toBeVisible();

    await page.getByRole("button", { name: "Save Changes" }).click();
    await expect(page.getByText("Edit Runner Pool")).toBeHidden();

    // Idle runners respawn to target under the new configuration
    await page
      .getByRole("link", { name: /View Pool Details/i })
      .first()
      .click();
    await expect
      .poll(async () => page.getByText("idle", { exact: true }).count(), { timeout: 30_000 })
      .toBeGreaterThanOrEqual(1);
  });

  test("renames the pool through the wizard", async ({ onboardedPage: page }) => {
    await page.getByRole("button", { name: "Edit pool default-pool" }).click();

    await page.getByLabel("Pool Name (Slug)").fill("default-pool-renamed");
    await page.getByRole("button", { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole("button", { name: /Continue to Specifications/i }).click();
    await page.getByRole("button", { name: /Review & Confirm/i }).click();

    await expect(page.getByText(/Renaming only changes how the pool is displayed/i)).toBeVisible();

    await page.getByRole("button", { name: "Save Changes" }).click();
    await expect(page.getByText("Edit Runner Pool")).toBeHidden();

    await expect(
      page.getByRole("heading", { name: "default-pool-renamed", exact: true }),
    ).toBeVisible();

    // Rename back so the next test keeps finding the default pool
    await page.getByRole("button", { name: "Edit pool default-pool-renamed" }).click();
    await page.getByLabel("Pool Name (Slug)").fill("default-pool");
    await page.getByRole("button", { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole("button", { name: /Continue to Specifications/i }).click();
    await page.getByRole("button", { name: /Review & Confirm/i }).click();
    await page.getByRole("button", { name: "Save Changes" }).click();
    await expect(page.getByText("Edit Runner Pool")).toBeHidden();
    await expect(page.getByRole("heading", { name: "default-pool", exact: true })).toBeVisible();
  });

  test("rejects renaming onto an existing pool name with a server error banner", async ({
    onboardedPage: page,
  }) => {
    // Create a collision pool first
    await page.getByRole("button", { name: "+ Add Runner Pool" }).click();
    await page.getByLabel("Pool Name (Slug)").fill("collision-pool");
    await page.getByRole("button", { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole("button", { name: "Select All Filtered" }).click();
    await page.getByRole("button", { name: /Continue to Specifications/i }).click();
    await page.getByRole("button", { name: /Review & Confirm/i }).click();
    await page.getByRole("button", { name: /Create Runner Pool/i }).click();
    await expect(page.getByText("Create Runner Pool Wizard")).toBeHidden();
    await expect(page.getByRole("heading", { name: "collision-pool", exact: true })).toBeVisible();

    // Try to rename default-pool onto collision-pool's name
    await page.getByRole("button", { name: "Edit pool default-pool" }).click();
    await page.getByLabel("Pool Name (Slug)").fill("collision-pool");
    await page.getByRole("button", { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole("button", { name: /Continue to Specifications/i }).click();
    await page.getByRole("button", { name: /Review & Confirm/i }).click();
    await page.getByRole("button", { name: "Save Changes" }).click();

    // Server rejects with already_exists; the wizard surfaces the message and stays open
    await expect(page.getByText(/already exists/i)).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText("Edit Runner Pool")).toBeVisible();
  });

  test("spares busy runners when a spawn-identity edit recycles idle ones", async ({
    onboardedPage: page,
    request,
  }) => {
    // Reconcile ticks run every 10s; busy-sync convergence plus warm-pool
    // respawn exceed the default 30s test timeout
    test.setTimeout(180_000);
    // Busy-state sync (docs/19) needs a forge listing: mirror the pool's
    // tracked runners in the mock provider's registry, then flip one to busy.
    await openDefaultPoolDetail(page);

    // The warm pool keeps at least one idle standby around
    await expect
      .poll(async () => (await poolRunnerRows(page)).filter((r) => r.state === "idle").length, {
        timeout: 30_000,
      })
      .toBeGreaterThanOrEqual(1);
    const initial = await poolRunnerRows(page);
    const busyRunner = initial.find((r) => r.state === "idle");
    if (!busyRunner) throw new Error("no idle runner available to flip busy");

    // Register every tracked runner at the forge so the recycle path can also
    // deregister idle ones through the real API (docs/20 §4.3)
    for (const runner of initial) {
      await registerRemoteRunner(request, runner.name, runner.name === busyRunner.name);
    }

    // Busy-state sync converges within a reconcile tick (docs/19): the forge
    // listing is authoritative, so the mirrored busy flag flips the runner
    await expect
      .poll(
        async () => (await poolRunnerRows(page)).find((r) => r.name === busyRunner.name)?.state,
        { timeout: 60_000 },
      )
      .toBe("busy");

    const recycledNames = (await poolRunnerRows(page))
      .filter((r) => r.state === "idle")
      .map((r) => r.name);

    // Spawn-identity edit (labels): idle runners are recycled, busy ones spared
    await page.goto("/pools");
    await page.getByRole("button", { name: "Edit pool default-pool" }).click();
    await page.getByRole("button", { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole("button", { name: /Continue to Specifications/i }).click();
    await page.getByLabel("Runner Labels").fill("self-hosted,linux,busy-safe");
    await page.getByRole("button", { name: /Review & Confirm/i }).click();

    await expect(page.getByText(/will be recycled to apply the new configuration/i)).toBeVisible();
    await expect(page.getByText(/running jobs are not affected/i)).toBeVisible();

    await page.getByRole("button", { name: "Save Changes" }).click();
    await expect(page.getByText("Edit Runner Pool")).toBeHidden();

    // The busy runner survived the edit untouched; idle standbys respawn fresh
    await openDefaultPoolDetail(page);
    await expect
      .poll(
        async () => {
          const rows = await poolRunnerRows(page);
          return (
            rows.find((r) => r.name === busyRunner.name)?.state === "busy" &&
            rows.some((r) => r.state === "idle")
          );
        },
        { timeout: 60_000 },
      )
      .toBe(true);

    // Recycled standbys were replaced by new spawns (fresh names)
    for (const name of recycledNames) {
      await expect
        .poll(async () => (await poolRunnerRows(page)).some((r) => r.name === name), {
          timeout: 30_000,
        })
        .toBe(false);
    }

    // Cleanup: release the busy flag at the forge, then drop the mirror
    // registration. Releasing the flag leaves the pool briefly
    // over-provisioned — the busy-inclusive standby target kept four warm
    // runners while one was busy (standby backfill, docs/03 §3) — so the
    // excess-idle drain reclaims one idle runner on the next reconcile, and
    // the victim is arbitrary. Both outcomes are correct production
    // behavior: the runner is observed idle again or recycled as excess
    // (RUN-164).
    await registerRemoteRunner(request, busyRunner.name, false);
    await expect
      .poll(
        async () => {
          const row = (await poolRunnerRows(page)).find((r) => r.name === busyRunner.name);
          return row === undefined || row.state === "idle";
        },
        { timeout: 60_000 },
      )
      .toBe(true);
    await forgetRemoteRunner(request, busyRunner.name);
  });
});

// registerRemoteRunner mirrors a tracked runner in the mock provider's
// registered-runners registry (PUT /_admin/runners), optionally busy.
async function registerRemoteRunner(request: APIRequestContext, name: string, busy: boolean) {
  const response = await request.put(`${MOCK_PROVIDER_URL}/_admin/runners`, {
    data: { name, busy, status: "online" },
  });
  expect(response.ok(), `mock provider upsert for ${name}`).toBeTruthy();
}

// forgetRemoteRunner drops a mirrored registration (idempotent).
async function forgetRemoteRunner(request: APIRequestContext, name: string) {
  await request.delete(`${MOCK_PROVIDER_URL}/_admin/runners/${name}`);
}

// poolRunnerRows reads the pool-detail runners tab: one {name, state} per row.
async function poolRunnerRows(page: Page): Promise<Array<{ name: string; state: string }>> {
  return page.locator("table tbody tr").evaluateAll((rows) =>
    rows.map((row) => {
      const cells = row.querySelectorAll("td");
      return {
        name: (cells[1]?.textContent ?? "").trim(),
        state: (cells[2]?.textContent ?? "").trim(),
      };
    }),
  );
}

// openDefaultPoolDetail navigates to default-pool's detail page. The pools
// grid holds every pool's "View Pool Details" link, and other pools created
// in this flow (collision-pool) can precede default-pool, so the link must be
// scoped to default-pool's card.
async function openDefaultPoolDetail(page: Page) {
  await page
    .locator("div.group", { has: page.getByRole("heading", { name: "default-pool", exact: true }) })
    .getByRole("link", { name: /View Pool Details/i })
    .click();
}
