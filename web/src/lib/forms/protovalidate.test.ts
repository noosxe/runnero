import { describe, it, expect } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { CreatePoolRequestSchema, PoolSchema } from "../../gen/api_pb";
import { ViolationsSchema } from "../../gen/buf/validate/validate_pb";
import { groupByField, validateMessage } from "./protovalidate";
import { violationsFromConnectError } from "./violations";

function validPoolRequest() {
  return create(CreatePoolRequestSchema, {
    pool: create(PoolSchema, {
      name: "valid-pool",
      provider: "github",
      repositoryUrl: "https://github.com/org/repo",
      scope: "repo",
      authProfileId: 1n,
      minIdleRunners: 1,
      maxConcurrency: 5,
    }),
  });
}

describe("validateMessage (protovalidate adapter)", () => {
  it("returns no violations for a valid message", () => {
    expect(validateMessage(CreatePoolRequestSchema, validPoolRequest())).toEqual([]);
  });

  it("reports standard-rule violations with canonical rule ids and field paths", () => {
    const req = validPoolRequest();
    req.pool!.name = "Bad Name!";
    const violations = validateMessage(CreatePoolRequestSchema, req);
    const pattern = violations.find((v) => v.ruleId === "string.pattern");
    expect(pattern).toBeDefined();
    expect(pattern?.fieldPath).toEqual(["pool", "name"]);
    expect(pattern?.message).not.toBe("");
  });

  it("reports min_len for empty names", () => {
    const req = validPoolRequest();
    req.pool!.name = "";
    const violations = validateMessage(CreatePoolRequestSchema, req);
    expect(violations.some((v) => v.ruleId === "string.min_len")).toBe(true);
  });

  it("evaluates cross-field CEL (min_idle <= max_concurrency)", () => {
    const req = validPoolRequest();
    req.pool!.minIdleRunners = 20;
    const violations = validateMessage(CreatePoolRequestSchema, req);
    const cel = violations.find((v) => v.ruleId === "pool.min_idle.max_concurrency");
    expect(cel).toBeDefined();
    // Message-level CEL: the path is the message itself (docs/30 §5.4 —
    // step-level bucket, not a single field).
    expect(cel?.fieldPath).toEqual(["pool"]);
  });

  it("skips rules when the value is within range (poll interval sentinel)", () => {
    const req = validPoolRequest();
    req.pool!.pollIntervalSeconds = 0;
    expect(validateMessage(CreatePoolRequestSchema, req)).toEqual([]);
  });

  it("groups violations by last field path element", () => {
    const req = validPoolRequest();
    req.pool!.name = "";
    req.pool!.minIdleRunners = -1;
    const { byField, messageLevel } = groupByField(validateMessage(CreatePoolRequestSchema, req));
    expect(byField.get("name")?.length).toBeGreaterThan(0);
    expect(byField.get("min_idle_runners")?.length).toBeGreaterThan(0);
    expect(messageLevel).toEqual([]);
  });
});

describe("violationsFromConnectError", () => {
  it("extracts typed Violations details", () => {
    const err = new ConnectError("rejected", Code.InvalidArgument, undefined, [
      {
        desc: ViolationsSchema,
        value: {
          violations: [
            {
              ruleId: "pool.name.duplicate",
              message: "pool name already exists",
              field: { elements: [{ fieldName: "name" }] },
            },
          ],
        },
      },
    ]);
    const violations = violationsFromConnectError(err);
    expect(violations).toHaveLength(1);
    expect(violations?.[0]).toEqual({
      ruleId: "pool.name.duplicate",
      message: "pool name already exists",
      fieldPath: ["name"],
    });
  });

  it("returns undefined for errors without violations details", () => {
    expect(
      violationsFromConnectError(new ConnectError("boom", Code.InvalidArgument)),
    ).toBeUndefined();
    expect(violationsFromConnectError(new Error("plain"))).toBeUndefined();
  });
});
