import { useParams, Link } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { cn } from "cn";
import { useJobRecord, useRunnerLogs } from "../lib/api/query-hooks";
import { useStreamRunnerLogs } from "../lib/api/streaming-hooks";
import { LogTerminal } from "../components/terminal/log-terminal";
import {
  ArrowLeft,
  CheckCircle2,
  XCircle,
  Clock,
  Radio,
  Timer,
  Server,
  Calendar,
} from "lucide-react";

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

export function HistoryDetailPage() {
  const { jobId } = useParams({ strict: false }) as { jobId?: string };
  const numericJobId = jobId && !isNaN(Number(jobId)) ? BigInt(jobId) : undefined;

  const { data: job, isLoading: isJobLoading } = useJobRecord(numericJobId);

  const runnerName = job?.runnerName || jobId || "";
  const isRunning = job?.status === "running";

  // If the job is active/running, stream live; otherwise fetch stored historical archive
  const {
    logs: liveLogs,
    isConnected,
    isConnecting,
    clearLogs,
  } = useStreamRunnerLogs(runnerName, {
    enabled: isRunning && Boolean(runnerName),
  });

  const { data: historicalLogs, isLoading: isHistLoading } = useRunnerLogs(
    runnerName,
    !isRunning && Boolean(runnerName),
  );

  const logs = isRunning ? liveLogs : (historicalLogs ?? []);
  const isLogsLoading = isRunning ? isConnecting : isHistLoading;

  const isSuccess = job?.status === "success";
  const isFailed = job?.status === "failure" || job?.status === "failed";

  return (
    <div className="space-y-6">
      {/* Navigation Breadcrumb */}
      <div className="flex items-center justify-between">
        <Link
          to="/history"
          className="inline-flex items-center gap-1 text-xs font-semibold text-primary hover:underline "
        >
          <ArrowLeft className="h-3.5 w-3.5" /> Back to Job Execution History
        </Link>
      </div>

      {/* Execution Summary Header Card */}
      <Card className="px-(--card-spacing)">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="space-y-1">
            <div className="flex items-center gap-2.5">
              <h1 className="font-mono text-xl font-bold tracking-tight text-foreground">
                {runnerName || (isJobLoading ? "Loading runner..." : `Job #${jobId}`)}
              </h1>

              {job?.status && (
                <Badge
                  className={cn(
                    "uppercase tracking-wider",
                    isSuccess
                      ? "border-success/30 bg-success/10 text-success"
                      : isFailed
                        ? "border-destructive/30 bg-destructive/10 text-destructive"
                        : isRunning
                          ? "border-primary/30 bg-primary/10 text-primary"
                          : "bg-muted text-muted-foreground",
                  )}
                >
                  {isSuccess ? (
                    <CheckCircle2 className="h-3 w-3" />
                  ) : isFailed ? (
                    <XCircle className="h-3 w-3" />
                  ) : isRunning ? (
                    <Radio className="h-3 w-3 animate-pulse" />
                  ) : (
                    <Clock className="h-3 w-3" />
                  )}
                  <span>{job.status}</span>
                </Badge>
              )}
            </div>

            <p className="text-xs text-muted-foreground">
              {job ? (
                <>
                  Execution record for pool{" "}
                  <span className="font-semibold text-foreground">
                    {job.poolName || `#${job.poolId.toString()}`}
                  </span>
                  {job.id > 0n && ` • Job ID #${job.id.toString()}`}
                </>
              ) : (
                "Loading execution metadata..."
              )}
            </p>
          </div>
        </div>

        {/* Quick KPI Strip */}
        <div className="mt-5 grid grid-cols-2 gap-3 border-t border-border/60 pt-4 sm:grid-cols-4">
          <div className="flex items-center gap-2">
            <Timer className="h-4 w-4 text-muted-foreground" />
            <div>
              <p className="text-[10px] font-semibold uppercase text-muted-foreground">Duration</p>
              <p className="font-mono text-xs font-bold text-foreground">
                {job ? formatDuration(job.durationSeconds) : "—"}
              </p>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Clock className="h-4 w-4 text-muted-foreground" />
            <div>
              <p className="text-[10px] font-semibold uppercase text-muted-foreground">
                Queue Latency
              </p>
              <p className="font-mono text-xs font-bold text-foreground">
                {job && job.queueTimeSeconds > 0 ? `${job.queueTimeSeconds.toFixed(1)}s` : "—"}
              </p>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Server className="h-4 w-4 text-muted-foreground" />
            <div>
              <p className="text-[10px] font-semibold uppercase text-muted-foreground">Pool</p>
              <p className="truncate font-mono text-xs font-bold text-foreground">
                {job?.poolName || (job?.poolId ? `#${job.poolId.toString()}` : "—")}
              </p>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Calendar className="h-4 w-4 text-muted-foreground" />
            <div>
              <p className="text-[10px] font-semibold uppercase text-muted-foreground">Started</p>
              <p className="font-mono text-[11px] text-muted-foreground">
                {formatTimestamp(job?.startedAt)}
              </p>
            </div>
          </div>
        </div>
      </Card>

      {/* Log Console Terminal View */}
      <div className="h-[600px]">
        <LogTerminal
          logs={logs}
          mode={isRunning ? "live" : "historical"}
          runnerName={runnerName}
          isConnected={isConnected}
          isConnecting={isConnecting}
          isLoading={isLogsLoading}
          onClear={isRunning ? clearLogs : undefined}
          title={`Runner Logs: ${runnerName}`}
        />
      </div>
    </div>
  );
}
