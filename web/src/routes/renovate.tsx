import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { cn } from "cn";
import { Button } from "@/components/ui/button";
import { Link } from "@tanstack/react-router";
import { usePools, useRenovateStatus, useTriggerRenovateRun } from "../lib/api/query-hooks";
import type { Pool } from "../gen/api_pb";
import { poolTargetList, TargetCountBadge } from "../components/pools/pool-targets";
import { Bot, Play, ArrowUpRight, Clock, Layers, Calendar, Loader2 } from "lucide-react";

export function RenovatePage() {
  const { data: pools, isLoading } = usePools();

  const totalPools = pools?.length ?? 0;
  const enabledPools = pools?.filter((p) => p.renovate?.enabled).length ?? 0;

  return (
    <div className="space-y-6">
      {/* Header */}
      <div>
        <div className="flex items-center gap-2.5">
          <h1 className="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
            Renovate Bot Dashboard
          </h1>
          <Badge className="border-primary/30 bg-primary/10 text-primary font-medium">
            <Bot />
            <span className="font-mono text-[10px]">Managed Automation</span>
          </Badge>
        </div>
        <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
          Automated dependency updates, scheduled scans, and on-demand maintenance runs across
          runner pools.
        </p>
      </div>

      {/* Overview Stats */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <div className="rounded-2xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-slate-500 dark:text-slate-400">
              Configured Pools
            </span>
            <Layers className="h-4 w-4 text-slate-400" />
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-slate-900 dark:text-white">{totalPools}</span>
            <span className="text-xs text-slate-400">total pools</span>
          </div>
        </div>

        <div className="rounded-2xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-slate-500 dark:text-slate-400">
              Renovate Active
            </span>
            <Bot className="h-4 w-4 text-emerald-500" />
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-emerald-600 dark:text-emerald-400">
              {enabledPools}
            </span>
            <span className="text-xs text-slate-400">of {totalPools} pools scheduled</span>
          </div>
        </div>

        <div className="rounded-2xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-slate-500 dark:text-slate-400">
              Automation Coverage
            </span>
            <Calendar className="h-4 w-4 text-blue-500" />
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-slate-900 dark:text-white">
              {totalPools > 0 ? Math.round((enabledPools / totalPools) * 100) : 0}%
            </span>
            <span className="text-xs text-slate-400">pools covered</span>
          </div>
        </div>
      </div>

      {/* Pools Renovate List */}
      <div className="space-y-4">
        <h2 className="text-base font-bold text-slate-900 dark:text-white">
          Runner Pool Schedules & Status
        </h2>

        {isLoading ? (
          <div className="rounded-2xl border border-slate-200 bg-white p-12 text-center text-xs text-slate-400 dark:border-slate-800 dark:bg-slate-900">
            Loading runner pools...
          </div>
        ) : !pools || pools.length === 0 ? (
          <div className="rounded-2xl border border-dashed border-slate-300 p-12 text-center text-slate-500 dark:border-slate-800 dark:text-slate-400">
            <Bot className="mx-auto h-8 w-8 text-slate-400 mb-2" />
            <p className="text-sm font-semibold text-slate-800 dark:text-slate-200">
              No runner pools found
            </p>
            <p className="text-xs text-slate-500 mt-1 max-w-sm mx-auto">
              Create your first runner pool to enable automated Renovate dependency updates.
            </p>
            <Link
              to="/onboarding"
              className="mt-4 inline-flex items-center gap-1.5 rounded-xl bg-blue-600 px-4 py-2 text-xs font-semibold text-white shadow-xs hover:bg-blue-500 transition-colors"
            >
              + Create Runner Pool
            </Link>
          </div>
        ) : (
          <div className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
            <div className="overflow-x-auto">
              <table className="w-full text-left text-xs text-slate-600 dark:text-slate-300">
                <thead className="border-b border-slate-100 bg-slate-50 font-semibold uppercase tracking-wider text-[11px] text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
                  <tr>
                    <th className="px-5 py-3.5">Pool / Repository</th>
                    <th className="px-5 py-3.5">Renovate Status</th>
                    <th className="px-5 py-3.5">Schedule</th>
                    <th className="px-5 py-3.5">Last Run Result</th>
                    <th className="px-5 py-3.5 text-right">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {pools.map((pool) => (
                    <PoolRenovateRow key={pool.id.toString()} pool={pool} />
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function PoolRenovateRow({ pool }: { pool: Pool }) {
  const { data: status } = useRenovateStatus(pool.id, {
    enabled: pool.renovate?.enabled ?? false,
    refetchInterval: 10000,
  });
  const triggerMutation = useTriggerRenovateRun();
  const [triggerMsg, setTriggerMsg] = useState<string | null>(null);

  const isEnabled = pool.renovate?.enabled ?? false;
  const isRunning = status?.lastRun?.status === "running";

  const handleTrigger = async () => {
    setTriggerMsg(null);
    try {
      const res = await triggerMutation.mutateAsync(pool.id);
      if (res.success) {
        setTriggerMsg(`Run #${res.runId} triggered`);
        setTimeout(() => setTriggerMsg(null), 3000);
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Trigger failed";
      setTriggerMsg(msg);
      setTimeout(() => setTriggerMsg(null), 4000);
    }
  };

  return (
    <tr className="hover:bg-slate-50/50 dark:hover:bg-slate-800/50 transition-colors">
      <td className="px-5 py-4">
        <Link
          to="/pools/$poolId"
          params={{ poolId: pool.id.toString() }}
          className="font-semibold text-blue-600 hover:underline dark:text-blue-400"
        >
          {pool.name}
        </Link>
        <div className="mt-0.5 flex items-center gap-1.5 text-[11px] font-mono text-slate-400 max-w-xs">
          <span className="truncate">{poolTargetList(pool)[0]}</span>
          <TargetCountBadge pool={pool} />
        </div>
      </td>

      <td className="px-5 py-4">
        <Badge
          className={cn(
            "uppercase tracking-wider",
            isEnabled
              ? "border-success/30 bg-success/10 text-success"
              : "bg-muted text-muted-foreground",
          )}
        >
          <span
            className={`h-1.5 w-1.5 rounded-full ${isEnabled ? "bg-emerald-500" : "bg-slate-400"}`}
          />
          <span>{isEnabled ? "Enabled" : "Disabled"}</span>
        </Badge>
      </td>

      <td className="px-5 py-4">
        {isEnabled ? (
          <div className="space-y-0.5">
            <span className="font-mono text-xs font-medium text-slate-800 dark:text-slate-200">
              {pool.renovate?.cronSchedule || "0 3 * * 1"}
            </span>
            <div className="text-[10px] text-slate-400 flex items-center gap-1">
              <Clock className="h-3 w-3" />
              <span>Next: {status?.nextScheduledRun || "Scheduled"}</span>
            </div>
          </div>
        ) : (
          <span className="text-slate-400 text-xs italic">Not configured</span>
        )}
      </td>

      <td className="px-5 py-4">
        {status?.lastRun ? (
          <div className="space-y-0.5">
            <Badge
              className={cn(
                "uppercase tracking-wider",
                isRunning
                  ? "border-warning/30 bg-warning/10 text-warning"
                  : status.lastRun.status === "success"
                    ? "border-success/30 bg-success/10 text-success"
                    : "border-destructive/30 bg-destructive/10 text-destructive",
              )}
            >
              <span
                className={`h-1.5 w-1.5 rounded-full ${
                  isRunning
                    ? "bg-amber-500 animate-ping"
                    : status.lastRun.status === "success"
                      ? "bg-emerald-500"
                      : "bg-rose-500"
                }`}
              />
              <span>{status.lastRun.status}</span>
            </Badge>
            {status.lastRun.completedAt && (
              <div className="text-[10px] text-slate-400 font-mono">
                {new Date(status.lastRun.completedAt).toLocaleDateString()}
              </div>
            )}
          </div>
        ) : (
          <span className="text-slate-400 text-xs">No runs yet</span>
        )}
      </td>

      <td className="px-5 py-4 text-right">
        <div className="flex items-center justify-end gap-2">
          {triggerMsg && (
            <span className="text-[11px] font-medium text-emerald-600 dark:text-emerald-400 animate-pulse">
              {triggerMsg}
            </span>
          )}
          <Button
            variant="outline"
            size="xs"
            onClick={handleTrigger}
            disabled={triggerMutation.isPending || isRunning}
          >
            {triggerMutation.isPending || isRunning ? (
              <Loader2 data-icon="inline-start" className="animate-spin" />
            ) : (
              <Play data-icon="inline-start" className="fill-current" />
            )}
            <span>{isRunning ? "Running..." : "Trigger"}</span>
          </Button>
          <Link
            to="/pools/$poolId"
            params={{ poolId: pool.id.toString() }}
            className="inline-flex items-center gap-1 rounded-lg border border-transparent px-2 py-1 text-xs font-semibold text-blue-600 hover:text-blue-700 dark:text-blue-400 dark:hover:text-blue-300 transition-colors"
          >
            <span>Manage</span>
            <ArrowUpRight className="h-3.5 w-3.5" />
          </Link>
        </div>
      </td>
    </tr>
  );
}
