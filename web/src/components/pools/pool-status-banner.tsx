import { PoolHealthStatus, type Pool } from "../../gen/api_pb";
import { PoolHealthBadge } from "./pool-health-badge";
import { Activity, CircleCheck, Clock, LoaderCircle, TriangleAlert } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";

export interface PoolStatusBannerProps {
  pool: Pool;
  className?: string;
}

function formatRelativeTime(isoString?: string): string {
  if (!isoString) return "Recently";
  try {
    const d = new Date(isoString);
    if (isNaN(d.getTime())) return "Recently";
    const diffSec = Math.max(0, Math.floor((Date.now() - d.getTime()) / 1000));
    if (diffSec < 5) return "Just now";
    if (diffSec < 60) return `${diffSec}s ago`;
    const diffMin = Math.floor(diffSec / 60);
    if (diffMin < 60) return `${diffMin}m ago`;
    const diffHours = Math.floor(diffMin / 60);
    return `${diffHours}h ago`;
  } catch {
    return "Recently";
  }
}

export function PoolStatusBanner({ pool, className = "" }: PoolStatusBannerProps) {
  const isDegraded = pool.healthStatus === PoolHealthStatus.DEGRADED;
  const isProvisioning = pool.healthStatus === PoolHealthStatus.PROVISIONING;

  const description =
    pool.currentIntent ||
    (isDegraded
      ? "Reconciliation encountered errors. See diagnostics below."
      : "Warm pool target satisfied and monitoring for incoming workflow jobs.");

  return (
    <Alert variant={isDegraded ? "destructive" : "default"} className={className}>
      {isDegraded ? (
        <TriangleAlert />
      ) : isProvisioning ? (
        <LoaderCircle className="animate-spin" />
      ) : (
        <CircleCheck />
      )}
      <AlertTitle className="flex items-center gap-2.5">
        <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Operational State
        </span>
        <PoolHealthBadge status={pool.healthStatus} size="sm" />
      </AlertTitle>
      <AlertDescription>
        <span className="block text-sm font-medium">{description}</span>
        <span className="mt-1 flex flex-wrap items-center gap-3 text-xs">
          <span className="flex items-center gap-1">
            <Clock className="size-3.5 text-muted-foreground" />
            <span>Reconciled {formatRelativeTime(pool.lastReconciledAt)}</span>
          </span>
          <span className="flex items-center gap-1 border-l border-border pl-3">
            <Activity className="size-3.5 text-muted-foreground" />
            <span>Loop: ~10s cadence</span>
          </span>
        </span>
      </AlertDescription>
    </Alert>
  );
}
