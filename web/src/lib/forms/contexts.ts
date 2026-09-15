import { createFormHookContexts } from "@tanstack/react-form";

/**
 * Shared contexts for the app form hook (docs/30 §5.4): every bound field
 * component reads its field through useFieldContext, so the wizard only
 * composes <form.AppField> trees.
 */
export const { fieldContext, useFieldContext, useFormContext, formContext } =
  createFormHookContexts();
