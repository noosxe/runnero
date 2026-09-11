import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { cn } from "cn";
import { useJobHistory, usePools } from "../lib/api/query-hooks";
import { Link } from "@tanstack/react-router";
import {
  History,
  CheckCircle2,
  XCircle,
  Clock,
  Search,
  ChevronLeft,
  ChevronRight,
  Download,
  Terminal,
  Loader2,
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

export function HistoryPage() {
  const [search, setSearch] = useState("");
  const [selectedPool, setSelectedPool] = useState<string>("0");
  const [statusFilter, setStatusFilter] = useState<string>("all");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);

  const { data: pools } = usePools();

  const poolId = selectedPool === "0" ? 0n : BigInt(selectedPool);
  const offset = (page - 1) * pageSize;

  const {
    data: history,
    isLoading,
    isFetching,
  } = useJobHistory({
    poolId,
    limit: pageSize,
    offset,
    search,
    status: statusFilter,
  });

  const totalCount = history?.totalCount ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalCount / pageSize));

  // Reset to page 1 when search or filters change
  const handleSearchChange = (val: string) => {
    setSearch(val);
    setPage(1);
  };

  const handlePoolChange = (val: string) => {
    setSelectedPool(val);
    setPage(1);
  };

  const handleStatusChange = (val: string) => {
    setStatusFilter(val);
    setPage(1);
  };

  const handleExportCSV = () => {
    if (!history?.jobs || history.jobs.length === 0) return;
    const headers = [
      "ID",
      "Status",
      "Runner Name",
      "Pool ID",
      "Pool Name",
      "Duration (s)",
      "Queue Time (s)",
      "Queued At",
      "Started At",
      "Completed At",
    ];
    const rows = history.jobs.map((j) => [
      j.id.toString(),
      j.status,
      `"${j.runnerName.replace(/"/g, '""')}"`,
      j.poolId.toString(),
      `"${(j.poolName || "").replace(/"/g, '""')}"`,
      j.durationSeconds.toFixed(1),
      j.queueTimeSeconds.toFixed(1),
      j.queuedAt || "",
      j.startedAt || "",
      j.completedAt || "",
    ]);
    const csvContent = [headers.join(","), ...rows.map((r) => r.join(","))].join("\n");
    const blob = new Blob([csvContent], { type: "text/csv;charset=utf-8;" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.setAttribute("href", url);
    link.setAttribute("download", `job-history-${new Date().toISOString().slice(0, 10)}.csv`);
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
  };

  return (
    <div className="space-y-6">
      {/* Page Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
            Job Execution History
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            Historical execution records, queue latencies, duration metrics, and runner logs.
          </p>
        </div>

        <Button
          variant="outline"
          size="sm"
          onClick={handleExportCSV}
          disabled={!history?.jobs || history.jobs.length === 0}
        >
          <Download data-icon="inline-start" />
          <span>Export CSV</span>
        </Button>
      </div>

      {/* Filters Toolbar */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between rounded-2xl border border-slate-200 bg-white p-3 shadow-xs dark:border-slate-800 dark:bg-slate-900">
        <div className="relative flex-1">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400" />
          <input
            type="text"
            value={search}
            onChange={(e) => handleSearchChange(e.target.value)}
            placeholder="Search by runner name..."
            className="w-full rounded-xl border-0 bg-slate-50 py-2 pl-9 pr-4 text-xs text-slate-900 placeholder-slate-400 focus:bg-white focus:ring-2 focus:ring-blue-500 dark:bg-slate-800 dark:text-white dark:focus:bg-slate-900"
          />
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {/* Pool Filter */}
          <select
            value={selectedPool}
            onChange={(e) => handlePoolChange(e.target.value)}
            className="rounded-xl border border-slate-200 bg-white px-3 py-2 text-xs font-medium text-slate-700 shadow-xs focus:ring-2 focus:ring-blue-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-200"
          >
            <option value="0">All Pools</option>
            {pools?.map((p) => (
              <option key={p.id.toString()} value={p.id.toString()}>
                {p.name}
              </option>
            ))}
          </select>

          {/* Status Filter */}
          <select
            value={statusFilter}
            onChange={(e) => handleStatusChange(e.target.value)}
            className="rounded-xl border border-slate-200 bg-white px-3 py-2 text-xs font-medium text-slate-700 shadow-xs focus:ring-2 focus:ring-blue-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-200"
          >
            <option value="all">All Statuses</option>
            <option value="success">Success</option>
            <option value="failure">Failed</option>
            <option value="running">Running</option>
          </select>

          {/* Page Size Select */}
          <select
            value={pageSize}
            onChange={(e) => {
              setPageSize(Number(e.target.value));
              setPage(1);
            }}
            className="rounded-xl border border-slate-200 bg-white px-3 py-2 text-xs font-medium text-slate-700 shadow-xs focus:ring-2 focus:ring-blue-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-200"
          >
            <option value={10}>10 / page</option>
            <option value={25}>25 / page</option>
            <option value={50}>50 / page</option>
          </select>
        </div>
      </div>

      {/* History Table */}
      {isLoading ? (
        <div className="rounded-2xl border border-slate-200 bg-white p-12 text-center text-xs text-slate-400 dark:border-slate-800 dark:bg-slate-900">
          <Loader2 className="mx-auto h-6 w-6 animate-spin text-blue-500 mb-2" />
          <span>Loading execution records...</span>
        </div>
      ) : !history?.jobs || history.jobs.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-slate-300 p-12 text-center text-slate-500 dark:border-slate-800 dark:text-slate-400">
          <History className="mx-auto h-8 w-8 text-slate-400 mb-2" />
          <p className="text-sm font-semibold text-slate-800 dark:text-slate-200">
            No execution records found
          </p>
          <p className="text-xs text-slate-500 mt-1 max-w-sm mx-auto">
            {search || statusFilter !== "all" || selectedPool !== "0"
              ? "No job history matched your current filters. Try resetting search or status filters."
              : "Completed runner workflow jobs will appear here automatically."}
          </p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs text-slate-600 dark:text-slate-300">
              <thead className="border-b border-slate-100 bg-slate-50 font-semibold uppercase tracking-wider text-[11px] text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
                <tr>
                  <th className="px-5 py-3">ID</th>
                  <th className="px-5 py-3">Status</th>
                  <th className="px-5 py-3">Runner Name</th>
                  <th className="px-5 py-3">Pool</th>
                  <th className="px-5 py-3">Duration</th>
                  <th className="px-5 py-3">Queue Wait</th>
                  <th className="px-5 py-3">Started At</th>
                  <th className="px-5 py-3">Completed At</th>
                  <th className="px-5 py-3 text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {history.jobs.map((job) => {
                  const isSuccess = job.status === "success";
                  const isFailed = job.status === "failure" || job.status === "failed";
                  const isRunning = job.status === "running";

                  return (
                    <tr
                      key={job.id.toString()}
                      className="hover:bg-slate-50/50 dark:hover:bg-slate-800/50 transition-colors"
                    >
                      <td className="px-5 py-3.5 font-mono text-[11px] text-slate-400">
                        #{job.id.toString()}
                      </td>
                      <td className="px-5 py-3.5">
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
                          ) : (
                            <Clock className="h-3 w-3" />
                          )}
                          <span>{job.status}</span>
                        </Badge>
                      </td>
                      <td className="px-5 py-3.5 font-mono font-medium text-slate-900 dark:text-white">
                        {job.runnerName}
                      </td>
                      <td className="px-5 py-3.5">
                        <span className="rounded-md bg-slate-100 px-2 py-0.5 text-[11px] font-medium text-slate-700 dark:bg-slate-800 dark:text-slate-300">
                          {job.poolName || `Pool #${job.poolId.toString()}`}
                        </span>
                      </td>
                      <td className="px-5 py-3.5 font-mono">
                        {formatDuration(job.durationSeconds)}
                      </td>
                      <td className="px-5 py-3.5 font-mono text-slate-500">
                        {job.queueTimeSeconds > 0 ? `${job.queueTimeSeconds.toFixed(1)}s` : "—"}
                      </td>
                      <td className="px-5 py-3.5 text-slate-500 font-mono text-[11px]">
                        {formatTimestamp(job.startedAt)}
                      </td>
                      <td className="px-5 py-3.5 text-slate-500 font-mono text-[11px]">
                        {formatTimestamp(job.completedAt)}
                      </td>
                      <td className="px-5 py-3.5 text-right">
                        <Link
                          to="/history/$jobId"
                          params={{ jobId: job.id.toString() }}
                          className="inline-flex items-center gap-1 rounded-lg border border-slate-200 bg-white px-2.5 py-1 text-xs font-semibold text-blue-600 shadow-2xs hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-blue-400 dark:hover:bg-slate-700 transition-colors"
                        >
                          <Terminal className="h-3 w-3" />
                          <span>Logs</span>
                        </Link>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>

          {/* Pagination Footer */}
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between border-t border-slate-100 px-5 py-3.5 dark:border-slate-800 text-xs text-slate-500">
            <div>
              Showing{" "}
              <span className="font-semibold text-slate-700 dark:text-slate-200">
                {totalCount === 0 ? 0 : offset + 1}
              </span>{" "}
              to{" "}
              <span className="font-semibold text-slate-700 dark:text-slate-200">
                {Math.min(offset + pageSize, totalCount)}
              </span>{" "}
              of{" "}
              <span className="font-semibold text-slate-700 dark:text-slate-200">{totalCount}</span>{" "}
              runs
            </div>

            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="xs"
                onClick={() => setPage((p) => Math.max(1, p - 1))}
                disabled={page <= 1 || isFetching}
              >
                <ChevronLeft data-icon="inline-start" /> Prev
              </Button>

              <span className="px-2 font-mono text-slate-600 dark:text-slate-300">
                {page} / {totalPages}
              </span>

              <Button
                variant="outline"
                size="xs"
                onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                disabled={page >= totalPages || isFetching}
              >
                Next <ChevronRight data-icon="inline-end" />
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
