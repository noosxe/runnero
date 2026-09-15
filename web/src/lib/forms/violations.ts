import { ConnectError } from "@connectrpc/connect";
import { ViolationsSchema } from "@/gen/buf/validate/validate_pb";
import type { ViolationView } from "./protovalidate";

/**
 * Stable rule_id mirror (docs/30 §5.4, docs/08): class A CEL ids from
 * proto/api.proto plus the class B registry from
 * internal/server/violations.go. These ids are API contract — forms key
 * behavior on them, never on message text. Keep in lockstep with the Go
 * registry; RUN-223 audits parity after the migration lands.
 */
export const RULE_ID = {
  // Class A CEL ids (proto/api.proto).
  poolMinIdleMaxConcurrency: "pool.min_idle.max_concurrency",
  poolPollIntervalRange: "pool.poll_interval.range",
  poolUpdateIdRequired: "pool.update.id_required",
  authProfileAppIdRequired: "auth_profile.app_id.required",
  authProfilePrivateKeyRequired: "auth_profile.private_key.required",
  authProfileTokenRequired: "auth_profile.token.required",

  // Class B registry (internal/server/violations.go).
  poolMemorySwapRequiresMemory: "pool.memory_swap.requires_memory",
  poolMemoryLimitParse: "pool.memory_limit.parse",
  poolMemorySwapParse: "pool.memory_swap.parse",
  poolMemorySwapGteMemory: "pool.memory_swap.gte_memory",
} as const;

/**
 * Extract protovalidate violations from a Connect error's typed details
 * (buf.validate.Violations). Returns undefined when the rejection carries no
 * violations detail — callers fall back to the banner (docs/30 §5.4).
 */
export function violationsFromConnectError(err: unknown): ViolationView[] | undefined {
  if (!(err instanceof ConnectError)) {
    return undefined;
  }
  const [detail] = err.findDetails(ViolationsSchema);
  if (!detail) {
    return undefined;
  }
  return detail.violations.map((violation) => {
    const fieldPath = (violation.field?.elements ?? [])
      .map((element) => element.fieldName ?? "")
      .filter((name) => name !== "");
    return {
      ruleId: violation.ruleId ?? "",
      message: violation.message ?? "",
      fieldPath,
    };
  });
}
