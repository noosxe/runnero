import { useState } from "react";
import { DataTable, useAppTable } from "../lib/tables";
import { Alert, AlertAction, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
  EmptyDescription,
  EmptyContent,
} from "@/components/ui/empty";
import { LinkButton } from "../lib/link-button";
import {
  DEFAULT_STATS_TIMEFRAME_HOURS,
  useSystemStats,
  usePools,
  useJobHistory,
  useImageUpdates,
  useAuthProfiles,
  useIsAdmin,
} from "../lib/api/query-hooks";
import { QueueLatencyChart } from "../components/analytics/queue-latency-chart";
import { SuccessFailureWidget } from "../components/analytics/success-failure-widget";
import { ImageUpdateNotification } from "../components/notifications/image-update-notification";
import { Activity, CheckCircle2, Clock, Server, AlertTriangle } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { PoolHealthBadge } from "../components/pools/pool-health-badge";
import { poolTargetList, TargetCountBadge } from "../components/pools/pool-targets";
import { PoolHealthStatus } from "../gen/api_pb";

import { recentJobsColumns, formatDuration } from "./dashboard-columns";

export function DashboardPage() {
  const [timeframeHours, setTimeframeHours] = useState(DEFAULT_STATS_TIMEFRAME_HOURS);
  const isAdmin = useIsAdmin();

  const { data: stats, isLoading: statsLoading } = useSystemStats(timeframeHours);
  const { data: pools, isLoading: poolsLoading } = usePools();
  // Admin-bucket read: only fired for admins (docs/35 section 2.2).
  const { data: authProfiles } = useAuthProfiles(isAdmin);
  const { data: history } = useJobHistory({ limit: 5 });
  const { data: updates } = useImageUpdates();

  // Recent executions table (docs/31 §5 phase 1): hook lives at the
  // component top level, never inside conditional JSX.
  const recentJobsTable = useAppTable({
    columns: recentJobsColumns(),
    data: history?.jobs ?? [],
    getRowId: (job) => job.id.toString(),
  });

  const hasAuthProfiles = Boolean(authProfiles && authProfiles.length > 0);
  const degradedPools = pools?.filter((p) => p.healthStatus === PoolHealthStatus.DEGRADED) ?? [];

  const totalJobs = stats?.totalJobs24h ?? 0;
  const successfulJobs = stats?.successfulJobs24h ?? 0;
  const failedJobs = stats?.failedJobs24h ?? 0;
  // docs/21 §5.6: degrade truthfully — no fabricated 100%/0 when the
  // denominator is empty. `null` renders as "—" in the KPI cards.
  const knownOutcomeJobs = Number(stats?.knownOutcomeJobs ?? 0n);
  const queueTimedJobs = Number(stats?.queueTimedJobs ?? 0n);
  const successRate = knownOutcomeJobs > 0 ? (stats?.successRatePercent ?? 0) : null;
  const avgQueueSeconds = queueTimedJobs > 0 ? (stats?.averageQueueTimeSeconds ?? 0) : null;
  const avgRuntimeSeconds = stats?.averageRuntimeSeconds ?? 0;
  const trend = stats?.queueLatencyTrend ?? [];

  const poolNameLookup: Record<string, string> = {};
  if (pools) {
    for (const p of pools) {
      poolNameLookup[p.id.toString()] = p.name;
    }
  }

  return (
    <div className="flex flex-col gap-8">
      {/* Header */}
      <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground ">Dashboard Overview</h1>
          <p className="text-sm text-muted-foreground ">
            Real-time supervisor metrics, queue wait-time analytics, runner capacity, and execution
            health.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <Link
            to="/pools"
            className="inline-flex items-center gap-1.5 rounded-xl border border-border bg-card px-3.5 py-2 text-xs font-semibold text-foreground shadow-xs hover:bg-muted/50 transition-colors"
          >
            <span>Manage Pools</span>
          </Link>
        </div>
      </div>

      {/* Pending Image Updates Banner */}
      {updates && updates.length > 0 && (
        <ImageUpdateNotification updates={updates} poolNameLookup={poolNameLookup} />
      )}

      {/* Degraded Pool Warning Banner */}
      {degradedPools.length > 0 && (
        <Alert variant="destructive">
          <AlertTriangle />
          <AlertTitle className="font-semibold">
            {degradedPools.length === 1
              ? `Runner Pool "${degradedPools[0].name}" is Degraded`
              : `${degradedPools.length} Runner Pools are Degraded`}
          </AlertTitle>
          <AlertDescription className="text-xs">
            Runner provisioning or reconciliation encountered errors. Inspect diagnostics to resolve
            configuration or credential issues.
          </AlertDescription>
          {degradedPools.length === 1 && degradedPools[0].lastError && (
            <p className="mt-1 truncate font-mono text-[11px] text-destructive/90">
              {degradedPools[0].lastError}
            </p>
          )}
          <AlertAction>
            <LinkButton
              to={degradedPools.length === 1 ? "/pools/$poolId" : "/pools"}
              params={
                degradedPools.length === 1 ? { poolId: degradedPools[0].id.toString() } : undefined
              }
              variant="destructive"
              size="sm"
            >
              Inspect Diagnostics &rarr;
            </LinkButton>
          </AlertAction>
        </Alert>
      )}

      {/* Primary KPI Cards */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card size="sm">
          <CardContent>
            <div className="flex items-center justify-between text-xs font-semibold uppercase tracking-wider text-muted-foreground ">
              <span>Active Runners</span>
              <Activity className="size-4 text-success" />
            </div>
            <div className="mt-2 font-mono text-3xl font-extrabold tracking-tight text-foreground ">
              {statsLoading ? (
                <Skeleton className="inline-block h-9 w-24 align-middle" />
              ) : (
                `${stats?.totalActiveRunners ?? 0} active`
              )}
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {stats?.totalIdleRunners ?? 0} warm idle standby
            </div>
          </CardContent>
        </Card>

        <Card size="sm">
          <CardContent>
            <div className="flex items-center justify-between text-xs font-semibold uppercase tracking-wider text-muted-foreground ">
              <span>{timeframeHours}h Jobs Executed</span>
              <Server className="size-4 text-primary" />
            </div>
            <div className="mt-2 text-3xl font-extrabold tracking-tight text-foreground font-mono">
              {statsLoading ? (
                <Skeleton className="inline-block h-9 w-24 align-middle" />
              ) : (
                totalJobs
              )}
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {avgQueueSeconds === null
                ? "Queue timing requires webhooks"
                : `Avg queue wait: ${avgQueueSeconds.toFixed(1)}s`}
            </div>
          </CardContent>
        </Card>

        <Card size="sm">
          <CardContent>
            <div className="flex items-center justify-between text-xs font-semibold uppercase tracking-wider text-muted-foreground ">
              <span>Success Rate</span>
              <CheckCircle2 className="size-4 text-success" />
            </div>
            <div className="mt-2 text-3xl font-extrabold tracking-tight text-foreground font-mono">
              {statsLoading ? (
                <Skeleton className="inline-block h-9 w-24 align-middle" />
              ) : successRate === null ? (
                "—"
              ) : (
                `${successRate.toFixed(1)}%`
              )}
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {knownOutcomeJobs === 0
                ? "No concluded jobs in window"
                : `${successfulJobs} passed / ${failedJobs} failed`}
            </div>
          </CardContent>
        </Card>

        <Card size="sm">
          <CardContent>
            <div className="flex items-center justify-between text-xs font-semibold uppercase tracking-wider text-muted-foreground ">
              <span>Avg Runtime</span>
              <Clock className="size-4 text-primary" />
            </div>
            <div className="mt-2 text-3xl font-extrabold tracking-tight text-foreground font-mono">
              {statsLoading ? (
                <Skeleton className="inline-block h-9 w-24 align-middle" />
              ) : (
                formatDuration(avgRuntimeSeconds)
              )}
            </div>
            <div className="mt-1 text-xs text-muted-foreground">Job completion duration</div>
          </CardContent>
        </Card>
      </div>

      {/* Analytics Graphs Section */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <QueueLatencyChart
            trend={trend}
            averageQueueSeconds={avgQueueSeconds ?? 0}
            timeframeHours={timeframeHours}
            onTimeframeChange={setTimeframeHours}
          />
        </div>

        <div className="lg:col-span-1">
          <SuccessFailureWidget
            totalJobs={totalJobs}
            successfulJobs={successfulJobs}
            failedJobs={failedJobs}
            successRatePercent={successRate}
            averageRuntimeSeconds={avgRuntimeSeconds}
          />
        </div>
      </div>

      {/* Pools Summary */}
      <div className="flex flex-col gap-4">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-lg font-bold text-foreground ">Configured Runner Pools</h2>
            <p className="text-xs text-muted-foreground ">
              Active dynamic scaling pools and container resource allocations.
            </p>
          </div>
          <Link to="/pools" className="text-xs font-semibold text-primary hover:underline ">
            View All Pools &rarr;
          </Link>
        </div>

        {poolsLoading ? (
          <div className="flex flex-col gap-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : !pools || pools.length === 0 ? (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Server />
              </EmptyMedia>
              <EmptyTitle>No runner pools configured yet</EmptyTitle>
              <EmptyDescription>
                {hasAuthProfiles
                  ? "Git credentials are connected. Create your first auto-scaling runner pool to begin processing CI workflow runs."
                  : "To start dispatching ephemeral runner containers, connect a Git provider authentication profile and create your first runner pool."}
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <div className="flex items-center justify-center gap-3">
                <LinkButton to={hasAuthProfiles ? "/pools" : "/profiles"} size="sm">
                  {hasAuthProfiles ? "Create First Pool" : "Connect Git Provider"}
                </LinkButton>
                <LinkButton to="/pools" variant="outline" size="sm">
                  Manage Pools
                </LinkButton>
              </div>
            </EmptyContent>
          </Empty>
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {pools.map((p) => (
              <Link
                key={p.id.toString()}
                to="/pools/$poolId"
                params={{ poolId: p.id.toString() }}
                className="group"
              >
                <Card className="transition-all hover:ring-primary/40 hover:shadow-md">
                  <CardContent>
                    <div className="flex items-center justify-between gap-2">
                      <span className="truncate font-semibold text-foreground group-hover:text-primary">
                        {p.name}
                      </span>
                      <div className="flex shrink-0 items-center gap-1.5">
                        <PoolHealthBadge status={p.healthStatus} size="sm" />
                        <span className="rounded-md bg-muted px-2 py-0.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                          {p.provider}
                        </span>
                      </div>
                    </div>
                    {poolTargetList(p).length > 1 && (
                      <div className="mt-2 flex items-center text-xs text-muted-foreground">
                        <TargetCountBadge pool={p} />
                      </div>
                    )}
                    {p.currentIntent && (
                      <div className="mt-1.5 truncate text-[11px] italic text-muted-foreground">
                        {p.currentIntent}
                      </div>
                    )}
                    <div className="mt-4 flex items-center justify-between border-t border-border/60 pt-3 text-xs text-muted-foreground">
                      <span>
                        Active: <strong className="text-foreground">{p.activeRunners}</strong>
                      </span>
                      <span>
                        Idle Target:{" "}
                        <strong
                          className={
                            p.healthStatus === PoolHealthStatus.DEGRADED && p.minIdleRunners > 0
                              ? "text-destructive"
                              : "text-foreground"
                          }
                        >
                          {p.minIdleRunners}
                        </strong>
                      </span>
                      <span>
                        Max: <strong className="text-foreground">{p.maxConcurrency}</strong>
                      </span>
                    </div>
                  </CardContent>
                </Card>
              </Link>
            ))}
          </div>
        )}
      </div>

      {/* Recent History */}
      <div className="flex flex-col gap-4">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-lg font-bold text-foreground ">Recent Executions</h2>
            <p className="text-xs text-muted-foreground ">
              Latest ephemeral workflow jobs completed across runner pools.
            </p>
          </div>
          <Link to="/history" className="text-xs font-semibold text-primary hover:underline ">
            View Full History &rarr;
          </Link>
        </div>

        {!history?.jobs || history.jobs.length === 0 ? (
          <Empty>
            <EmptyHeader>
              <EmptyTitle>No executions recorded in the last 24h.</EmptyTitle>
            </EmptyHeader>
          </Empty>
        ) : (
          <Card className="py-0">
            <DataTable table={recentJobsTable} />
          </Card>
        )}
      </div>
    </div>
  );
}
