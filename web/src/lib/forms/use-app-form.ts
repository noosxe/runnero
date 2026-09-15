import type { ComponentType } from "react";
import { createFormHook } from "@tanstack/react-form";
import { fieldContext, formContext } from "./contexts";
import { TextField } from "./fields/text-field";
import { CheckboxField } from "./fields/checkbox-field";
import { SubmitButton } from "./submit-button";

/**
 * The app form hook (docs/30 §5.4): the ONLY way a surface composes TanStack
 * Form. Field/form components are fixed here, so every form gets the same
 * bound controls, violation markup, and submit gating.
 */
export const { useAppForm, withForm } = createFormHook({
  fieldComponents: {
    TextField,
    CheckboxField,
  },
  fieldContext,
  formComponents: {
    SubmitButton,
  },
  formContext,
});

export type AppFieldComponent = ComponentType;
