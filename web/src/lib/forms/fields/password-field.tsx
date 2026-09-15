import { useState } from "react";
import { useFieldContext } from "../contexts";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group";
import { Eye, EyeOff } from "lucide-react";
import { FormError } from "./form-error";

interface PasswordFieldProps {
  label: string;
  id: string;
  placeholder?: string;
  className?: string;
  description?: string;
  /** Autofocus the input on mount (login/admin steps). */
  autoFocus?: boolean;
  onBlurExtra?: () => void;
}

/**
 * Secret input bound to the app form (docs/30 §5.4): same value/blur/error
 * flow as TextField, with a visibility toggle (the eye never touches the
 * value). Evaluated on the shared violation map like every bound field.
 */
export function PasswordField({
  label,
  id,
  placeholder,
  className,
  description,
  autoFocus,
  onBlurExtra,
}: PasswordFieldProps) {
  const field = useFieldContext<string>();
  const [visible, setVisible] = useState(false);
  const errors: string[] = field.state.meta.errors.filter(
    (e): e is string => typeof e === "string",
  );
  const showError = errors.length > 0;
  return (
    <Field data-invalid={showError || undefined} className={className}>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <InputGroup>
        <InputGroupInput
          id={id}
          name={field.name}
          type={visible ? "text" : "password"}
          value={field.state.value}
          placeholder={placeholder}
          autoFocus={autoFocus}
          onBlur={() => {
            field.handleBlur();
            onBlurExtra?.();
          }}
          onChange={(e) => field.handleChange(e.target.value)}
          aria-invalid={showError}
          aria-describedby={showError ? `${id}-error` : undefined}
        />
        <InputGroupAddon align="inline-end">
          <InputGroupButton
            variant="ghost"
            size="icon-xs"
            aria-label="Toggle password visibility"
            onClick={() => setVisible(!visible)}
            tabIndex={-1}
          >
            {visible ? <EyeOff /> : <Eye />}
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>
      {description ? (
        <FieldDescription className="text-[11px]">{description}</FieldDescription>
      ) : null}
      <FormError id={`${id}-error`} messages={errors} />
    </Field>
  );
}
