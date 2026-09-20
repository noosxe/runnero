import { useFieldContext } from "../contexts";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { FormError } from "./form-error";

interface TextFieldProps {
  label: string;
  id: string;
  type?: "text" | "number" | "url";
  placeholder?: string;
  /** Lower/upper bounds for type="number" inputs. */
  min?: number;
  max?: number;
  step?: number;
  className?: string;
  /** Classes for the input itself (e.g. monospace numerics). */
  inputClassName?: string;
  description?: string;
  /** Unit suffix rendered flush after the input (e.g. "runners"). */
  suffix?: string;
  /** Autofocus the input on mount (login/admin steps). */
  autoFocus?: boolean;
  /** autocomplete hint (docs/36 §5.5/§5.10): username | email | url | one-time-code … */
  autoComplete?: string;
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
  inputClassName,
  description,
  suffix,
  autoFocus,
  autoComplete,
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
      {suffix ? (
        <div className="flex rounded-xl border border-border bg-card shadow-xs bg-muted">
          <Input
            id={id}
            name={field.name}
            type={type}
            value={field.state.value}
            placeholder={placeholder}
            min={min}
            max={max}
            step={step}
            className={inputClassName}
            autoFocus={autoFocus}
            autoComplete={autoComplete}
            onBlur={() => {
              field.handleBlur();
              onBlurExtra?.();
            }}
            onChange={(e) => field.handleChange(e.target.value)}
            aria-invalid={showError}
            aria-describedby={showError ? `${id}-error` : undefined}
          />
          <span className="flex items-center px-3 text-xs text-muted-foreground">{suffix}</span>
        </div>
      ) : (
        <Input
          id={id}
          name={field.name}
          type={type}
          value={field.state.value}
          placeholder={placeholder}
          min={min}
          max={max}
          step={step}
          className={inputClassName}
          autoFocus={autoFocus}
          autoComplete={autoComplete}
          onBlur={() => {
            field.handleBlur();
            onBlurExtra?.();
          }}
          onChange={(e) => field.handleChange(e.target.value)}
          aria-invalid={showError}
          aria-describedby={showError ? `${id}-error` : undefined}
        />
      )}
      {description ? (
        <FieldDescription className="text-[11px]">{description}</FieldDescription>
      ) : null}
      <FormError id={`${id}-error`} messages={errors} />
    </Field>
  );
}
