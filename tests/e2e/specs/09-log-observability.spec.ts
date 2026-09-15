import { test, expect } from "../fixtures";

// Flow 09: Log observability UI (docs/29, RUN-219).
//
// Exercises the /logs page against the real supervisor state: the boot file
// table (the supervisor always writes its own boot log), a live follow on the
// current boot, and a genuine removal record produced by terminating a runner
// through the pool detail UI (mock docker serves the capture logs).

test.describe("Flow 09: Log Observability", () => {
  test("lists supervisor boot files and streams the current boot", async ({
    onboardedPage: page,
  }) => {
    await page.goto("/logs");

    // Supervisor tab is the default and lists at least the running boot.
    await expect(page.getByRole("heading", { name: "Logs" })).toBeVisible();
    const bootRows = page.getByTestId("logs-boot-row");
    await expect(bootRows.first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("logs-boot-current-badge").first()).toBeVisible();

    // Open the current boot; the viewer offers Follow only for the current boot.
    await bootRows.first().click();
    const followToggle = page.getByTestId("logs-follow-toggle");
    await expect(followToggle).toBeVisible();

    // Supervisor boot log content renders in the terminal.
    await expect(page.getByText("Supervisor Boot Log").first()).toBeVisible();
    await expect(page.getByText("[stdout]").first()).toBeVisible();

    // Follow connects live against the running supervisor.
    await followToggle.click();
    await expect(followToggle).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByText(/Live Stream/i)).toBeVisible({ timeout: 10_000 });
    await followToggle.click();
  });

  test("terminating a runner produces a removal record with capture navigation", async ({
    onboardedPage: page,
  }) => {
    // Drive a real removal through the pool detail UI so the record, its
    // capture, and its boot id are produced by the production code path.
    await page.goto("/pools");

    // Pool cards are not links; the card's "View Pool Details" link navigates
    // to the detail page. Select the card whose heading is default-pool.
    const poolCard = page
      .locator("div")
      .filter({ has: page.getByRole("heading", { name: "default-pool", exact: true }) })
      .filter({ has: page.getByRole("link", { name: "View Pool Details" }) })
      .last();
    await poolCard.getByRole("link", { name: "View Pool Details" }).click();

    // Warm-pool target spawns at least one idle runner; poll for it.
    const runnerActions = page.getByLabel("Runner actions").first();
    await expect(runnerActions).toBeVisible({ timeout: 45_000 });

    await runnerActions.click();
    await page.getByRole("menuitem", { name: "Terminate" }).click();
    await page.getByRole("button", { name: "Terminate Instance" }).click();
    await expect(page.getByRole("button", { name: "Terminate Instance" })).toBeHidden({
      timeout: 15_000,
    });

    // The removal record appears on the /logs removals tab. Filter to
    // reason=manual before asserting: the die-event path can append a
    // second "reap" record for the same container milliseconds after the
    // manual terminate (RUN-224), and the list is newest-first, so the
    // unfiltered first row may be that reap record.
    await page.goto("/logs?tab=removals");
    await page.getByTestId("logs-filter-reason").click();
    await page.getByRole("option", { name: "manual", exact: true }).click();
    await page.getByTestId("logs-filter-apply").click();
    const removalRow = page.getByTestId("logs-removal-row").first();
    await expect(removalRow).toBeVisible({ timeout: 15_000 });
    await expect(removalRow.getByTestId("logs-reason-badge")).toHaveText("manual", {
      timeout: 15_000,
    });

    // Capture outcome is environment-dependent (the mock docker holds the log
    // stream open, so idle-runner captures may hit their hard deadline and
    // render the "none" badge); both outcomes render, so only assert the row
    // resolved its capture column.
    await expect(removalRow.getByText(/KiB|B|none/).first()).toBeVisible();

    // Row action: jump to the runner capture, prefilled via deep link.
    await removalRow.getByTestId("logs-removal-view-capture").click();
    await expect(page.getByTestId("logs-tab-panel-runners")).toBeVisible();
    await expect(page.getByTestId("logs-runner-input")).not.toHaveValue("");
    await expect(page.getByText("Historical Archive")).toBeVisible();

    // Row action: jump back to the boot that wrote the record. Re-apply the
    // manual filter: navigation resets it, and the unfiltered first row can
    // again be the die-event reap record (RUN-224).
    await page.goto("/logs?tab=removals");
    await page.getByTestId("logs-filter-reason").click();
    await page.getByRole("option", { name: "manual", exact: true }).click();
    await page.getByTestId("logs-filter-apply").click();
    const removalRowAgain = page.getByTestId("logs-removal-row").first();
    await expect(removalRowAgain).toBeVisible({ timeout: 15_000 });
    await removalRowAgain.getByTestId("logs-removal-view-boot").click();
    await expect(page.getByTestId("logs-tab-panel-supervisor")).toBeVisible();
    await expect(page.getByText("Supervisor Boot Log").first()).toBeVisible();

    // Filters narrow the removal list without clearing the record.
    await page.goto("/logs?tab=removals");
    await page.getByTestId("logs-filter-reason").click();
    await page.getByRole("option", { name: "manual", exact: true }).click();
    await page.getByTestId("logs-filter-apply").click();
    await expect(page.getByTestId("logs-removal-row").first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("logs-reason-badge").first()).toHaveText("manual");
  });
});
