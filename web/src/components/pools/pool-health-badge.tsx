import { PoolHealthStatus } from "../../gen/api_pb";
import { CheckCircle2, AlertTriangle, Loader2, Pause } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "cn";

export interface PoolHealthBadgeProps {
  status?: PoolHealthStatus | number;
  size?: "sm" | "md";
  className?: string;
}

export function PoolHealthBadge({ status, size = "md", className = "" }: PoolHealthBadgeProps) {
  const isSm = size === "sm";
  const sizeClass = isSm ? "h-4 px-1.5 text-[10px]" : "gap-1.5";

  switch (status) {
    case PoolHealthStatus.DEGRADED:
      return (
        <Badge variant="destructive" className={cn(sizeClass, className)}>
          <AlertTriangle data-icon="inline-start" />
          <span>Degraded</span>
        </Badge>
      );

    case PoolHealthStatus.PROVISIONING:
      return (
        <Badge className={cn("border-primary/30 bg-primary/10 text-primary", sizeClass, className)}>
          <Loader2 data-icon="inline-start" className="animate-spin" />
          <span>Provisioning</span>
        </Badge>
      );

    case PoolHealthStatus.PAUSED:
      return (
        <Badge variant="secondary" className={cn(sizeClass, className)}>
          <Pause data-icon="inline-start" />
          <span>Paused</span>
        </Badge>
      );

    case PoolHealthStatus.HEALTHY:
    default:
      return (
        <Badge className={cn("border-success/30 bg-success/10 text-success", sizeClass, className)}>
          <CheckCircle2 data-icon="inline-start" />
          <span>Healthy</span>
        </Badge>
      );
  }
}
