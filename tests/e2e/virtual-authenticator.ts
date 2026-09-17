import type { CDPSession, Page } from "@playwright/test";

/**
 * Attach a Chromium virtual authenticator to `page` via CDP (RUN-248,
 * docs/34 §12.2): a ctap2 authenticator with resident-key storage and
 * automatic user presence + user verification, so passkey ceremonies run
 * unattended inside the E2E browser.
 *
 * The authenticator belongs to the browser context, and discoverable
 * credentials live inside it: a spec that enrolls a passkey and then logs
 * in with it must attach exactly one authenticator BEFORE enrollment and
 * keep using the same context for the assertion.
 */
export async function attachVirtualAuthenticator(page: Page): Promise<CDPSession> {
  const client = await page.context().newCDPSession(page);
  await client.send("WebAuthn.enable");
  await client.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2",
      transport: "internal",
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });
  return client;
}
