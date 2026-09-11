import { useState } from "react";
import { Checkbox } from "@/components/ui/checkbox";
import { FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { cn } from "cn";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogMedia,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { useParams, Link } from "@tanstack/react-router";
import {
  usePools,
  useRunners,
  useTerminateRunner,
  useAuthProfiles,
  useSession,
  useRenovateStatus,
  useRenovateHistory,
  useTriggerRenovateRun,
  useUpdatePool,
  useCheckImageUpdate,
  useImageUpdates,
  usePullImage,
} from "../lib/api/query-hooks";
import { useWatchRunners, useStreamRunnerLogs } from "../lib/api/streaming-hooks";
import { LogTerminal } from "../components/terminal/log-terminal";
import { PoolHealthBadge } from "../components/pools/pool-health-badge";
import { PoolStatusBanner } from "../components/pools/pool-status-banner";
import { PoolDiagnosticsCard } from "../components/pools/pool-diagnostics-card";
import { PoolPollStatus } from "../components/pools/pool-poll-status";
import { poolTargetList, TargetCountBadge } from "../components/pools/pool-targets";
import { PoolDeleteModal } from "../components/pools/pool-delete-modal";
import { PoolWizardModal } from "../components/pools/pool-wizard-modal";
import { PoolHealthStatus, type RunnerInstance, type Pool } from "../gen/api_pb";
import {
  ArrowLeft,
  Server,
  Activity,
  Cpu,
  HardDrive,
  Shield,
  Clock,
  Terminal,
  Trash2,
  AlertTriangle,
  Pencil,
  Bot,
  Play,
  CheckCircle2,
  XCircle,
  Loader2,
  Save,
  RefreshCw,
  DownloadCloud,
  Copy,
  Check,
} from "lucide-react";

function formatUptime(seconds: number | bigint): string {
  const sec = Number(seconds);
  if (sec < 60) return `${sec}s`;
  const mins = Math.floor(sec / 60);
  const remSec = sec % 60;
  if (mins < 60) return `${mins}m ${remSec}s`;
  const hours = Math.floor(mins / 60);
  const remMins = mins % 60;
  return `${hours}h ${remMins}m`;
}

export function PoolDetailPage() {
  const { poolId } = useParams({ strict: false }) as { poolId?: string };
  const poolIdBigInt = poolId ? BigInt(poolId) : 0n;

  const { data: pools } = usePools();
  const pool = pools?.find((p) => p.id === poolIdBigInt);
  const { data: authProfiles } = useAuthProfiles();
  const { data: session } = useSession();

  const { data: runners, isLoading: runnersLoading } = useRunners(poolIdBigInt);
  const { isConnected: isStreamActive } = useWatchRunners(poolIdBigInt);

  const [activeTab, setActiveTab] = useState<"runners" | "config" | "renovate">("runners");
  const [isEditModalOpen, setIsEditModalOpen] = useState(false);
  const [isDeleteModalOpen, setIsDeleteModalOpen] = useState(false);
  const [selectedRunnerForLogs, setSelectedRunnerForLogs] = useState<RunnerInstance | null>(null);
  const [runnerToTerminate, setRunnerToTerminate] = useState<RunnerInstance | null>(null);
  const [labelsCopied, setLabelsCopied] = useState(false);

  // GitHub Actions accepts a comma-separated label list on runs-on; copy the
  // pool's labels in exactly that shape so they paste straight into a
  // workflow file. Falls back to execCommand because the control plane is
  // commonly served over plain HTTP on a LAN/tailnet, where the async
  // Clipboard API is unavailable (not a secure context).
  const copyRunnerLabels = () => {
    if (!pool?.labels || pool.labels.length === 0) return;
    const text = pool.labels.join(", ");
    const done = () => {
      setLabelsCopied(true);
      window.setTimeout(() => setLabelsCopied(false), 2000);
    };
    if (navigator.clipboard?.writeText) {
      navigator.clipboard
        .writeText(text)
        .then(done)
        .catch(() => {});
      return;
    }
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    try {
      if (document.execCommand("copy")) done();
    } finally {
      document.body.removeChild(ta);
    }
  };

  const terminateMutation = useTerminateRunner();
  const { data: updates } = useImageUpdates();
  const checkUpdateMutation = useCheckImageUpdate();
  const pullImageMutation = usePullImage();
  const poolUpdate = updates?.find((u) => u.poolId === poolIdBigInt);

  const handleConfirmTerminate = async () => {
    if (!runnerToTerminate || !pool) return;
    try {
      await terminateMutation.mutateAsync({
        poolId: pool.id,
        containerId: runnerToTerminate.containerId,
      });
      setRunnerToTerminate(null);
    } catch (err) {
      console.error("Failed to terminate runner:", err);
    }
  };

  if (!pool) {
    return (
      <div className="space-y-4">
        <Link
          to="/pools"
          className="inline-flex items-center gap-1.5 text-xs font-semibold text-blue-600 hover:underline"
        >
          <ArrowLeft className="h-3.5 w-3.5" /> Back to Pools
        </Link>
        <div className="rounded-2xl border border-slate-200 bg-white p-12 text-center text-sm text-slate-500 dark:border-slate-800 dark:bg-slate-900">
          Pool not found or loading...
        </div>
      </div>
    );
  }

  const activeInstances = runners?.filter((r) => r.status === "busy").length ?? 0;
  const idleInstances = runners?.filter((r) => r.status === "idle").length ?? 0;

  return (
    <div className="space-y-6">
      {/* Navigation & Header */}
      <div>
        <Link
          to="/pools"
          className="inline-flex items-center gap-1.5 text-xs font-semibold text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-200 transition-colors mb-3"
        >
          <ArrowLeft className="h-3.5 w-3.5" /> Back to Runner Pools
        </Link>

        <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <div className="flex items-center gap-3">
              <h1 className="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
                {pool.name}
              </h1>
              <PoolHealthBadge status={pool.healthStatus} size="md" />
              <Badge
                className={cn(
                  "border",
                  isStreamActive
                    ? "border-success/30 bg-success/10 text-success"
                    : "border-warning/30 bg-warning/10 text-warning",
                )}
              >
                <span
                  className={`h-1.5 w-1.5 rounded-full ${
                    isStreamActive ? "bg-emerald-500 animate-pulse" : "bg-amber-500"
                  }`}
                />
                <span className="font-mono text-[10px]">
                  {isStreamActive ? "Live Orchestrator Stream" : "Connecting"}
                </span>
              </Badge>
            </div>
            {poolTargetList(pool).length > 1 && (
              <div className="mt-1">
                <TargetCountBadge pool={pool} />
              </div>
            )}
          </div>

          <div className="flex items-center gap-2">
            <span className="rounded-md bg-blue-50 px-2.5 py-1 text-xs font-semibold text-blue-700 uppercase tracking-wider dark:bg-blue-950/50 dark:text-blue-400 border border-blue-200 dark:border-blue-900">
              {pool.provider}
            </span>
            <span className="rounded-md bg-slate-100 px-2.5 py-1 text-xs font-semibold text-slate-600 uppercase tracking-wider dark:bg-slate-800 dark:text-slate-300">
              {pool.scope || "repo"}
            </span>
          </div>
        </div>
      </div>

      {/* Operational Status & Diagnostics */}
      <PoolStatusBanner pool={pool} />
      <PoolDiagnosticsCard pool={pool} />
      <PoolPollStatus pool={pool} />

      {/* KPI Stats Strip */}
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <div className="rounded-2xl border border-slate-200 bg-white p-4 shadow-xs dark:border-slate-800 dark:bg-slate-900">
          <span className="text-xs font-medium text-slate-500 dark:text-slate-400">
            Active Running Jobs
          </span>
          <div className="mt-1 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-slate-900 dark:text-white">
              {activeInstances}
            </span>
            <span className="text-xs text-slate-400">of {pool.maxConcurrency} max</span>
          </div>
        </div>

        <div className="rounded-2xl border border-slate-200 bg-white p-4 shadow-xs dark:border-slate-800 dark:bg-slate-900">
          <span className="text-xs font-medium text-slate-500 dark:text-slate-400">
            Idle Warm Pool
          </span>
          <div className="mt-1 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-slate-900 dark:text-white">
              {idleInstances}
            </span>
            <span
              className={`text-xs ${
                pool.healthStatus === PoolHealthStatus.DEGRADED &&
                pool.minIdleRunners > 0 &&
                idleInstances === 0
                  ? "font-medium text-rose-600 dark:text-rose-400"
                  : pool.healthStatus === PoolHealthStatus.PROVISIONING &&
                      idleInstances < pool.minIdleRunners
                    ? "font-medium text-amber-600 dark:text-amber-400"
                    : "text-slate-400"
              }`}
            >
              target: {pool.minIdleRunners}
              {pool.healthStatus === PoolHealthStatus.DEGRADED &&
              pool.minIdleRunners > 0 &&
              idleInstances === 0
                ? " (Reconciliation Failed)"
                : pool.healthStatus === PoolHealthStatus.PROVISIONING &&
                    idleInstances < pool.minIdleRunners
                  ? " (Provisioning...)"
                  : ""}
            </span>
          </div>
        </div>

        <div className="rounded-2xl border border-slate-200 bg-white p-4 shadow-xs dark:border-slate-800 dark:bg-slate-900">
          <span className="text-xs font-medium text-slate-500 dark:text-slate-400">
            Resource Quotas
          </span>
          <div className="mt-1 flex items-center gap-3 text-xs font-semibold text-slate-800 dark:text-slate-200">
            <span className="inline-flex items-center gap-1">
              <Cpu className="h-3.5 w-3.5 text-slate-400" />
              {pool.cpuLimit || "Unlimited"}
            </span>
            <span className="inline-flex items-center gap-1">
              <HardDrive className="h-3.5 w-3.5 text-slate-400" />
              {pool.memoryLimit || "Unlimited"}
            </span>
          </div>
        </div>

        <div className="rounded-2xl border border-slate-200 bg-white p-4 shadow-xs dark:border-slate-800 dark:bg-slate-900">
          <span className="text-xs font-medium text-slate-500 dark:text-slate-400">
            Docker Privileges
          </span>
          <div className="mt-1 flex items-center gap-1.5 text-xs font-semibold text-slate-800 dark:text-slate-200">
            <Shield className="h-3.5 w-3.5 text-slate-400" />
            <span>{pool.allowDocker ? "Docker Daemon Enabled" : "Rootless Isolation"}</span>
          </div>
        </div>
      </div>

      {/* Navigation Tabs */}
      <div className="flex border-b border-slate-200 dark:border-slate-800">
        <button
          type="button"
          onClick={() => setActiveTab("runners")}
          className={`flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors ${
            activeTab === "runners"
              ? "border-blue-600 text-blue-600 dark:border-blue-500 dark:text-blue-400"
              : "border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200"
          }`}
        >
          <Activity className="h-3.5 w-3.5" />
          <span>Active Containers & Runners</span>
          <span className="rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-800 dark:text-slate-300">
            {runners?.length ?? 0}
          </span>
        </button>

        <button
          type="button"
          onClick={() => setActiveTab("config")}
          className={`flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors ${
            activeTab === "config"
              ? "border-blue-600 text-blue-600 dark:border-blue-500 dark:text-blue-400"
              : "border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200"
          }`}
        >
          <Server className="h-3.5 w-3.5" />
          <span>Pool Configuration</span>
        </button>

        <button
          type="button"
          onClick={() => setActiveTab("renovate")}
          className={`flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors ${
            activeTab === "renovate"
              ? "border-blue-600 text-blue-600 dark:border-blue-500 dark:text-blue-400"
              : "border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200"
          }`}
        >
          <Bot className="h-3.5 w-3.5" />
          <span>Renovate Bot</span>
          {pool.renovate?.enabled && <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />}
        </button>
      </div>

      {/* Tab Content: Runners & Containers Table */}
      {activeTab === "runners" && (
        <div className="space-y-4">
          {runnersLoading ? (
            <div className="rounded-2xl border border-slate-200 bg-white p-8 text-center text-xs text-slate-400 dark:border-slate-800 dark:bg-slate-900">
              Auditing active container instances...
            </div>
          ) : !runners || runners.length === 0 ? (
            <div className="rounded-2xl border border-dashed border-slate-300 p-12 text-center text-slate-500 dark:border-slate-800 dark:text-slate-400">
              <Server className="mx-auto h-8 w-8 text-slate-400 mb-2" />
              <p className="text-sm font-semibold text-slate-800 dark:text-slate-200">
                No active container instances
              </p>
              <p className="text-xs text-slate-500 mt-1 max-w-sm mx-auto">
                No runners are currently executing or warming in this pool. Ephemeral instances will
                automatically spawn when workflow jobs are queued.
              </p>
            </div>
          ) : (
            <div className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
              <div className="overflow-x-auto">
                <table className="w-full text-left text-xs text-slate-600 dark:text-slate-300">
                  <thead className="border-b border-slate-100 bg-slate-50 font-semibold uppercase tracking-wider text-[11px] text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
                    <tr>
                      <th className="px-5 py-3">Container ID</th>
                      <th className="px-5 py-3">Runner Name</th>
                      <th className="px-5 py-3">State</th>
                      <th className="px-5 py-3">IP Address</th>
                      <th className="px-5 py-3">Uptime</th>
                      <th className="px-5 py-3">CPU / Mem Limit</th>
                      <th className="px-5 py-3 text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {runners.map((r) => {
                      const isBusy = r.status === "busy";
                      const isIdle = r.status === "idle";
                      const isDegraded = r.status === "degraded";

                      return (
                        <tr
                          key={r.containerId}
                          className="hover:bg-slate-50/50 dark:hover:bg-slate-800/50 transition-colors"
                        >
                          <td className="px-5 py-3.5 font-mono text-[11px] font-semibold text-slate-900 dark:text-white">
                            {r.containerId.substring(0, 12)}
                          </td>
                          <td className="px-5 py-3.5 font-mono font-medium text-slate-800 dark:text-slate-200">
                            {r.name}
                          </td>
                          <td className="px-5 py-3.5">
                            <Badge
                              className={cn(
                                "uppercase tracking-wider",
                                isBusy
                                  ? "border-success/30 bg-success/10 text-success"
                                  : isIdle
                                    ? "border-primary/30 bg-primary/10 text-primary"
                                    : isDegraded
                                      ? "border-destructive/30 bg-destructive/10 text-destructive"
                                      : "bg-muted text-muted-foreground",
                              )}
                            >
                              <span
                                className={`h-1.5 w-1.5 rounded-full ${
                                  isBusy
                                    ? "bg-emerald-500 animate-pulse"
                                    : isIdle
                                      ? "bg-sky-500"
                                      : isDegraded
                                        ? "bg-rose-500"
                                        : "bg-slate-400"
                                }`}
                              />
                              <span>{r.status}</span>
                            </Badge>
                          </td>
                          <td className="px-5 py-3.5 font-mono text-slate-500 dark:text-slate-400">
                            {r.ipAddress || "—"}
                          </td>
                          <td className="px-5 py-3.5 font-mono text-slate-600 dark:text-slate-300">
                            <span className="inline-flex items-center gap-1">
                              <Clock className="h-3 w-3 text-slate-400" />
                              {formatUptime(r.uptimeSeconds)}
                            </span>
                          </td>
                          <td className="px-5 py-3.5 text-slate-500">
                            {r.cpuLimit || pool.cpuLimit || "Unlimited"} /{" "}
                            {r.memoryLimit || pool.memoryLimit || "Unlimited"}
                          </td>
                          <td className="px-5 py-3.5 text-right">
                            <div className="flex items-center justify-end gap-2">
                              <Button
                                variant="outline"
                                size="xs"
                                onClick={() => setSelectedRunnerForLogs(r)}
                              >
                                <Terminal data-icon="inline-start" />
                                <span>Logs</span>
                              </Button>
                              <Button
                                variant="destructive"
                                size="xs"
                                onClick={() => setRunnerToTerminate(r)}
                              >
                                <Trash2 data-icon="inline-start" />
                                <span>Terminate</span>
                              </Button>
                            </div>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </div>
      )}

      {/* Tab Content: Configuration */}
      {activeTab === "config" && (
        <div className="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-6">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-base font-bold text-slate-900 dark:text-white">
              Pool Parameters & Resource Limits
            </h2>
            <div className="flex items-center gap-2">
              <Button
                size="xs"
                aria-label="Edit pool configuration"
                onClick={() => setIsEditModalOpen(true)}
              >
                <Pencil data-icon="inline-start" />
                <span>Edit Configuration</span>
              </Button>
              <Button
                variant="destructive"
                size="xs"
                aria-label="Delete pool"
                onClick={() => setIsDeleteModalOpen(true)}
              >
                <Trash2 data-icon="inline-start" />
                <span>Delete Pool</span>
              </Button>
            </div>
          </div>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 text-xs">
            <div className="rounded-xl bg-slate-50 p-4 dark:bg-slate-950/60 border border-slate-100 dark:border-slate-800">
              <span className="text-slate-400">Target Repositories</span>
              <div className="mt-1 space-y-1">
                {poolTargetList(pool).map((url) => (
                  <div
                    key={url}
                    className="font-mono font-semibold text-slate-900 dark:text-white break-all"
                  >
                    {url}
                  </div>
                ))}
              </div>
            </div>

            <div className="rounded-xl bg-slate-50 p-4 dark:bg-slate-950/60 border border-slate-100 dark:border-slate-800">
              <span className="text-slate-400">Git Provider</span>
              <div className="mt-1 font-semibold text-slate-900 dark:text-white uppercase">
                {pool.provider}
              </div>
            </div>

            <div className="rounded-xl bg-slate-50 p-4 dark:bg-slate-950/60 border border-slate-100 dark:border-slate-800">
              <span className="text-slate-400">Registration Scope</span>
              <div className="mt-1 font-semibold text-slate-900 dark:text-white uppercase">
                {pool.scope || "repo"}
              </div>
            </div>

            <div className="rounded-xl bg-slate-50 p-4 dark:bg-slate-950/60 border border-slate-100 dark:border-slate-800 flex flex-col justify-between">
              <div>
                <div className="flex items-center justify-between">
                  <span className="text-slate-400">Runner Container Image</span>
                  <Button
                    variant="outline"
                    size="xs"
                    onClick={() => checkUpdateMutation.mutate(poolIdBigInt)}
                    disabled={checkUpdateMutation.isPending}
                  >
                    {checkUpdateMutation.isPending ? (
                      <Loader2 data-icon="inline-start" className="animate-spin" />
                    ) : (
                      <RefreshCw data-icon="inline-start" />
                    )}
                    <span>
                      {checkUpdateMutation.isPending ? "Checking..." : "Check for Updates"}
                    </span>
                  </Button>
                </div>
                <div className="mt-1 font-mono font-semibold text-slate-900 dark:text-white break-all">
                  {pool.runnerImage || "ghcr.io/noosxe/runnero:latest"}
                </div>
              </div>

              {checkUpdateMutation.isSuccess && (
                <div className="mt-3 text-xs">
                  {checkUpdateMutation.data.updateAvailable ? (
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-1.5 text-amber-600 dark:text-amber-400 font-medium">
                        <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                        <span>
                          Update available:{" "}
                          <code className="font-mono text-[11px]">
                            {checkUpdateMutation.data.update?.latestDigest
                              ? `${checkUpdateMutation.data.update.latestDigest.slice(0, 19)}...`
                              : "Newer version in registry"}
                          </code>
                        </span>
                      </div>
                      <Button
                        size="xs"
                        onClick={() => pullImageMutation.mutate(poolIdBigInt)}
                        disabled={pullImageMutation.isPending}
                        className="bg-warning text-white hover:bg-warning/80"
                      >
                        {pullImageMutation.isPending ? (
                          <Loader2 data-icon="inline-start" className="animate-spin" />
                        ) : (
                          <DownloadCloud data-icon="inline-start" />
                        )}
                        <span>{pullImageMutation.isPending ? "Pulling..." : "Pull Update"}</span>
                      </Button>
                    </div>
                  ) : (
                    <div className="flex items-center gap-1.5 text-emerald-600 dark:text-emerald-400 font-medium">
                      <CheckCircle2 className="h-3.5 w-3.5 shrink-0" />
                      <span>Image is up-to-date with registry</span>
                    </div>
                  )}
                </div>
              )}

              {checkUpdateMutation.isError && (
                <div className="mt-3 flex items-center gap-1.5 text-xs text-rose-600 dark:text-rose-400 font-medium">
                  <XCircle className="h-3.5 w-3.5 shrink-0" />
                  <span>Check failed: {checkUpdateMutation.error.message}</span>
                </div>
              )}

              {!checkUpdateMutation.isSuccess && !checkUpdateMutation.isError && poolUpdate && (
                <div className="mt-3 flex items-center justify-between gap-2 text-xs">
                  <div className="flex items-center gap-1.5 text-amber-600 dark:text-amber-400 font-medium">
                    <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                    <span>
                      Update available:{" "}
                      <code className="font-mono text-[11px]">
                        {poolUpdate.latestDigest.slice(0, 19)}...
                      </code>
                    </span>
                  </div>
                  <Button
                    size="xs"
                    onClick={() => pullImageMutation.mutate(poolIdBigInt)}
                    disabled={pullImageMutation.isPending}
                    className="bg-warning text-white hover:bg-warning/80"
                  >
                    {pullImageMutation.isPending ? (
                      <Loader2 data-icon="inline-start" className="animate-spin" />
                    ) : (
                      <DownloadCloud data-icon="inline-start" />
                    )}
                    <span>{pullImageMutation.isPending ? "Pulling..." : "Pull Update"}</span>
                  </Button>
                </div>
              )}
            </div>

            <div className="rounded-xl bg-slate-50 p-4 dark:bg-slate-950/60 border border-slate-100 dark:border-slate-800">
              <span className="text-slate-400">Max Job Lifetime Limit</span>
              <div className="mt-1 font-semibold text-slate-900 dark:text-white">
                {pool.maxRunnerLifetimeSeconds
                  ? `${pool.maxRunnerLifetimeSeconds} seconds`
                  : "7200s (2 hours)"}
              </div>
            </div>

            <div className="rounded-xl bg-slate-50 p-4 dark:bg-slate-950/60 border border-slate-100 dark:border-slate-800">
              <div className="flex items-center justify-between gap-2">
                <span className="text-slate-400">Runner Labels</span>
                {labelsCopied && (
                  <span className="inline-flex items-center gap-1 text-[11px] font-semibold text-emerald-600 dark:text-emerald-400">
                    <Check className="h-3 w-3" />
                    Copied
                  </span>
                )}
              </div>
              <Button
                variant="ghost"
                onClick={copyRunnerLabels}
                disabled={!pool.labels || pool.labels.length === 0}
                title={
                  pool.labels && pool.labels.length > 0
                    ? "Copy labels — pastes directly into a GitHub Actions runs-on list"
                    : undefined
                }
                className="mt-1 h-auto w-full flex-wrap justify-start gap-1 text-left font-normal"
              >
                {pool.labels && pool.labels.length > 0 ? (
                  pool.labels.map((l) => (
                    <span
                      key={l}
                      className="rounded-md bg-slate-200 px-2 py-0.5 text-[11px] font-mono text-slate-700 dark:bg-slate-800 dark:text-slate-300"
                    >
                      {l}
                    </span>
                  ))
                ) : (
                  <span className="font-mono text-slate-400">self-hosted, linux, arm64</span>
                )}
                {pool.labels && pool.labels.length > 0 && (
                  <Copy className="h-3 w-3 shrink-0 text-slate-400" />
                )}
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* Tab Content: Renovate Bot */}
      {activeTab === "renovate" && <PoolRenovateTab pool={pool} />}

      {/* Edit Pool Modal (docs/22 §7.1) — conditionally mounted so edit-mode
          prefill state initializes fresh from the pool on every open */}
      {isEditModalOpen && (
        <PoolWizardModal
          isOpen
          mode="edit"
          pool={pool}
          onClose={() => setIsEditModalOpen(false)}
          authProfiles={authProfiles}
          hostOs={session?.hostOs}
          hostArch={session?.hostArch}
        />
      )}

      {/* Delete Pool Modal (RUN-127, docs/25 §4.7) — conditionally mounted so
          the drain preselection re-derives from the current busy count on
          every open */}
      {isDeleteModalOpen && (
        <PoolDeleteModal
          isOpen
          onClose={() => setIsDeleteModalOpen(false)}
          poolId={pool.id}
          poolName={pool.name}
          busyCount={activeInstances}
          idleCount={idleInstances}
          maxRunnerLifetimeSeconds={Number(pool.maxRunnerLifetimeSeconds ?? 0)}
        />
      )}

      {/* Confirmation Modal: Terminate Runner */}
      <AlertDialog
        open={!!runnerToTerminate}
        onOpenChange={(open) => {
          if (!open) setRunnerToTerminate(null);
        }}
      >
        <AlertDialogContent size="sm">
          <AlertDialogMedia className="bg-destructive/10 text-destructive">
            <AlertTriangle />
          </AlertDialogMedia>
          <AlertDialogHeader>
            <AlertDialogTitle>Terminate Runner Instance?</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to manually terminate runner{" "}
              <strong className="font-mono text-foreground">{runnerToTerminate?.name}</strong>{" "}
              (container ID:{" "}
              <span className="font-mono">{runnerToTerminate?.containerId.substring(0, 12)}</span>)?
              If this runner is currently executing a workflow, the job will fail immediately.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel size="sm" disabled={terminateMutation.isPending}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              size="sm"
              onClick={handleConfirmTerminate}
              disabled={terminateMutation.isPending || !runnerToTerminate}
            >
              {terminateMutation.isPending ? "Terminating..." : "Terminate Instance"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Live Runner Log Modal */}
      {selectedRunnerForLogs && (
        <RunnerLogViewerModal
          runner={selectedRunnerForLogs}
          onClose={() => setSelectedRunnerForLogs(null)}
        />
      )}
    </div>
  );
}

function RunnerLogViewerModal({
  runner,
  onClose,
}: {
  runner: RunnerInstance;
  onClose: () => void;
}) {
  const { logs, isConnected, isConnecting, clearLogs } = useStreamRunnerLogs(runner.name);

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent
        showCloseButton
        aria-label={`Live logs for ${runner.name}`}
        className="flex h-[82vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-5xl"
      >
        <DialogTitle className="sr-only">Live logs for {runner.name}</DialogTitle>
        <div className="relative flex-1 overflow-hidden">
          <LogTerminal
            logs={logs}
            mode="live"
            runnerName={runner.name}
            containerId={runner.containerId}
            isConnected={isConnected}
            isConnecting={isConnecting}
            onClear={clearLogs}
            title={runner.name}
            headerRightInset
          />
        </div>
      </DialogContent>
    </Dialog>
  );
}

function PoolRenovateTab({ pool }: { pool: Pool }) {
  const { data: status } = useRenovateStatus(pool.id, {
    refetchInterval: 5000,
  });
  const { data: history, isLoading: historyLoading } = useRenovateHistory(pool.id, 10, 0);
  const triggerMutation = useTriggerRenovateRun();
  const updatePoolMutation = useUpdatePool();

  const [enabled, setEnabled] = useState(pool.renovate?.enabled ?? false);
  const [cronSchedule, setCronSchedule] = useState(pool.renovate?.cronSchedule || "0 3 * * 1");
  const [image, setImage] = useState(pool.renovate?.image || "renovate/renovate:latest");
  const [saveSuccess, setSaveSuccess] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [triggerMsg, setTriggerMsg] = useState<{ type: "success" | "error"; text: string } | null>(
    null,
  );

  const isRunning = status?.lastRun?.status === "running";

  const handleTrigger = async () => {
    setTriggerMsg(null);
    try {
      const res = await triggerMutation.mutateAsync(pool.id);
      if (res.success) {
        setTriggerMsg({ type: "success", text: `Triggered Renovate run #${res.runId}` });
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to trigger Renovate run";
      setTriggerMsg({ type: "error", text: msg });
    }
  };

  const handleSaveConfig = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaveError(null);
    setSaveSuccess(false);
    try {
      await updatePoolMutation.mutateAsync({
        pool: {
          ...pool,
          renovate: {
            $typeName: "supervisor.v1.RenovateConfig",
            enabled,
            cronSchedule: cronSchedule.trim(),
            image: image.trim(),
          },
        },
      });
      setSaveSuccess(true);
      setTimeout(() => setSaveSuccess(false), 3000);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to save Renovate settings";
      setSaveError(msg);
    }
  };

  return (
    <div className="space-y-6">
      {/* Overview & Manual Trigger Card */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <div className="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 lg:col-span-2 space-y-4">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Bot className="h-5 w-5 text-blue-500" />
              <h2 className="text-base font-bold text-slate-900 dark:text-white">
                Renovate Status & Automation
              </h2>
            </div>
            <Button
              size="xs"
              onClick={handleTrigger}
              disabled={triggerMutation.isPending || isRunning}
            >
              {triggerMutation.isPending ? (
                <>
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  <span>Triggering...</span>
                </>
              ) : isRunning ? (
                <>
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  <span>Run in progress...</span>
                </>
              ) : (
                <>
                  <Play data-icon="inline-start" className="fill-current" />
                  <span>Trigger Renovate Run</span>
                </>
              )}
            </Button>
          </div>

          {triggerMsg && (
            <div
              className={`rounded-xl p-3 text-xs flex items-center gap-2 ${
                triggerMsg.type === "success"
                  ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-300 border border-emerald-200 dark:border-emerald-800"
                  : "bg-rose-50 text-rose-700 dark:bg-rose-950/50 dark:text-rose-300 border border-rose-200 dark:border-rose-800"
              }`}
            >
              {triggerMsg.type === "success" ? (
                <CheckCircle2 className="h-4 w-4 shrink-0" />
              ) : (
                <XCircle className="h-4 w-4 shrink-0" />
              )}
              <span>{triggerMsg.text}</span>
            </div>
          )}

          {/* Status Metrics Strip */}
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 pt-2">
            <div className="rounded-xl bg-slate-50 p-3.5 dark:bg-slate-950/60 border border-slate-100 dark:border-slate-800">
              <span className="text-[11px] font-medium text-slate-400">Bot State</span>
              <div className="mt-1 flex items-center gap-2">
                <Badge
                  className={cn(
                    "uppercase tracking-wider",
                    isRunning
                      ? "border-warning/30 bg-warning/10 text-warning"
                      : status?.lastRun?.status === "success"
                        ? "border-success/30 bg-success/10 text-success"
                        : status?.lastRun?.status === "failure"
                          ? "border-destructive/30 bg-destructive/10 text-destructive"
                          : pool.renovate?.enabled
                            ? "border-primary/30 bg-primary/10 text-primary"
                            : "bg-muted text-muted-foreground",
                  )}
                >
                  <span
                    className={`h-1.5 w-1.5 rounded-full ${
                      isRunning
                        ? "bg-amber-500 animate-ping"
                        : status?.lastRun?.status === "success"
                          ? "bg-emerald-500"
                          : status?.lastRun?.status === "failure"
                            ? "bg-rose-500"
                            : pool.renovate?.enabled
                              ? "bg-sky-500"
                              : "bg-slate-400"
                    }`}
                  />
                  <span>
                    {isRunning
                      ? "Running"
                      : status?.lastRun?.status ||
                        (pool.renovate?.enabled ? "Scheduled" : "Disabled")}
                  </span>
                </Badge>
              </div>
            </div>

            <div className="rounded-xl bg-slate-50 p-3.5 dark:bg-slate-950/60 border border-slate-100 dark:border-slate-800">
              <span className="text-[11px] font-medium text-slate-400">Next Scheduled Run</span>
              <div className="mt-1 font-mono text-xs font-semibold text-slate-800 dark:text-slate-200 truncate">
                {status?.nextScheduledRun ||
                  (pool.renovate?.enabled ? pool.renovate.cronSchedule : "Disabled")}
              </div>
            </div>

            <div className="rounded-xl bg-slate-50 p-3.5 dark:bg-slate-950/60 border border-slate-100 dark:border-slate-800">
              <span className="text-[11px] font-medium text-slate-400">Last Execution</span>
              <div className="mt-1 text-xs font-medium text-slate-700 dark:text-slate-300">
                {status?.lastRun?.startedAt
                  ? new Date(status.lastRun.startedAt).toLocaleString()
                  : "No runs yet"}
              </div>
            </div>
          </div>

          {status?.lastRun?.summary && (
            <div className="mt-2 rounded-xl bg-slate-50 p-3 text-xs font-mono text-slate-700 dark:bg-slate-950/60 dark:text-slate-300 border border-slate-100 dark:border-slate-800 whitespace-pre-wrap">
              <div className="text-[10px] uppercase tracking-wider font-sans font-semibold text-slate-400 mb-1">
                Latest Run Summary
              </div>
              {status.lastRun.summary}
            </div>
          )}
        </div>

        {/* Configuration Form */}
        <div className="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-4">
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-bold text-slate-900 dark:text-white">Bot Settings</h3>
          </div>

          <form onSubmit={handleSaveConfig} className="space-y-4 text-xs">
            <div>
              <label className="flex items-center gap-2 cursor-pointer">
                <Checkbox checked={enabled} onCheckedChange={(v) => setEnabled(v === true)} />
                <span className="font-semibold text-slate-800 dark:text-slate-200">
                  Enable Managed Renovate
                </span>
              </label>
              <p className="mt-1 text-[11px] text-slate-400">
                Automatically scans and updates dependencies according to the schedule.
              </p>
            </div>

            <div>
              <FieldLabel className="block dark:mb-1">Cron Schedule</FieldLabel>
              <Input
                type="text"
                value={cronSchedule}
                onChange={(e) => setCronSchedule(e.target.value)}
                placeholder="0 3 * * 1"
                className="font-mono text-xs"
              />
              <p className="mt-1 text-[11px] text-slate-400">
                Standard 5-part cron syntax (e.g., <code className="font-mono">0 3 * * 1</code> for
                weekly Monday 3 AM).
              </p>
            </div>

            <div>
              <FieldLabel className="block dark:mb-1">Task Container Image</FieldLabel>
              <Input
                type="text"
                value={image}
                onChange={(e) => setImage(e.target.value)}
                placeholder="renovate/renovate:latest"
                className="font-mono text-xs"
              />
            </div>

            {saveError && (
              <div className="rounded-xl bg-rose-50 p-2.5 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300 border border-rose-200 dark:border-rose-800">
                {saveError}
              </div>
            )}

            {saveSuccess && (
              <div className="rounded-xl bg-emerald-50 p-2.5 text-xs text-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-300 border border-emerald-200 dark:border-emerald-800">
                Settings saved successfully!
              </div>
            )}

            <Button
              type="submit"
              size="sm"
              disabled={updatePoolMutation.isPending}
              className="w-full"
            >
              {updatePoolMutation.isPending ? (
                <>
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  <span>Saving...</span>
                </>
              ) : (
                <>
                  <Save data-icon="inline-start" />
                  <span>Save Bot Settings</span>
                </>
              )}
            </Button>
          </form>
        </div>
      </div>

      {/* History Table */}
      <div className="space-y-3">
        <h3 className="text-sm font-bold text-slate-900 dark:text-white">Execution History</h3>

        {historyLoading ? (
          <div className="rounded-2xl border border-slate-200 bg-white p-8 text-center text-xs text-slate-400 dark:border-slate-800 dark:bg-slate-900">
            Loading run history...
          </div>
        ) : !history?.runs || history.runs.length === 0 ? (
          <div className="rounded-2xl border border-dashed border-slate-300 p-8 text-center text-slate-500 dark:border-slate-800 dark:text-slate-400">
            <Bot className="mx-auto h-7 w-7 text-slate-400 mb-2" />
            <p className="text-xs font-semibold text-slate-800 dark:text-slate-200">
              No Renovate runs recorded yet
            </p>
            <p className="text-[11px] text-slate-400 mt-0.5">
              Trigger a manual run or wait for the scheduled cron run to execute.
            </p>
          </div>
        ) : (
          <div className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
            <div className="overflow-x-auto">
              <table className="w-full text-left text-xs text-slate-600 dark:text-slate-300">
                <thead className="border-b border-slate-100 bg-slate-50 font-semibold uppercase tracking-wider text-[11px] text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
                  <tr>
                    <th className="px-5 py-3">Run ID</th>
                    <th className="px-5 py-3">Status</th>
                    <th className="px-5 py-3">Started At</th>
                    <th className="px-5 py-3">Completed At</th>
                    <th className="px-5 py-3">Summary</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {history.runs.map((run) => (
                    <tr
                      key={run.id.toString()}
                      className="hover:bg-slate-50/50 dark:hover:bg-slate-800/50 transition-colors"
                    >
                      <td className="px-5 py-3.5 font-mono font-semibold text-slate-900 dark:text-white">
                        #{run.id.toString()}
                      </td>
                      <td className="px-5 py-3.5">
                        <Badge
                          className={cn(
                            "uppercase tracking-wider",
                            run.status === "running"
                              ? "border-warning/30 bg-warning/10 text-warning"
                              : run.status === "success"
                                ? "border-success/30 bg-success/10 text-success"
                                : "border-destructive/30 bg-destructive/10 text-destructive",
                          )}
                        >
                          <span
                            className={`h-1.5 w-1.5 rounded-full ${
                              run.status === "running"
                                ? "bg-amber-500 animate-ping"
                                : run.status === "success"
                                  ? "bg-emerald-500"
                                  : "bg-rose-500"
                            }`}
                          />
                          <span>{run.status}</span>
                        </Badge>
                      </td>
                      <td className="px-5 py-3.5 font-mono text-slate-500 dark:text-slate-400">
                        {run.startedAt ? new Date(run.startedAt).toLocaleString() : "—"}
                      </td>
                      <td className="px-5 py-3.5 font-mono text-slate-500 dark:text-slate-400">
                        {run.completedAt ? new Date(run.completedAt).toLocaleString() : "—"}
                      </td>
                      <td className="px-5 py-3.5 font-mono text-[11px] text-slate-600 dark:text-slate-300 max-w-md truncate">
                        {run.summary || "—"}
                      </td>
                    </tr>
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
