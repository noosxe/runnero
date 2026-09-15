import { FieldError } from "@/components/ui/field";

/**
 * Uniform inline error markup (docs/30 §5.6): role=alert + stable testid.
 * Rendered by bound field components when the field's violation map is
 * non-empty; the id pairs with aria-describedby on the input.
 */
export function FormError({ id, messages }: { id: string; messages: string[] }) {
  if (messages.length === 0) {
    return null;
  }
  return (
    <FieldError id={id}>
      <p role="alert" data-testid="form-error">
        {messages.join(" ")}
      </p>
    </FieldError>
  );
}
