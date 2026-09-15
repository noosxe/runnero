import {
  createValidator,
  CompilationError,
  RuntimeError,
  type Violation,
} from "@bufbuild/protovalidate";
import type { DescField, DescMessage, MessageShape } from "@bufbuild/protobuf";
import type { Path } from "@bufbuild/protobuf/reflect";

/**
 * One validator for the whole app (docs/30 §5.4): rules are evaluated from the
 * proto descriptors embedded in our generated code — the exact artifacts the
 * server enforces. No zod, no mirrored rule definitions.
 */
const validator = createValidator();

/**
 * A rule violation projected for form consumption: stable rule id, the
 * annotation's user-facing message, and the proto field path it attaches to
 * ("pool.memory_limit" → ["pool", "memory_limit"]). Message-level CEL rules
 * carry an empty path.
 */
export interface ViolationView {
  ruleId: string;
  message: string;
  fieldPath: string[];
}

/**
 * Field path elements are descriptors (field names) interleaved with
 * list/map subscripts; project to plain proto field names for mapping.
 */
function fieldPathOf(violation: Violation): string[] {
  const path: Path = violation.field;
  return path.flatMap((element) =>
    typeof element === "object" && element !== null && "name" in element
      ? [(element as DescField).name]
      : [],
  );
}

export function toViolationView(violation: Violation): ViolationView {
  return {
    ruleId: violation.ruleId,
    message: violation.message,
    fieldPath: fieldPathOf(violation),
  };
}

/**
 * Evaluate a request message against its protovalidate annotations. This is
 * the client-side PREVIEW of what the server enforces (docs/30 §3 goal 4):
 * skipping it buys nothing, and the server verdict through the violations
 * detail channel is authoritative.
 *
 * Rule compilation/evaluation failures are fail-silent here by design: the
 * preview is a courtesy, the server enforces the same rules and answers with
 * structured violations on submit.
 */
export function validateMessage<Desc extends DescMessage>(
  schema: Desc,
  message: MessageShape<Desc>,
): ViolationView[] {
  let result;
  try {
    result = validator.validate(schema, message);
  } catch (err) {
    if (err instanceof CompilationError || err instanceof RuntimeError) {
      return [];
    }
    throw err;
  }
  if (result.kind === "valid") {
    return [];
  }
  return (result.violations ?? []).map(toViolationView);
}

/**
 * Group violations by the LAST proto field name of their path — the key the
 * web maps onto form fields (docs/30 §5.4). Violations without a field path
 * (message-level CEL) are returned separately so callers can surface them at
 * form/step level.
 */
export function groupByField(violations: ViolationView[]): {
  byField: Map<string, string[]>;
  messageLevel: ViolationView[];
} {
  const byField = new Map<string, string[]>();
  const messageLevel: ViolationView[] = [];
  for (const violation of violations) {
    if (violation.fieldPath.length === 0) {
      messageLevel.push(violation);
      continue;
    }
    const key = violation.fieldPath[violation.fieldPath.length - 1];
    const existing = byField.get(key);
    if (existing) {
      existing.push(violation.message);
    } else {
      byField.set(key, [violation.message]);
    }
  }
  return { byField, messageLevel };
}
