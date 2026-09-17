import { describe, it, expect } from "vitest";

import {
  b64uDecode,
  b64uEncode,
  parseCreationOptions,
  parseRequestOptions,
  serializeAssertionResponse,
  serializeCreationResponse,
} from "./webauthn";

/** JSON with binary fields shaped exactly like go-webauthn's protocol types. */
const CREATION_JSON = JSON.stringify({
  publicKey: {
    rp: { id: "localhost", name: "Runnero" },
    user: {
      id: "AAAAAAAAAAAAAAAAAAAABw", // 15 zero bytes + 0x07 (admin id 7, BE int64)
      name: "admin",
      displayName: "admin",
    },
    challenge: "AQIDBAUGBwgJCgsMDQ4PEA",
    pubKeyCredParams: [
      { type: "public-key", alg: -8 },
      { type: "public-key", alg: -7 },
      { type: "public-key", alg: -257 },
    ],
    timeout: 60000,
    authenticatorSelection: {
      residentKey: "required",
      requireResidentKey: true,
      userVerification: "required",
    },
    attestation: "none",
  },
});

const REQUEST_JSON = JSON.stringify({
  publicKey: {
    challenge: "AQIDBAUGBwgJCgsMDQ4PEA",
    timeout: 60000,
    rpId: "localhost",
    userVerification: "required",
  },
});

function fakeCreationCredential() {
  return {
    id: "Y3JlZC1pZA",
    rawId: new Uint8Array([1, 2, 3, 4]).buffer,
    type: "public-key",
    getClientExtensionResults: () => ({}),
    response: {
      clientDataJSON: new Uint8Array([9, 9]).buffer,
      attestationObject: new Uint8Array([7, 7, 7]).buffer,
      getTransports: () => ["internal"],
    },
    authenticatorAttachment: "platform",
  } as unknown as PublicKeyCredential;
}

function fakeAssertionCredential() {
  return {
    id: "Y3JlZC1pZA",
    rawId: new Uint8Array([1, 2, 3, 4]).buffer,
    type: "public-key",
    getClientExtensionResults: () => ({ credProps: { rk: true } }),
    response: {
      authenticatorData: new Uint8Array([1, 1, 1]).buffer,
      clientDataJSON: new Uint8Array([9, 9]).buffer,
      signature: new Uint8Array([5, 5, 5, 5]).buffer,
      userHandle: new Uint8Array([0, 7]).buffer,
    },
  } as unknown as PublicKeyCredential;
}

describe("webauthn base64url codecs", () => {
  it("round-trips bytes through encode/decode", () => {
    const bytes = new Uint8Array([0, 1, 2, 250, 251, 62, 63, 64, 255]);
    const encoded = b64uEncode(bytes);
    expect(encoded).not.toMatch(/[+/=]/); // URL-safe, unpadded
    expect(Array.from(b64uDecode(encoded))).toEqual(Array.from(bytes));
  });

  it("decodes the library's unpadded challenge into the exact bytes", () => {
    expect(Array.from(b64uDecode("AQIDBAUGBwgJCgsMDQ4PEA"))).toEqual([
      1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
    ]);
  });
});

describe("webauthn option parsing", () => {
  it("maps creation JSON onto DOM options with decoded buffers", () => {
    const options = parseCreationOptions(new TextEncoder().encode(CREATION_JSON));

    expect(options.rp.id).toBe("localhost");
    expect(options.challenge).toBeInstanceOf(Uint8Array);
    expect(Array.from(options.challenge as Uint8Array)).toHaveLength(16);
    expect(Array.from(options.user.id as Uint8Array)).toEqual([...Array(15).fill(0), 7]);
    expect(options.authenticatorSelection?.residentKey).toBe("required");
    expect(options.authenticatorSelection?.userVerification).toBe("required");
    expect(options.pubKeyCredParams).toHaveLength(3);
    expect(options.excludeCredentials).toBeUndefined();
  });

  it("maps assertion JSON onto DOM options with an empty allow-list", () => {
    const options = parseRequestOptions(new TextEncoder().encode(REQUEST_JSON));

    expect(options.rpId).toBe("localhost");
    expect(options.userVerification).toBe("required");
    expect(options.allowCredentials).toBeUndefined(); // discoverable login
    expect(Array.from(options.challenge as Uint8Array)).toHaveLength(16);
  });
});

describe("webauthn response serialization", () => {
  it("serializes a registration response the Go parser accepts", () => {
    const json = JSON.parse(serializeCreationResponse(fakeCreationCredential())) as Record<
      string,
      unknown
    >;

    expect(json.type).toBe("public-key");
    expect(json.rawId).toBe("AQIDBA"); // base64url([1,2,3,4])
    expect(json.id).toBe("Y3JlZC1pZA");
    const response = json.response as Record<string, unknown>;
    expect(response.clientDataJSON).toBe("CQk");
    expect(response.attestationObject).toBe("BwcH"); // [7,7,7]
    expect(response.transports).toEqual(["internal"]);
  });

  it("serializes an assertion response including the user handle", () => {
    const json = JSON.parse(serializeAssertionResponse(fakeAssertionCredential())) as Record<
      string,
      unknown
    >;

    const response = json.response as Record<string, unknown>;
    expect(response.authenticatorData).toBe("AQEB"); // [1,1,1]
    expect(response.signature).toBe("BQUFBQ"); // [5,5,5,5]
    expect(response.userHandle).toBe("AAc"); // user id 7
    // Extension output survives for parsers that read credProps.
    expect((json.clientExtensionResults as Record<string, unknown>).credProps).toEqual({
      rk: true,
    });
  });

  it("encodes a null user handle as an empty string", () => {
    const cred = fakeAssertionCredential();
    (cred.response as AuthenticatorAssertionResponse & { userHandle: null }).userHandle = null;
    const json = JSON.parse(serializeAssertionResponse(cred)) as {
      response: { userHandle: string };
    };
    expect(json.response.userHandle).toBe("");
  });
});
