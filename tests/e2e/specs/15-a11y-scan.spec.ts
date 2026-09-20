import AxeBuilder from "@axe-core/playwright";
import type { Page, TestInfo } from "@playwright/test";
import { test, expect } from "../fixtures";

// Flow 15: Accessibility baseline scan (RUN-262, docs/36 §4). axe-core runs
// across the route × role × theme matrix and REPORTS violations into the
// HTML report without failing the suite — enforcement flips in phase 3
// (RUN-265, docs/36 §6). The only hard assertions here are matrix-drift
// guards: every URL in the matrix must actually produce a scan (or a
// recorded, justified skip).
//
// Runs after flow 13/14 alphabetically: the suite database then already has
// the e2e-viewer account and at least one completed job, so the viewer and
// history-detail scans can reuse those artifacts. ensureViewer re-creates
// the account if the database was seeded without flow 13.

const VIEWER_USERNAME = "e2e-viewer";
const VIEWER_PASSWORD = "e2e-viewer-password-1";

// Theme is pinned by seeding the storage key use-theme reads at boot
// (web/src/hooks/use-theme.ts) — no reliance on OS preference.
type Theme = "light" | "dark";

type ScanTarget = {
  name: string;
  // Navigates to the page under scan and returns the URL that was scanned.
  goto: (page: Page) => Promise<string>;
  // Resolves when the route's data has rendered (axe must scan content,
  // not skeletons). Streaming continues in the background on some routes;
  // that is accepted for the baseline.
  ready: (page: Page) => Promise<void>;
};

type ScanResult = {
  name: string;
  url: string;
  status: "scanned" | "skipped";
  reason?: string;
  violations: Array<{
    id: string;
    impact: string | null;
    nodes: number;
    help: string;
    // Up to 5 axe node targets (CSS-ish selectors) so triage can pinpoint
    // the offending elements straight from the report.
    targets: string[];
  }>;
};

async function scanPage(page: Page, target: ScanTarget, result: ScanResult): Promise<void> {
  const url = await target.goto(page);
  await target.ready(page);

  const builder = new AxeBuilder({ page }).withTags([
    "wcag2a",
    "wcag2aa",
    "wcag22aa",
    "best-practice",
  ]);
  const results = await builder.analyze();

  result.url = url;
  result.status = "scanned";
  result.violations = results.violations.map((v) => ({
    id: v.id,
    impact: v.impact ?? null,
    nodes: v.nodes.length,
    help: v.help,
    targets: v.nodes.slice(0, 5).map((n) => n.target.join(" ")),
  }));
}

function summarize(results: ScanResult[]): string {
  const lines: string[] = [];
  let totalViolations = 0;
  let totalNodes = 0;
  for (const r of results) {
    if (r.status === "skipped") {
      lines.push(`  SKIP ${r.name}: ${r.reason}`);
      continue;
    }
    const count = (impact: string) => r.violations.filter((v) => v.impact === impact).length;
    totalViolations += r.violations.length;
    totalNodes += r.violations.reduce((acc, v) => acc + v.nodes, 0);
    lines.push(
      `  ${r.url} → ${r.violations.length} violation(s) ` +
        `(critical ${count("critical")}, serious ${count("serious")}, moderate ${count("moderate")}, minor ${count("minor")})`,
    );
    for (const v of r.violations) {
      lines.push(`      [${v.impact ?? "?"}] ${v.id} ×${v.nodes}: ${v.help}`);
      for (const t of v.targets) {
        lines.push(`          ↳ ${t}`);
      }
    }
  }
  lines.unshift(
    `axe scan summary: ${results.filter((r) => r.status === "scanned").length} page(s) scanned, ` +
      `${totalViolations} violation rule(s) across ${totalNodes} node(s)`,
  );
  return lines.join("\n");
}

async function attachResults(
  testInfo: TestInfo,
  label: string,
  results: ScanResult[],
): Promise<void> {
  const summary = summarize(results);
  console.log(`\n[axe ${label}]\n${summary}`);
  await testInfo.attach(`axe-${label}`, {
    contentType: "text/plain",
    body: summary,
  });
  await testInfo.attach(`axe-${label}-raw`, {
    contentType: "application/json",
    body: JSON.stringify(results, null, 2),
  });
}

// ---- dynamic route resolution -------------------------------------------

// Resolves a pool detail URL by following the pools page's card link, the
// same affordance users take (flows 08/10 use the card, not a literal id).
async function gotoPoolDetail(page: Page): Promise<string> {
  await page.goto("/pools");
  await expect(page.getByRole("heading", { name: "Runner Pools" })).toBeVisible();
  const detailLink = page.locator('a[href^="/pools/"]').first();
  await detailLink.click();
  await page.waitForURL(/\/pools\/\d+/);
  const url = page.url();
  await page.goto(url); // normalize: scan a fresh load of the deep link
  return new URL(url).pathname;
}

