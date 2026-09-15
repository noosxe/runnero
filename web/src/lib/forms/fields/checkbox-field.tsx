import { useFieldContext } from "../contexts";
import { Field, FieldLabel } from "@/components/ui/field";
import { Checkbox } from "@/components/ui/checkbox";
import { FormError } from "./form-error";

interface CheckboxFieldProps {
  label: string;
  id: string;
  description?: string;
}

/**
 * Boolean toggle bound to the app form (docs/30 §5.4). Select/checkbox rules
 * evaluate on change — callers re-run the shared evaluation in handleChange
 * flows via the form's listeners.
 */
export function CheckboxField({ label, id, description }: CheckboxFieldProps) {
  const field = useFieldContext<boolean>();
  const errors: string[] = field.state.meta.errors.filter(
    (e): e is string => typeof e === "string",
  );
  const showError = errors.length > 0;
  return (
    <Field data-invalid={showError || undefined}>
      <div className="flex items-start gap-3">
        <Checkbox
          id={id}
          name={field.name}
          checked={field.state.value}
          onCheckedChange={(checked) => field.handleChange(checked === true)}
          onBlur={field.handleBlur}
          aria-invalid={showError}
          aria-describedby={showError ? `${id}-error` : undefined}
        />
        <div className="grid gap-1.5">
          <FieldLabel htmlFor={id}>{label}</FieldLabel>
          {description ? <p className="text-muted-foreground text-sm">{description}</p> : null}
        </div>
      </div>
      <FormError id={`${id}-error`} messages={errors} />
    </Field>
  );
}
