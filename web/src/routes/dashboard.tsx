import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "cn";
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
import {
  useSystemStats,
  usePools,
  useJobHistory,
  useImageUpdates,
  useAuthProfiles,
} from "../lib/api/query-hooks";
import { QueueLatencyChart } from "../components/analytics/queue-latency-chart";
import { SuccessFailureWidget } from "../components/analytics/success-failure-widget";
import { ImageUpdateNotification } from "../components/notifications/image-update-notification";
import {
  Activity,
  CheckCircle2,
  Clock,
  Server,
  XCircle,
  Terminal,
  AlertTriangle,
} from "lucide-react";
import { Link } from "@tanstack/react-router";
import { PoolHealthBadge } from "../components/pools/pool-health-badge";
import { poolTargetList, TargetCountBadge } from "../components/pools/pool-targets";
import { PoolHealthStatus } from "../gen/api_pb";

function formatDuration(seconds: number): string {
  if (!seconds || seconds <= 0) return "—";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const mins = Math.floor(seconds / 60);
  const remSec = Math.round(seconds % 60);
  if (mins < 60) return `${mins}m ${remSec}s`;
  const hours = Math.floor(mins / 60);
  const remMins = mins % 60;
  return `${hours}h ${remMins}m`;
}

function formatTimestamp(isoString?: string): string {
  if (!isoString) return "—";
  try {
    const d = new Date(isoString);
    if (isNaN(d.getTime())) return "—";
    return d.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  } catch {
    return isoString;
  }
}

