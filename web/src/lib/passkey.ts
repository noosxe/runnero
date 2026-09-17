/**
 * Passkey ceremony runners (RUN-248, docs/34 §3.3): drive the three steps of
 * a WebAuthn ceremony - server Begin, browser prompt, server Finish - and
 * return the Finish response. Kept free of React state so both the login
 * page and the Security tab share one implementation per path.
 */

import { authClient } from "./api/transport";
import type { LoginResponse, PasskeyInfo } from "../gen/api_pb";
import {
  parseCreationOptions,
  parseRequestOptions,
  serializeAssertionResponse,
  serializeCreationResponse,
} from "./webauthn";

/**
 * Passwordless login (docs/34 §3.2): the discoverable credential identifies
 * the user; a successful Finish sets the session cookie exactly like the
 * password path.
 */
export async function runPasskeyLogin(): Promise<LoginResponse> {
  const begin = await authClient.beginPasskeyLogin({});
  const credential = (await navigator.credentials.get({
    publicKey: parseRequestOptions(begin.publicKeyOptionsJson),
  })) as PublicKeyCredential | null;
  if (!credential) {
    throw new Error("Passkey prompt was dismissed");
  }
  return await authClient.finishPasskeyLogin({
    assertionResponseJson: new TextEncoder().encode(serializeAssertionResponse(credential)),
  });
}

/**
 * Enroll a passkey for the signed-in user. The current password is re-checked
 * server-side at Begin (docs/34 §4.2) - a stolen-but-live session must not
 * mint a new complete login identity.
 */
export async function runPasskeyEnrollment(
  currentPassword: string,
  name: string,
): Promise<PasskeyInfo> {
  const begin = await authClient.beginPasskeyEnrollment({ currentPassword });
  const credential = (await navigator.credentials.create({
    publicKey: parseCreationOptions(begin.publicKeyOptionsJson),
  })) as PublicKeyCredential | null;
  if (!credential) {
    throw new Error("Passkey prompt was dismissed");
  }
  const finish = await authClient.finishPasskeyEnrollment({
    attestationResponseJson: new TextEncoder().encode(serializeCreationResponse(credential)),
    name,
  });
  if (!finish.passkey) {
    throw new Error("Enrollment completed without a passkey payload");
  }
  return finish.passkey;
}
