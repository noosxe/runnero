import type { AnyFormApi } from "@tanstack/react-form";

/**
 * Project a shared violation map onto field meta (docs/30 §5.4): each field's
 * first message lands in errorMap.onBlur — v1.33 DERIVES meta.errors from
 * errorMap, so a direct `errors:` write would be ignored. Fields listed in
 * `allFields` but absent from `fieldErrors` are cleared, so a fixed field
 * loses its stale inline error on the next evaluation. `prev?.errorMap` may
 * be undefined for unmounted fields.
 */
export function applyFieldErrors<T extends string>(
  form: AnyFormApi,
  fieldErrors: Partial<Record<T, string[]>>,
  allFields: readonly T[],
): void {
  for (const key of allFields) {
    const messages = fieldErrors[key] ?? [];
    form.setFieldMeta(key, (prev) => ({
      ...prev,
      errorMap: { ...(prev?.errorMap ?? {}), onBlur: messages[0] },
    }));
  }
}
