import type { LucideIcon } from "lucide-react";
import { ShieldBan } from "lucide-react";
import { Card } from "@/components/ui/card";
import { FEATURE_DISABLED_HINT } from "../../lib/feature-gates";

/**
 * Shared "disabled for v1.0.0" notice (RUN-289): rendered in place of the
 * interactive content of gated surfaces (Renovate page, pool renovate tab).
 * The surrounding page header/nav stays, so the surface remains visible —
 * only its usability is removed until the feature flag flips.
 */
export function FeatureDisabledNotice({
  title,
  icon: Icon = ShieldBan,
  testId,
}: {
  title: string;
  icon?: LucideIcon;
  testId?: string;
}) {
  return (
    <Card
      size="sm"
      className="flex flex-col items-center gap-3 px-6 py-10 text-center"
      data-testid={testId ?? "feature-disabled-notice"}
    >
      <span
        aria-hidden="true"
        className="flex size-10 items-center justify-center rounded-lg border border-border bg-muted text-muted-foreground"
      >
        <Icon className="size-5" />
      </span>
      <p className="text-sm font-semibold text-foreground">{title}</p>
      <p className="max-w-md text-xs text-muted-foreground">{FEATURE_DISABLED_HINT}</p>
    </Card>
  );
}
