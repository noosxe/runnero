import crypto from "node:crypto";

import { type APIRequestContext, type Page } from "@playwright/test";

import { test, expect } from "../fixtures";

// Flow 14: Job execution history lifecycle (docs/21, RUN-253).
//
// The only spec that drives a real job through its full lifecycle: signed
// workflow_job webhooks (queued -> in_progress -> completed) delivered to
// the supervisor's /hooks/github receiver, a warm pool runner taking the
// job, and the /history surface rendering the resulting row and its logs.
// This is the seam whose absence let the RUN-252 log-resolution bug ship
// invisible to CI — every /history assertion below resolves logs BY RUNNER
// NAME, exactly the lookup that 404'd when the resolver only matched
// container ids.

// The mock forge has no webhook fan-out, so the spec plays GitHub itself:
// it HMAC-signs each event with the shared secret the compose file
// configures (SUPERVISOR_WEBHOOK_GITHUB_SECRET) and POSTs it to the
// receiver. The receiver is only mounted when a secret is configured, so a
// missing secret fails loudly here with a connection refused / 405.
const WEBHOOK_SECRET = process.env.WEBHOOK_SECRET ?? "runnero-e2e-webhook-secret";
const MOCK_DOCKER_URL = process.env.MOCK_DOCKER_URL ?? "http://e2e-mock-docker:2375";

// The spec drives events against its own pool, isolated from the pools
// earlier flows leave behind (default-pool, collision-pool — all targeting
// the same mock repos, so repo matching alone cannot disambiguate). The
// unique jobflow label is the discriminator: MatchPoolForEvent requires the
// pool's label contract to cover every job label (docs/24 §5.2), and no
// other pool carries it.
const POOL_NAME = "jobflow-pool";
const JOB_LABELS = ["jobflow"];
const REPO_FULL_NAME = "test-org/test-repo";
const REPO_HTML_URL = "https://github.com/test-org/test-repo";

// postWorkflowJobEvent signs the payload and POSTs it to the supervisor's
// receiver, asserting the documented 202 fast-ack (docs/03 §4).
async function postWorkflowJobEvent(
  request: APIRequestContext,
  action: string,
  event: object,
): Promise<void> {
  const body = JSON.stringify(event);
  const signature = crypto.createHmac("sha256", WEBHOOK_SECRET).update(body).digest("hex");
  const response = await request.post("/hooks/github", {
    headers: {
      "content-type": "application/json",
      "X-Hub-Signature-256": `sha256=${signature}`,
      "X-GitHub-Event": "workflow_job",
    },
    data: body,
  });
  expect(response.status(), `webhook ${action} fast-acked`).toBe(202);
}

// buildJobEvent assembles a GitHub workflow_job webhook payload. The
// RFC3339 timestamps are spaced exactly 5s apart, so the history row's
// derived queue wait (started - queued) and duration (completed - started)
// render as deterministic "5.0s" / "5s" values (computed server-side from
// the stored timestamps, internal/server/analytics.go).
function buildJobEvent(
  action: string,
  jobId: number,
  runnerName: string,
  queuedAt: Date,
  startedAt: Date,
  completedAt?: Date,
) {
  return {
    action,
    workflow_job: {
      id: jobId,
      run_id: jobId + 1,
      workflow_name: "E2E job flow",
      head_branch: "main",
      head_sha: "e2e0feedc0de",
      status: action,
      labels: JOB_LABELS,
      created_at: queuedAt.toISOString(),
      started_at: startedAt.toISOString(),
      completed_at: completedAt?.toISOString(),
      conclusion: action === "completed" ? "success" : undefined,
      runner_name: runnerName,
    },
    repository: {
      id: 1001,
      name: "test-repo",
      full_name: REPO_FULL_NAME,
      html_url: REPO_HTML_URL,
      clone_url: `${REPO_HTML_URL}.git`,
    },
    sender: { id: 1, login: "e2e-user" },
  };
}

// poolRunnerRows reads the pool-detail runners tab: one {name, state} per
// row. Same reader as flow 08 (tests/e2e/specs/08-pool-edit-workflow.spec.ts).
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

// exitMockContainer makes the mock container exit itself (RUN-165): after
// the forge releases the busy flag, an ephemeral runner exits — the
// supervisor reaps it through the die-event path, capturing its output to
// the log archive. Same helper shape as flow 08.
async function exitMockContainer(request: APIRequestContext, name: string): Promise<void> {
  const response = await request.post(`${MOCK_DOCKER_URL}/_admin/containers/${name}/exit`, {
    data: { exitCode: 0 },
  });
  expect(response.ok(), `mock docker exit for ${name}`).toBeTruthy();
}

