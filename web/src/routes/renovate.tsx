import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { cn } from "cn";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
  EmptyDescription,
  EmptyContent,
} from "@/components/ui/empty";
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from "@/components/ui/table";
import { LinkButton } from "../lib/link-button";
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
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Bot />
              </EmptyMedia>
              <EmptyTitle>No runner pools found</EmptyTitle>
              <EmptyDescription>
                Create your first runner pool to enable automated Renovate dependency updates.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <LinkButton to="/onboarding" size="sm">
                Create Runner Pool
              </LinkButton>
            </EmptyContent>
          </Empty>
        ) : (
          <div className="overflow-hidden rounded-2xl border bg-card shadow-xs">
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Pool / Repository</TableHead>
                    <TableHead>Renovate Status</TableHead>
                    <TableHead>Schedule</TableHead>
                    <TableHead>Last Run Result</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {pools.map((pool) => (
                    <PoolRenovateRow key={pool.id.toString()} pool={pool} />
                  ))}
                </TableBody>
              </Table>
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
    <TableRow>
      <TableCell>
        <Link
          to="/pools/$poolId"
          params={{ poolId: pool.id.toString() }}
          className="font-semibold text-primary hover:underline"
        >
          {pool.name}
        </Link>
        <div className="mt-0.5 flex max-w-xs items-center gap-1.5 font-mono text-[11px] text-muted-foreground">
          <span className="truncate">{poolTargetList(pool)[0]}</span>
          <TargetCountBadge pool={pool} />
        </div>
      </TableCell>

      <TableCell>
        <Badge
          className={cn(
            "uppercase tracking-wider",
            isEnabled
              ? "border-success/30 bg-success/10 text-success"
              : "bg-muted text-muted-foreground",
          )}
        >
          <span
            className={`size-1.5 rounded-full ${isEnabled ? "bg-emerald-500" : "bg-slate-400"}`}
          />
          <span>{isEnabled ? "Enabled" : "Disabled"}</span>
        </Badge>
      </TableCell>

      <TableCell>
        {isEnabled ? (
          <div className="flex flex-col gap-0.5">
            <span className="font-mono text-xs font-medium">
              {pool.renovate?.cronSchedule || "0 3 * * 1"}
            </span>
            <div className="flex items-center gap-1 text-[10px] text-muted-foreground">
              <Clock className="size-3" />
              <span>Next: {status?.nextScheduledRun || "Scheduled"}</span>
            </div>
          </div>
        ) : (
          <span className="text-xs italic text-muted-foreground">Not configured</span>
        )}
      </TableCell>

      <TableCell>
        {status?.lastRun ? (
          <div className="flex flex-col gap-0.5">
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
                className={`size-1.5 rounded-full ${
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
              <div className="font-mono text-[10px] text-muted-foreground">
                {new Date(status.lastRun.completedAt).toLocaleDateString()}
              </div>
            )}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">No runs yet</span>
        )}
      </TableCell>

      <TableCell className="text-right">
        <div className="flex items-center justify-end gap-2">
          {triggerMsg && (
            <span className="animate-pulse text-[11px] font-medium text-success">{triggerMsg}</span>
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
          <LinkButton
            to="/pools/$poolId"
            params={{ poolId: pool.id.toString() }}
            variant="ghost"
            size="xs"
          >
            <span>Manage</span>
            <ArrowUpRight data-icon="inline-end" />
          </LinkButton>
        </div>
      </TableCell>
    </TableRow>
  );
}