export function DashboardPage() {
  const [timeframeHours, setTimeframeHours] = useState(24);

  const { data: stats, isLoading: statsLoading } = useSystemStats(timeframeHours);
  const { data: pools, isLoading: poolsLoading } = usePools();
  const { data: authProfiles } = useAuthProfiles();
  const { data: history } = useJobHistory({ limit: 5 });
  const { data: updates } = useImageUpdates();

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
    <div className="space-y-8">
      {/* Header */}
      <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
            Dashboard Overview
          </h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">
            Real-time supervisor metrics, queue wait-time analytics, runner capacity, and execution
            health.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <Link
            to="/pools"
            className="inline-flex items-center gap-1.5 rounded-xl border border-slate-200 bg-white px-3.5 py-2 text-xs font-semibold text-slate-700 shadow-xs hover:bg-slate-50 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800 transition-colors"
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
        <div className="flex items-start gap-3 rounded-2xl border border-rose-200 bg-rose-50/80 p-4 text-xs text-rose-900 dark:border-rose-900/50 dark:bg-rose-950/30 dark:text-rose-200 shadow-xs">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-rose-600 dark:text-rose-400" />
          <div className="flex-1 min-w-0 space-y-1">
            <p className="font-semibold text-rose-900 dark:text-rose-100">
              {degradedPools.length === 1
                ? `Runner Pool "${degradedPools[0].name}" is Degraded`
                : `${degradedPools.length} Runner Pools are Degraded`}
            </p>
            <p className="text-[11px] text-rose-700 dark:text-rose-300">
              Runner provisioning or reconciliation encountered errors. Inspect diagnostics to
              resolve configuration or credential issues.
            </p>
            {degradedPools.length === 1 && degradedPools[0].lastError && (
              <p className="mt-1 font-mono text-[11px] text-rose-800 dark:text-rose-300 truncate">
                {degradedPools[0].lastError}
              </p>
            )}
          </div>
          <Link
            to={degradedPools.length === 1 ? "/pools/$poolId" : "/pools"}
            params={
              degradedPools.length === 1 ? { poolId: degradedPools[0].id.toString() } : undefined
            }
            className="shrink-0 rounded-xl bg-rose-600 px-3 py-1.5 text-xs font-semibold text-white shadow-xs transition-colors hover:bg-rose-700 dark:bg-rose-500 dark:hover:bg-rose-600"
          >
            Inspect Diagnostics &rarr;
          </Link>
        </div>
      )}

      {/* Primary KPI Cards */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card size="sm">
          <CardContent>
            <div className="flex items-center justify-between text-xs font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
              <span>Active Runners</span>
              <Activity className="h-4 w-4 text-emerald-500" />
            </div>
            <div className="mt-2 text-3xl font-extrabold tracking-tight text-slate-900 dark:text-white font-mono">
              {statsLoading ? "..." : `${stats?.totalActiveRunners ?? 0} active`}
            </div>
            <div className="mt-1 text-xs text-slate-400">
              {stats?.totalIdleRunners ?? 0} warm idle standby
            </div>
          </CardContent>
        </Card>

        <Card size="sm">
          <CardContent>
            <div className="flex items-center justify-between text-xs font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
              <span>{timeframeHours}h Jobs Executed</span>
              <Server className="h-4 w-4 text-blue-500" />
            </div>
            <div className="mt-2 text-3xl font-extrabold tracking-tight text-slate-900 dark:text-white font-mono">
              {statsLoading ? "..." : totalJobs}
            </div>
            <div className="mt-1 text-xs text-slate-400">
              {avgQueueSeconds === null
                ? "Queue timing requires webhooks"
                : `Avg queue wait: ${avgQueueSeconds.toFixed(1)}s`}
            </div>
          </CardContent>
        </Card>

        <Card size="sm">
          <CardContent>
            <div className="flex items-center justify-between text-xs font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
              <span>Success Rate</span>
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
            </div>
            <div className="mt-2 text-3xl font-extrabold tracking-tight text-slate-900 dark:text-white font-mono">
              {statsLoading ? "..." : successRate === null ? "—" : `${successRate.toFixed(1)}%`}
            </div>
            <div className="mt-1 text-xs text-slate-400">
              {knownOutcomeJobs === 0
                ? "No concluded jobs in window"
                : `${successfulJobs} passed / ${failedJobs} failed`}
            </div>
          </CardContent>
        </Card>

        <Card size="sm">
          <CardContent>
            <div className="flex items-center justify-between text-xs font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
              <span>Avg Runtime</span>
              <Clock className="h-4 w-4 text-indigo-500" />
            </div>
            <div className="mt-2 text-3xl font-extrabold tracking-tight text-slate-900 dark:text-white font-mono">
              {statsLoading ? "..." : formatDuration(avgRuntimeSeconds)}
            </div>
            <div className="mt-1 text-xs text-slate-400">Job completion duration</div>
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
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-lg font-bold text-slate-900 dark:text-white">
              Configured Runner Pools
            </h2>
            <p className="text-xs text-slate-500 dark:text-slate-400">
              Active dynamic scaling pools and container resource allocations.
            </p>
          </div>
          <Link
            to="/pools"
            className="text-xs font-semibold text-blue-600 hover:underline dark:text-blue-400"
          >
            View All Pools &rarr;
          </Link>
        </div>

        {poolsLoading ? (
          <div className="text-sm text-slate-400">Loading pools...</div>
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
                className="group rounded-2xl border border-slate-200 bg-white p-5 shadow-xs transition-all hover:border-blue-400 hover:shadow-md dark:border-slate-800 dark:bg-slate-900 dark:hover:border-blue-600"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="font-semibold text-slate-900 group-hover:text-blue-600 dark:text-white dark:group-hover:text-blue-400 truncate">
                    {p.name}
                  </span>
                  <div className="flex items-center gap-1.5 shrink-0">
                    <PoolHealthBadge status={p.healthStatus} size="sm" />
                    <span className="rounded-md bg-slate-100 px-2 py-0.5 text-xs font-semibold uppercase tracking-wider text-slate-600 dark:bg-slate-800 dark:text-slate-400">
                      {p.provider}
                    </span>
                  </div>
                </div>
                {poolTargetList(p).length > 1 && (
                  <div className="mt-2 flex items-center text-xs text-slate-500 dark:text-slate-400">
                    <TargetCountBadge pool={p} />
                  </div>
                )}
                {p.currentIntent && (
                  <div className="mt-1.5 text-[11px] text-slate-500 dark:text-slate-400 italic truncate">
                    {p.currentIntent}
                  </div>
                )}
                <div className="mt-4 flex items-center justify-between border-t border-slate-100 pt-3 text-xs text-slate-600 dark:border-slate-800 dark:text-slate-400">
                  <span>
                    Active:{" "}
                    <strong className="text-slate-900 dark:text-white">{p.activeRunners}</strong>
                  </span>
                  <span>
                    Idle Target:{" "}
                    <strong
                      className={
                        p.healthStatus === PoolHealthStatus.DEGRADED && p.minIdleRunners > 0
                          ? "text-rose-600 dark:text-rose-400"
                          : "text-slate-900 dark:text-white"
                      }
                    >
                      {p.minIdleRunners}
                    </strong>
                  </span>
                  <span>
                    Max:{" "}
                    <strong className="text-slate-900 dark:text-white">{p.maxConcurrency}</strong>
                  </span>
                </div>
              </Link>
            ))}
          </div>
        )}
      </div>

      {/* Recent History */}
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-lg font-bold text-slate-900 dark:text-white">Recent Executions</h2>
            <p className="text-xs text-slate-500 dark:text-slate-400">
              Latest ephemeral workflow jobs completed across runner pools.
            </p>
          </div>
          <Link
            to="/history"
            className="text-xs font-semibold text-blue-600 hover:underline dark:text-blue-400"
          >
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
          <div className="overflow-hidden rounded-2xl border bg-card shadow-xs">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Status</TableHead>
                  <TableHead>Runner Name</TableHead>
                  <TableHead>Duration</TableHead>
                  <TableHead>Queue Wait</TableHead>
                  <TableHead>Completed At</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {history.jobs.map((job) => (
                  <TableRow key={job.id.toString()}>
                    <TableCell>
                      <Badge
                        className={cn(
                          "uppercase tracking-wider",
                          job.status === "success"
                            ? "border-success/30 bg-success/10 text-success"
                            : "border-destructive/30 bg-destructive/10 text-destructive",
                        )}
                      >
                        {job.status === "success" ? (
                          <CheckCircle2 className="size-3" />
                        ) : (
                          <XCircle className="size-3" />
                        )}
                        {job.status}
                      </Badge>
                    </TableCell>
                    <TableCell className="font-mono font-medium">{job.runnerName}</TableCell>
                    <TableCell className="font-mono">
                      {formatDuration(job.durationSeconds)}
                    </TableCell>
                    <TableCell className="font-mono text-muted-foreground">
                      {job.queueTimeSeconds > 0 ? `${job.queueTimeSeconds.toFixed(1)}s` : "—"}
                    </TableCell>
                    <TableCell className="font-mono text-muted-foreground">
                      {formatTimestamp(job.completedAt)}
                    </TableCell>
                    <TableCell className="text-right">
                      <LinkButton
                        to="/history/$jobId"
                        params={{ jobId: job.id.toString() }}
                        variant="outline"
                        size="xs"
                      >
                        <Terminal data-icon="inline-start" />
                        <span>Logs</span>
                      </LinkButton>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
    </div>
  );
}