test.describe("Flow 14: Job Execution History", () => {
  test("drives a webhook job to completion and renders its history row and logs", async ({
    onboardedPage: page,
    request,
  }) => {
    // Webhook handling is fast, but UI polling cadence (busy flip, history
    // refetch) and warm-runner readiness exceed the 30s default.
    test.setTimeout(120_000);

    // Create the spec's own pool (unique jobflow label) and wait for its warm
    // standby — the runner that takes the job. Read its name from the pool
    // detail table: a queued event covered by an idle runner spawns nothing
    // (warm-first, docs/03 §4), so this name is stable through the flow.
    await page.goto("/pools");
    await page.getByRole("button", { name: "+ Add Runner Pool" }).click();
    await page.getByLabel("Pool Name (Slug)").fill(POOL_NAME);
    await page.getByRole("button", { name: /Continue to Scope & Targets/i }).click();
    await page.getByRole("button", { name: "Select All Filtered" }).click();
    await page.getByRole("button", { name: /Continue to Specifications/i }).click();
    // 2 = one warm standby per selected target: the spread matches the
    // warm-pool target exactly, so the excess-idle drain cannot reap the
    // picked runner between selection and the webhook events.
    await page.getByLabel("Min Idle Warm Runners").fill("2");
    await page.getByLabel("Runner Labels").fill("self-hosted,linux,jobflow");
    await page.getByRole("button", { name: /Review & Confirm/i }).click();
    await page.getByRole("button", { name: /Create Runner Pool/i }).click();
    await expect(page.getByText("Create Runner Pool Wizard")).toBeHidden();

    await page
      .getByTestId(`pool-card-${POOL_NAME}`)
      .getByRole("link", { name: /View Pool Details/i })
      .click();
    await expect
      .poll(async () => (await poolRunnerRows(page)).filter((r) => r.state === "idle").length, {
        timeout: 45_000,
      })
      .toBeGreaterThanOrEqual(1);
    const idle = (await poolRunnerRows(page)).find((r) => r.state === "idle");
    if (!idle) throw new Error("no idle warm runner available to take the job");
    const runnerName = idle.name;

    // One forge job id per suite run: history rows upsert on (pool, job id),
    // and the anonymous-volume DB starts empty each run (RUN-166).
    const jobId = 77_000_000_000 + (Date.now() % 100_000_000);
    const queuedAt = new Date(Date.now() - 10_000);
    const startedAt = new Date(Date.now() - 5_000);

    // --- queued: the job row is booked against the matched pool. --------
    await postWorkflowJobEvent(
      request,
      "queued",
      buildJobEvent("queued", jobId, runnerName, queuedAt, startedAt),
    );

    await page.goto("/history");
    // The queued stub carries no runner name yet (RecordWebhookQueued stores
    // only the forge job identity — the runner is assigned at in_progress),
    // so the row is located by its status badge here.
    const queuedRow = page
      .getByRole("row")
      .filter({ has: page.getByText("queued", { exact: true }) })
      .first();
    await expect(queuedRow, "queued job row appears in history").toBeVisible({
      timeout: 15_000,
    });

    // --- in_progress: the forge assigned the job to the runner. ---------
    // The webhook fast path flips the tracked runner's busy flag (docs/19)
    // and marks the row running.
    await postWorkflowJobEvent(
      request,
      "in_progress",
      buildJobEvent("in_progress", jobId, runnerName, queuedAt, startedAt),
    );

    await page.goto("/pools");
    await page
      .getByTestId(`pool-card-${POOL_NAME}`)
      .getByRole("link", { name: /View Pool Details/i })
      .click();
    await expect
      .poll(
        async () =>
          (await poolRunnerRows(page)).find((r) => r.name === runnerName)?.state === "busy",
        { timeout: 30_000 },
      )
      .toBe(true);

    await page.goto("/history");
    // From in_progress on, the row carries the runner name — locate it by
    // name for the running/success assertions and the detail-page jump.
    const historyRow = page.getByRole("row").filter({ hasText: runnerName }).first();
    await expect(historyRow.getByText("running", { exact: true })).toBeVisible({
      timeout: 15_000,
    });

    // --- completed: forge reports success. -------------------------------
    await postWorkflowJobEvent(
      request,
      "completed",
      // completed_at is forge-provided: derive it from startedAt (+5s) so
      // the row's duration renders as exactly "5s" regardless of when the
      // event actually goes out.
      buildJobEvent(
        "completed",
        jobId,
        runnerName,
        queuedAt,
        startedAt,
        new Date(startedAt.getTime() + 5_000),
      ),
    );

    await exitMockContainer(request, runnerName);
    // The reap (capture -> removal record -> untrack) runs on the die
    // event; the capture and removal record are written before the runner
    // is untracked, so the runner row disappearing from the pool page
    // proves the archive the by-name lookups below resolve exists.
    await page.goto("/pools");
    await page
      .getByTestId(`pool-card-${POOL_NAME}`)
      .getByRole("link", { name: /View Pool Details/i })
      .click();
    await expect
      .poll(async () => (await poolRunnerRows(page)).some((r) => r.name === runnerName), {
        timeout: 30_000,
      })
      .toBe(false);

    await page.goto("/history");
    await expect(historyRow.getByText("success", { exact: true })).toBeVisible({
      timeout: 15_000,
    });
    // Forge timestamp enrichment (docs/21 §5.5): queue wait = started -
    // queued, duration = completed - started, both exactly 5s by payload.
    await expect(historyRow.getByText("5.0s", { exact: true })).toBeVisible();
    await expect(historyRow.getByText("5s", { exact: true })).toBeVisible();

    // --- detail page: archived log terminal resolved BY RUNNER NAME. -----
    // The row action renders with button ARIA semantics (Base UI Button +
    // render-prop anchor), so query by button role despite the anchor DOM.
    await historyRow.getByRole("button", { name: "Logs" }).click();
    await expect(page.getByRole("heading", { name: runnerName })).toBeVisible();
    await expect(page.getByText("success", { exact: true })).toBeVisible();
    await expect(page.getByText("Historical Archive")).toBeVisible();
    // The mock docker serves the runner's captured output as stdcopy
    // frames; the terminal must render it (the RUN-252 regression shape:
    // this lookup 404'd for every name-keyed request before the fix).
    await expect(page.getByText("[stdout]").first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("Listening for Jobs.").first()).toBeVisible();

    // --- /logs Runners tab: first by-NAME lookup driven from the UI. -----
    await page.goto("/logs/runners");
    await page.getByTestId("logs-runner-input").fill(runnerName);
    await page.getByTestId("logs-runner-load").click();
    await expect(page.getByText("Historical Archive")).toBeVisible();
    await expect(page.getByText("Listening for Jobs.").first()).toBeVisible({
      timeout: 15_000,
    });
  });
});
