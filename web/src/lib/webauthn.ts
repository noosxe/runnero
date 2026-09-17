/**
 * Browser ↔ server WebAuthn bridge (RUN-248, docs/34 §7).
 *
 * The backend crosses the wire with bytes holding the go-webauthn library's
 * canonical JSON shapes: `{"publicKey": {...}}` ceremony options outbound and
 * the browser-shaped `{id, rawId, type, response: {...}}` registration /
 * assertion payloads inbound, with every binary field base64url-encoded.
 * These helpers convert between those JSON shapes and the DOM
 * `PublicKeyCredential` API the browser speaks.
 *
 * Nothing here interprets credential material - it only re-encodes opaque
 * buffers between the two encodings.
 */

/** base64url (RFC 4648 §5, unpadded) → bytes. */
export function b64uDecode(s: string): Uint8Array<ArrayBuffer> {
  const padded = s.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(padded + "=".repeat((4 - (padded.length % 4)) % 4));
  const out = new Uint8Array(new ArrayBuffer(binary.length));
  for (let i = 0; i < binary.length; i++) {
    out[i] = binary.charCodeAt(i);
  }
  return out;
}

/** bytes → base64url (unpadded). */
export function b64uEncode(buf: ArrayBuffer | Uint8Array): string {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf);
  let binary = "";
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function utf8(json: Uint8Array): string {
  return new TextDecoder().decode(json);
}

interface RawCreationOptions {
  rp: { id?: string; name?: string };
  user: { id: string; name: string; displayName?: string };
  challenge: string;
  pubKeyCredParams: PubKeyCredParam[];
  timeout?: number;
  excludeCredentials?: { id: string; type?: string; transports?: string[] }[];
  authenticatorSelection?: AuthenticatorSelectionCriteriaJSON;
  attestation?: string;
}

interface PubKeyCredParam {
  type: string;
  alg: number;
}

interface AuthenticatorSelectionCriteriaJSON {
  residentKey?: string;
  requireResidentKey?: boolean;
  userVerification?: string;
}

/**
 * Parse the server's CredentialCreation JSON into DOM creation options
 * (decoding every base64url buffer the DOM API expects).
 */
export function parseCreationOptions(json: Uint8Array): PublicKeyCredentialCreationOptions {
  const raw = JSON.parse(utf8(json)) as { publicKey: RawCreationOptions };
  const pk = raw.publicKey;
  return {
    rp: pk.rp,
    user: {
      id: b64uDecode(pk.user.id),
      name: pk.user.name,
      displayName: pk.user.displayName ?? pk.user.name,
    },
    challenge: b64uDecode(pk.challenge),
    pubKeyCredParams: pk.pubKeyCredParams,
    timeout: pk.timeout,
    excludeCredentials: pk.excludeCredentials?.map((c): PublicKeyCredentialDescriptor => ({
      id: b64uDecode(c.id),
      type: "public-key" as const,
    })),
    authenticatorSelection: {
      residentKey: pk.authenticatorSelection?.residentKey,
      requireResidentKey: pk.authenticatorSelection?.requireResidentKey,
      userVerification: pk.authenticatorSelection?.userVerification,
    },
    attestation: pk.attestation,
  } as PublicKeyCredentialCreationOptions;
}

interface RawRequestOptions {
  rpId?: string;
  challenge: string;
  timeout?: number;
  userVerification?: string;
  allowCredentials?: { id: string; type?: string; transports?: string[] }[];
}

/**
 * Parse the server's CredentialAssertion JSON into DOM request options. The
 * allow-list is empty for discoverable logins (docs/34 §3.2) - the ceremony
 * runs username-less.
 */
export function parseRequestOptions(json: Uint8Array): PublicKeyCredentialRequestOptions {
  const raw = JSON.parse(utf8(json)) as { publicKey: RawRequestOptions };
  const pk = raw.publicKey;
  return {
    challenge: b64uDecode(pk.challenge),
    rpId: pk.rpId,
    timeout: pk.timeout,
    userVerification: (pk.userVerification ?? "required") as UserVerificationRequirement,
    allowCredentials: pk.allowCredentials?.map((c): PublicKeyCredentialDescriptor => ({
      id: b64uDecode(c.id),
      type: "public-key" as const,
    })),
  };
}

function clientExtensionJSON(cred: PublicKeyCredential): Record<string, unknown> {
  const ext = cred.getClientExtensionResults();
  return JSON.parse(JSON.stringify(ext)) as Record<string, unknown>;
}

/**
 * Serialize a registration PublicKeyCredential into the
 * protocol.CredentialCreationResponse JSON go-webauthn parses.
 */
export function serializeCreationResponse(cred: PublicKeyCredential): string {
  const response = cred.response as AuthenticatorAttestationResponse;
  return JSON.stringify({
    id: cred.id,
    rawId: b64uEncode(cred.rawId),
    type: cred.type,
    clientExtensionResults: clientExtensionJSON(cred),
    response: {
      clientDataJSON: b64uEncode(response.clientDataJSON),
      attestationObject: b64uEncode(response.attestationObject),
      transports: response.getTransports?.() ?? [],
    },
  });
}

/**
 * Serialize an authentication PublicKeyCredential into the
 * protocol.CredentialAssertionResponse JSON go-webauthn parses.
 */
export function serializeAssertionResponse(cred: PublicKeyCredential): string {
  const response = cred.response as AuthenticatorAssertionResponse;
  const userHandle = response.userHandle;
  return JSON.stringify({
    id: cred.id,
    rawId: b64uEncode(cred.rawId),
    type: cred.type,
    clientExtensionResults: clientExtensionJSON(cred),
    response: {
      authenticatorData: b64uEncode(response.authenticatorData),
      clientDataJSON: b64uEncode(response.clientDataJSON),
      signature: b64uEncode(response.signature),
      userHandle: userHandle && userHandle.byteLength > 0 ? b64uEncode(userHandle) : "",
    },
  });
}
