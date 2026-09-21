import { PoolHealthStatus, type Pool } from "../../gen/api_pb";
import { PoolHealthBadge } from "./pool-health-badge";
import { Activity, CircleCheck, Clock, LoaderCircle, Pause, TriangleAlert } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { cn } from "cn";

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

/**
 * Health-state presentation for the operational state card: icon, tinted
 * icon chip, and card emphasis (semantic tokens mirroring PoolHealthBadge).
 */
function healthPresentation(status: Pool["healthStatus"]) {
  switch (status) {
    case PoolHealthStatus.DEGRADED:
      return {
        icon: TriangleAlert,
        chip: "border-destructive/30 bg-destructive/10 text-destructive",
        card: "ring-destructive/30 bg-destructive/5",
      };
    case PoolHealthStatus.PROVISIONING:
      return {
        icon: LoaderCircle,
        chip: "border-primary/30 bg-primary/10 text-link",
        card: "",
      };
    case PoolHealthStatus.PAUSED:
      return {
        icon: Pause,
        chip: "border bg-muted text-muted-foreground",
        card: "",
      };
    case PoolHealthStatus.HEALTHY:
    default:
      return {
        icon: CircleCheck,
        chip: "border-success/30 bg-success/10 text-success",
        card: "",
      };
  }
}

export function PoolStatusBanner({ pool, className = "" }: PoolStatusBannerProps) {
  const isDegraded = pool.healthStatus === PoolHealthStatus.DEGRADED;
  const isProvisioning = pool.healthStatus === PoolHealthStatus.PROVISIONING;
  const { icon: StatusIcon, chip, card } = healthPresentation(pool.healthStatus);

  const description =
    pool.currentIntent ||
    (isDegraded
      ? "Reconciliation encountered errors. See diagnostics below."
      : pool.healthStatus === PoolHealthStatus.PAUSED
        ? "Reconciliation is paused for this pool."
        : isProvisioning
          ? "Spawning warm idle runners to reach the pool target."
          : "Warm pool target satisfied and monitoring for incoming workflow jobs.");

  return (
    <Card size="sm" data-testid="pool-status-banner" className={cn(card, className)}>
      <CardContent>
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
          {/* Status summary: tinted state icon, label + badge, current intent */}
          <div className="flex min-w-0 items-start gap-3">
            <div
              className={cn(
                "flex size-9 shrink-0 items-center justify-center rounded-lg border",
                chip,
              )}
            >
              <StatusIcon
                aria-hidden="true"
                className={cn("size-4", isProvisioning && "animate-spin")}
              />
            </div>
            <div className="flex min-w-0 flex-col gap-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  Operational State
                </span>
                <PoolHealthBadge status={pool.healthStatus} size="sm" />
              </div>
              <p className="text-sm font-medium text-foreground">{description}</p>
            </div>
          </div>

          {/* Reconciliation metadata, right-aligned on wide screens to mirror
              the KPI strip below */}
          <div className="flex shrink-0 items-center gap-6 sm:pl-6">
            <div>
              <div className="text-xs font-medium text-muted-foreground">Last Reconciled</div>
              <div className="mt-0.5 flex items-center gap-1.5 text-sm font-semibold text-foreground">
                <Clock className="size-3.5 text-muted-foreground" />
                <span>{formatRelativeTime(pool.lastReconciledAt)}</span>
              </div>
            </div>
            <Separator orientation="vertical" className="hidden h-8 sm:block" />
            <div>
              <div className="text-xs font-medium text-muted-foreground">Reconcile Loop</div>
              <div className="mt-0.5 flex items-center gap-1.5 text-sm font-semibold text-foreground">
                <Activity className="size-3.5 text-muted-foreground" />
                <span>~10s cadence</span>
              </div>
            </div>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