// Resolves a job detail URL by following the history table's first row
// detail link (flow 14 guarantees ≥1 completed job runs before this spec).
async function gotoHistoryDetail(page: Page): Promise<string> {
  await page.goto("/history");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  const detailLink = page.locator('a[href*="/history/"]').first();
  await expect(detailLink).toBeVisible();
  const href = await detailLink.getAttribute("href");
  await page.goto(href!); // scan a fresh load of the deep link
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  return href!;
}

// Same resolution as gotoPoolDetail, then deep-links the Config tab
// (docs/36 §4.1 matrix: the pool detail URL-state tabs each render distinct
// content; the default tab is covered by the plain pool-detail scan).
async function gotoPoolDetailConfig(page: Page): Promise<string> {
  await page.goto("/pools");
  await expect(page.getByRole("heading", { name: "Runner Pools" })).toBeVisible();
  const detailLink = page.locator('a[href^="/pools/"]').first();
  await detailLink.click();
  await page.waitForURL(/\/pools\/\d+/);
  const path = new URL(page.url()).pathname;
  await page.goto(`${path}?tab=config`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  return `${path}?tab=config`;
}

// The onboarding wizard is not renderable on a seeded stack (the database
// is onboarded; /onboarding redirects to /). The scan records a justified
// skip instead of silently dropping the matrix entry (docs/36 §9 drift
// guard). The wizard surfaces get their coverage from the manual keyboard
// pass (docs/36 §4.2) and flows 01/02.
async function gotoOnboarding(page: Page): Promise<{ url: string; skipped: boolean }> {
  await page.goto("/onboarding");
  await page.waitForURL((u) => !u.pathname.includes("/onboarding"));
  return { url: "/onboarding", skipped: true };
}

// ---- scan matrices (docs/36 §4.1) ----------------------------------------

const headingReady =
  (name?: string | RegExp) =>
  async (page: Page): Promise<void> => {
    // .first(): strict mode — some pages legitimately carry multiple h1s.
    const h1 = page.getByRole("heading", { level: 1 }).first();
    if (name === undefined) {
      await expect(h1).toBeVisible();
      return;
    }
    await expect(page.getByRole("heading", { name, level: 1 }).first()).toBeVisible();
  };

const ADMIN_SCANS: ScanTarget[] = [
  {
    name: "dashboard",
    goto: async (p) => (await p.goto("/"), "/"),
    ready: headingReady("Dashboard Overview"),
  },
  {
    name: "pools",
    goto: async (p) => (await p.goto("/pools"), "/pools"),
    ready: headingReady("Runner Pools"),
  },
  { name: "pool-detail", goto: gotoPoolDetail, ready: headingReady() },
  { name: "pool-detail-config", goto: gotoPoolDetailConfig, ready: headingReady() },
  {
    name: "logs",
    goto: async (p) => (await p.goto("/logs"), "/logs"),
    ready: headingReady("Logs"),
  },
  {
    name: "history",
    goto: async (p) => (await p.goto("/history"), "/history"),
    ready: headingReady(),
  },
  { name: "history-detail", goto: gotoHistoryDetail, ready: headingReady() },
  {
    name: "profiles",
    goto: async (p) => (await p.goto("/profiles"), "/profiles"),
    ready: headingReady(),
  },
  {
    name: "renovate",
    goto: async (p) => (await p.goto("/renovate"), "/renovate"),
    ready: headingReady(),
  },
  // Admin's default tab is Constraints (docs/09 §2.2).
  {
    name: "settings-default",
    goto: async (p) => (await p.goto("/settings"), "/settings"),
    ready: headingReady(),
  },
  {
    name: "settings-users",
    goto: async (p) => (await p.goto("/settings?tab=users"), "/settings?tab=users"),
    ready: async (p) => {
      await expect(p.getByTestId("users-card")).toBeVisible();
    },
  },
  {
    name: "onboarding",
    goto: async (p) => {
      const r = await gotoOnboarding(p);
      return r.url;
    },
    ready: async () => {
      // never reached: gotoOnboarding always records a skip
    },
  },
];

const VIEWER_SCANS: ScanTarget[] = [
  {
    name: "dashboard",
    goto: async (p) => (await p.goto("/"), "/"),
    ready: headingReady("Dashboard Overview"),
  },
  {
    name: "pools",
    goto: async (p) => (await p.goto("/pools"), "/pools"),
    ready: headingReady("Runner Pools"),
  },
  { name: "pool-detail", goto: gotoPoolDetail, ready: headingReady() },
  {
    name: "logs",
    goto: async (p) => (await p.goto("/logs"), "/logs"),
    ready: headingReady("Logs"),
  },
  {
    name: "history",
    goto: async (p) => (await p.goto("/history"), "/history"),
    ready: headingReady(),
  },
  { name: "history-detail", goto: gotoHistoryDetail, ready: headingReady() },
  {
    name: "renovate",
    goto: async (p) => (await p.goto("/renovate"), "/renovate"),
    ready: headingReady(),
  },
  // Viewer's settings page is the Security tab only (docs/35 §2.4).
  {
    name: "settings-default",
    goto: async (p) => (await p.goto("/settings"), "/settings"),
    ready: headingReady(),
  },
];

const UNAUTH_SCANS: ScanTarget[] = [
  {
    name: "login",
    goto: async (p) => (await p.goto("/login"), "/login"),
    ready: async (p) => {
      await expect(p.getByRole("button", { name: "Sign In", exact: true })).toBeVisible();
    },
  },
];

// ---- helper: viewer session ----------------------------------------------

// The onboardedPage fixture leaves an active admin session in this fresh
// context — go straight to the admin surface. Calling login() here would
// race the client-side /login redirect (fixtures.ts documents login() as an
// unauthenticated-page helper: the redirect fires after goto resolves, so
// the URL still contains /login when the helper checks it).
async function ensureViewerSession(page: Page): Promise<void> {
  await page.goto("/settings?tab=users");
  await expect(page.getByTestId("users-card")).toBeVisible();
  const viewerRow = page
    .getByTestId("users-card")
    .getByRole("row", { name: new RegExp(VIEWER_USERNAME) });
  if (!(await viewerRow.isVisible())) {
    await page.getByTestId("add-user-button").click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Username").fill(VIEWER_USERNAME);
    await dialog.getByLabel("Initial password").fill(VIEWER_PASSWORD);
    await dialog.getByLabel("Confirm password").fill(VIEWER_PASSWORD);
    await page.getByTestId("add-user-submit").click();
    await expect(viewerRow).toBeVisible();
  }

  // Fresh session as the viewer.
  await page.getByText("Supervisor Admin").first().click();
  await page.getByRole("menuitem", { name: /Sign Out/i }).click();
  await page.waitForURL(/login/);
  await page.getByLabel("Username").fill(VIEWER_USERNAME);
  await page.getByRole("textbox", { name: "Password" }).fill(VIEWER_PASSWORD);
  await page.getByRole("button", { name: "Sign In", exact: true }).click();
  await page.waitForURL((url) => !url.pathname.includes("/login"));
}

// ---- the matrix -----------------------------------------------------------

async function runMatrix(
  page: Page,
  testInfo: TestInfo,
  theme: Theme,
  label: string,
  targets: ScanTarget[],
): Promise<void> {
  await page.addInitScript((t) => localStorage.setItem("runnero-theme", t), theme);

  const results: ScanResult[] = targets.map((t) => ({
    name: t.name,
    url: "",
    status: "skipped",
    reason: "not run",
    violations: [],
  }));

  for (let i = 0; i < targets.length; i++) {
    const target = targets[i];
    // The onboarding target resolves to a documented skip on a seeded
    // stack; everything else must produce a real scan (matrix-drift guard).
    if (target.name === "onboarding") {
      const r = await gotoOnboarding(page);
      results[i] = {
        name: target.name,
        url: r.url,
        status: "skipped",
        reason:
          "system is onboarded; the wizard is not renderable on a seeded database (coverage via manual keyboard pass, docs/36 §4.2)",
        violations: [],
      };
      continue;
    }
    const result: ScanResult = {
      name: target.name,
      url: "",
      status: "skipped",
      reason: "not run",
      violations: [],
    };
    await scanPage(page, target, result);
    results[i] = result;
  }

  // Matrix-drift guard: every entry scanned or recorded-skip, never dropped.
  expect(
    results.every(
      (r) => r.status === "scanned" || (r.status === "skipped" && r.reason !== "not run"),
    ),
  ).toBe(true);

  await attachResults(testInfo, `${label}-${theme}`, results);
}

for (const theme of ["light", "dark"] as const) {
  test(`a11y scan: unauthenticated pages (${theme})`, async ({ page }, testInfo) => {
    await runMatrix(page, testInfo, theme, "unauth", UNAUTH_SCANS);
  });

  test(`a11y scan: admin pages (${theme})`, async ({ onboardedPage }, testInfo) => {
    await runMatrix(onboardedPage, testInfo, theme, "admin", ADMIN_SCANS);
  });

  test(`a11y scan: viewer pages (${theme})`, async ({ onboardedPage }, testInfo) => {
    await ensureViewerSession(onboardedPage);
    await runMatrix(onboardedPage, testInfo, theme, "viewer", VIEWER_SCANS);
  });
}
