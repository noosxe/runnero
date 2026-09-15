import { useFieldContext } from "../contexts";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { FormError } from "./form-error";

interface TextFieldProps {
  label: string;
  id: string;
  type?: "text" | "number";
  placeholder?: string;
  /** Lower/upper bounds for type="number" inputs. */
  min?: number;
  max?: number;
  step?: number;
  className?: string;
  description?: string;
  onBlurExtra?: () => void;
}

/**
 * Free-form input bound to the app form (docs/30 §5.4): value/blur flow
 * through TanStack Form, evaluation happens on the shared violation map, and
 * errors render per the uniform markup contract (docs/30 §5.6).
 */
export function TextField({
  label,
  id,
  type = "text",
  placeholder,
  min,
  max,
  step,
  className,
  description,
  onBlurExtra,
}: TextFieldProps) {
  const field = useFieldContext<string>();
  const errors: string[] = field.state.meta.errors.filter(
    (e): e is string => typeof e === "string",
  );
  const showError = errors.length > 0;
  return (
    <Field data-invalid={showError || undefined} className={className}>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input
        id={id}
        name={field.name}
        type={type}
        value={field.state.value}
        placeholder={placeholder}
        min={min}
        max={max}
        step={step}
        onBlur={() => {
          field.handleBlur();
          onBlurExtra?.();
        }}
        onChange={(e) => field.handleChange(e.target.value)}
        aria-invalid={showError}
        aria-describedby={showError ? `${id}-error` : undefined}
      />
      {description ? (
        <FieldDescription className="text-[11px]">{description}</FieldDescription>
      ) : null}
      <FormError id={`${id}-error`} messages={errors} />
    </Field>
  );
}
