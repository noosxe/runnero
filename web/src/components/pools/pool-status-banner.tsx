import { PoolHealthStatus, type Pool } from "../../gen/api_pb";
import { PoolHealthBadge } from "./pool-health-badge";
import { Activity, Clock } from "lucide-react";

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

  return (
    <div
      className={`rounded-2xl border p-4 transition-all ${
        isDegraded
          ? "border-rose-200 bg-rose-50/50 dark:border-rose-900/40 dark:bg-rose-950/20"
          : isProvisioning
            ? "border-blue-200 bg-blue-50/40 dark:border-blue-900/40 dark:bg-blue-950/20"
            : "border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900"
      } ${className}`}
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="space-y-1">
          <div className="flex items-center gap-2.5">
            <span className="text-xs font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
              Operational State
            </span>
            <PoolHealthBadge status={pool.healthStatus} size="sm" />
          </div>

          <p className="text-sm font-medium text-slate-800 dark:text-slate-200">
            {pool.currentIntent ||
              (isDegraded
                ? "Reconciliation encountered errors. See diagnostics below."
                : "Warm pool target satisfied and monitoring for incoming workflow jobs.")}
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-3 text-xs text-slate-500 dark:text-slate-400">
          <div className="flex items-center gap-1">
            <Clock className="h-3.5 w-3.5 text-slate-400" />
            <span>Reconciled {formatRelativeTime(pool.lastReconciledAt)}</span>
          </div>
          <div className="hidden sm:flex items-center gap-1 border-l border-slate-200 pl-3 dark:border-slate-800">
            <Activity className="h-3.5 w-3.5 text-slate-400" />
            <span>Loop: ~10s cadence</span>
          </div>
        </div>
      </div>
    </div>
  );
}
