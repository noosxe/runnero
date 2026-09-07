import { PoolHealthStatus } from "../../gen/api_pb";
import { CheckCircle2, AlertTriangle, Loader2, Pause } from "lucide-react";

export interface PoolHealthBadgeProps {
  status?: PoolHealthStatus | number;
  size?: "sm" | "md";
  className?: string;
}

export function PoolHealthBadge({ status, size = "md", className = "" }: PoolHealthBadgeProps) {
  const isSm = size === "sm";

  switch (status) {
    case PoolHealthStatus.DEGRADED:
      return (
        <span
          className={`inline-flex items-center gap-1.5 rounded-full font-medium border bg-rose-50 text-rose-700 dark:bg-rose-950/50 dark:text-rose-400 border-rose-200 dark:border-rose-900/60 ${
            isSm ? "px-2 py-0.5 text-[10px]" : "px-2.5 py-1 text-xs"
          } ${className}`}
        >
          <AlertTriangle className={isSm ? "h-3 w-3" : "h-3.5 w-3.5"} />
          <span>Degraded</span>
        </span>
      );

    case PoolHealthStatus.PROVISIONING:
      return (
        <span
          className={`inline-flex items-center gap-1.5 rounded-full font-medium border bg-blue-50 text-blue-700 dark:bg-blue-950/50 dark:text-blue-400 border-blue-200 dark:border-blue-900/60 ${
            isSm ? "px-2 py-0.5 text-[10px]" : "px-2.5 py-1 text-xs"
          } ${className}`}
        >
          <Loader2 className={`animate-spin ${isSm ? "h-3 w-3" : "h-3.5 w-3.5"}`} />
          <span>Provisioning</span>
        </span>
      );

    case PoolHealthStatus.PAUSED:
      return (
        <span
          className={`inline-flex items-center gap-1.5 rounded-full font-medium border bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300 border-slate-200 dark:border-slate-700 ${
            isSm ? "px-2 py-0.5 text-[10px]" : "px-2.5 py-1 text-xs"
          } ${className}`}
        >
          <Pause className={isSm ? "h-3 w-3" : "h-3.5 w-3.5"} />
          <span>Paused</span>
        </span>
      );

    case PoolHealthStatus.HEALTHY:
    default:
      return (
        <span
          className={`inline-flex items-center gap-1.5 rounded-full font-medium border bg-emerald-50 text-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-400 border-emerald-200 dark:border-emerald-900/60 ${
            isSm ? "px-2 py-0.5 text-[10px]" : "px-2.5 py-1 text-xs"
          } ${className}`}
        >
          <CheckCircle2 className={isSm ? "h-3 w-3" : "h-3.5 w-3.5"} />
          <span>Healthy</span>
        </span>
      );
  }
}
