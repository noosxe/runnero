import { type ComponentProps } from "react";
import { cn } from "cn";

import { Badge } from "@/components/ui/badge";

/**
 * Warning-styled Badge for app code.
 *
 * The preset `badge.tsx` (frozen for M27) has no `warning` variant, and usage
 * sites must not override component colors via className. This wrapper is the
 * sanctioned extension point per RUN-197: promote it into the preset mapping
 * if warning badges start recurring.
 */
function WarningBadge({ className, ...props }: ComponentProps<typeof Badge>) {
  return (
    <Badge
      className={cn("border-transparent bg-warning text-warning-foreground", className)}
      {...props}
    />
  );
}

export { WarningBadge };
