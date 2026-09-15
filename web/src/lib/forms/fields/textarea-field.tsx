import { useFieldContext } from "../contexts";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Textarea } from "@/components/ui/textarea";
import { FormError } from "./form-error";

interface TextareaFieldProps {
  label: string;
  id: string;
  rows?: number;
  placeholder?: string;
  className?: string;
  /** Classes for the textarea itself (e.g. monospace secrets). */
  inputClassName?: string;
  description?: string;
  onBlurExtra?: () => void;
}

/**
 * Multiline input bound to the app form (docs/30 §5.4): same value/blur/error
 * flow as TextField, for long-form secrets (PEM keys).
 */
export function TextareaField({
  label,
  id,
  rows,
  placeholder,
  className,
  inputClassName,
  description,
  onBlurExtra,
}: TextareaFieldProps) {
  const field = useFieldContext<string>();
  const errors: string[] = field.state.meta.errors.filter(
    (e): e is string => typeof e === "string",
  );
  const showError = errors.length > 0;
  return (
    <Field data-invalid={showError || undefined} className={className}>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Textarea
        id={id}
        name={field.name}
        rows={rows}
        placeholder={placeholder}
        className={inputClassName}
        value={field.state.value}
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
