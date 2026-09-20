import { createColumnHelper } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "cn";
import { LinkButton } from "../lib/link-button";
import { Link } from "@tanstack/react-router";
import { useRenovateStatus, useTriggerRenovateRun } from "../lib/api/query-hooks";
import { Spinner } from "@/components/ui/spinner";
import { toast } from "@/components/ui/toast";
import type { Pool } from "../gen/api_pb";
import { poolTargetList, TargetCountBadge } from "../components/pools/pool-targets";
import { ArrowUpRight, Clock, Play } from "lucide-react";
import type { AppTableFeatures } from "../lib/tables/use-app-table";

const columnHelper = createColumnHelper<AppTableFeatures, Pool>();

/**
 * Row-scoped status observer. The original hand-rolled table called these
 * hooks once per row component; as cell components they still run one
 * observer per row, and identical query keys dedupe inside react-query.
 */
function usePoolRenovateStatus(pool: Pool) {
  return useRenovateStatus(pool.id, {
    enabled: pool.renovate?.enabled ?? false,
    refetchInterval: 10000,
  });
}

function PoolNameCell({ pool }: { pool: Pool }) {
  return (
    <>
      <Link
        to="/pools/$poolId"
        params={{ poolId: pool.id.toString() }}
        className="font-semibold text-link hover:underline"
      >
        {pool.name}
      </Link>
      <div className="mt-0.5 flex max-w-xs items-center gap-1.5 font-mono text-[11px] text-muted-foreground">
        <span className="truncate">{poolTargetList(pool)[0]}</span>
        <TargetCountBadge pool={pool} />
      </div>
    </>
  );
}

function RenovateStatusCell({ pool }: { pool: Pool }) {
  const isEnabled = pool.renovate?.enabled ?? false;
  return (
    <Badge
      className={cn(
        "uppercase tracking-wider",
        isEnabled
          ? "border-success/30 bg-success/10 text-success"
          : "bg-muted text-muted-foreground",
      )}
    >
      <span className={cn("size-1.5 rounded-full", isEnabled ? "bg-success" : "bg-muted")} />
      <span>{isEnabled ? "Enabled" : "Disabled"}</span>
    </Badge>
  );
}

function ScheduleCell({ pool }: { pool: Pool }) {
  const { data: status } = usePoolRenovateStatus(pool);
  const isEnabled = pool.renovate?.enabled ?? false;
  if (!isEnabled) {
    return <span className="text-xs italic text-muted-foreground">Not configured</span>;
  }
  return (
    <div className="flex flex-col gap-0.5">
      <span className="font-mono text-xs font-medium">
        {pool.renovate?.cronSchedule || "0 3 * * 1"}
      </span>
      <div className="flex items-center gap-1 text-[10px] text-muted-foreground">
        <Clock className="size-3" />
        <span>Next: {status?.nextScheduledRun || "Scheduled"}</span>
      </div>
    </div>
  );
}

function LastRunCell({ pool }: { pool: Pool }) {
  const { data: status } = usePoolRenovateStatus(pool);
  const isRunning = status?.lastRun?.status === "running";
  if (!status?.lastRun) {
    return <span className="text-xs text-muted-foreground">No runs yet</span>;
  }
  return (
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
          className={cn(
            "size-1.5 rounded-full",
            isRunning
              ? "bg-warning animate-ping"
              : status.lastRun.status === "success"
                ? "bg-success"
                : "bg-destructive",
          )}
        />
        <span>{status.lastRun.status}</span>
      </Badge>
      {status.lastRun.completedAt && (
        <div className="font-mono text-[10px] text-muted-foreground">
          {new Date(status.lastRun.completedAt).toLocaleDateString()}
        </div>
      )}
    </div>
  );
}

function ActionsCell({ pool }: { pool: Pool }) {
  const { data: status } = usePoolRenovateStatus(pool);
  const triggerMutation = useTriggerRenovateRun();
  const isRunning = status?.lastRun?.status === "running";

  const handleTrigger = async () => {
    try {
      const res = await triggerMutation.mutateAsync(pool.id);
      if (res.success) {
        toast.add({
          title: `Run #${res.runId} triggered`,
          description: `Renovate run queued for pool "${pool.name}".`,
          type: "success",
        });
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Trigger failed";
      toast.add({
        title: "Trigger failed",
        description: msg,
        type: "error",
      });
    }
  };

  return (
    <div className="flex items-center justify-end gap-2">
      <Button
        variant="outline"
        size="xs"
        onClick={handleTrigger}
        disabled={triggerMutation.isPending || isRunning}
      >
        {triggerMutation.isPending || isRunning ? (
          <Spinner data-icon="inline-start" />
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
  );
}

/**
 * Column defs for the renovate dashboard pool table (docs/31 §5 phase 1).
 * Cells render row-scoped components so per-row queries (status polling)
 * and the trigger mutation keep working under the toolkit.
 */
export const poolRenovateColumns = () =>
  columnHelper.columns([
    columnHelper.display({
      id: "pool",
      header: "Pool / Repository",
      cell: ({ row }) => <PoolNameCell pool={row.original} />,
    }),
    columnHelper.display({
      id: "renovate-status",
      header: "Renovate Status",
      cell: ({ row }) => <RenovateStatusCell pool={row.original} />,
    }),
    columnHelper.display({
      id: "schedule",
      header: "Schedule",
      cell: ({ row }) => <ScheduleCell pool={row.original} />,
    }),
    columnHelper.display({
      id: "last-run",
      header: "Last Run Result",
      cell: ({ row }) => <LastRunCell pool={row.original} />,
    }),
    columnHelper.display({
      id: "actions",
      header: "Actions",
      meta: { headerClassName: "text-right", cellClassName: "text-right" },
      cell: ({ row }) => <ActionsCell pool={row.original} />,
    }),
  ]);
