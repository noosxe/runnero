import { useFormContext } from "./contexts";
import { Button } from "@/components/ui/button";

interface SubmitButtonProps {
  children: React.ReactNode;
  className?: string;
}

/**
 * Submit gate (docs/30 §5.4): disabled-shaped via aria-disabled while the
 * form is submitting, per the markup contract — the button stays focusable
 * and the click is a no-op instead of dropping the attribute set. The real
 * validity gate is the shared violation map the submit handler consults; the
 * gate and the inline messages can therefore never disagree.
 */
export function SubmitButton({ children, className }: SubmitButtonProps) {
  const form = useFormContext();
  const submitting = form.state.isSubmitting;
  return (
    <Button
      type="submit"
      className={className}
      aria-disabled={submitting || undefined}
      aria-busy={submitting || undefined} /* docs/36 §5.5: busy state announced */
      onClick={(e) => {
        if (submitting) {
          e.preventDefault();
        }
      }}
    >
      {children}
    </Button>
  );
}
