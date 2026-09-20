import { test, expect } from "@playwright/test";

test.describe("E2E Infrastructure Smoke Test", () => {
  test("supervisor healthz responds successfully", async ({ request }) => {
    const response = await request.get("/healthz");
    expect(response.status()).toBe(200);
    const body = await response.json();
    expect(body.status).toBe("healthy");
  });

  test("supervisor root serves embedded SPA index page", async ({ page }) => {
    await page.goto("/");
    // Check that the root app shell is mounted. Since RUN-263 (docs/36
    // §5.1) every route sets its own "<Page> · Runnero" document title,
    // which overrides the static index.html title on any deep link.
    await expect(page).toHaveTitle(/AIO Supervisor|GitHub Runner|Login|Setup|· Runnero/i);
  });
});
