import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { cn } from "cn";
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
  EmptyDescription,
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
import { useJobHistory, usePools } from "../lib/api/query-hooks";
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
      <Card size="sm" className="gap-3 p-3">
        <div className="relative flex-1">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="text"
            value={search}
            onChange={(e) => handleSearchChange(e.target.value)}
            placeholder="Search by runner name..."
            className="pl-9"
          />
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {/* Pool Filter */}
          <Select
            value={selectedPool}
            onValueChange={(v) => handlePoolChange(v as string)}
            items={[
              { value: "0", label: "All Pools" },
              ...(pools ?? []).map((p) => ({ value: p.id.toString(), label: p.name })),
            ]}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="0">All Pools</SelectItem>
                {(pools ?? []).map((p) => (
                  <SelectItem key={p.id.toString()} value={p.id.toString()}>
                    {p.name}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>

          {/* Status Filter */}
          <Select
            value={statusFilter}
            onValueChange={(v) => handleStatusChange(v as string)}
            items={[
              { value: "all", label: "All Statuses" },
              { value: "success", label: "Success" },
              { value: "failure", label: "Failed" },
              { value: "running", label: "Running" },
            ]}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">All Statuses</SelectItem>
                <SelectItem value="success">Success</SelectItem>
                <SelectItem value="failure">Failed</SelectItem>
                <SelectItem value="running">Running</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>

          {/* Page Size Select */}
          <Select
            value={pageSize}
            onValueChange={(v) => {
              setPageSize(Number(v));
              setPage(1);
            }}
            items={[10, 25, 50].map((n) => ({ value: n, label: `${n} / page` }))}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {[10, 25, 50].map((n) => (
                  <SelectItem key={n} value={n}>
                    {n} / page
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      </Card>

      {/* History Table */}
      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : !history?.jobs || history.jobs.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <History />
            </EmptyMedia>
            <EmptyTitle>No execution records found</EmptyTitle>
            <EmptyDescription>
              {search || statusFilter !== "all" || selectedPool !== "0"
                ? "No job history matched your current filters. Try resetting search or status filters."
                : "Completed runner workflow jobs will appear here automatically."}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <Card className="py-0">
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>ID</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Runner Name</TableHead>
                  <TableHead>Pool</TableHead>
                  <TableHead>Duration</TableHead>
                  <TableHead>Queue Wait</TableHead>
                  <TableHead>Started At</TableHead>
                  <TableHead>Completed At</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {history.jobs.map((job) => {
                  const isSuccess = job.status === "success";
                  const isFailed = job.status === "failure" || job.status === "failed";
                  const isRunning = job.status === "running";

                  return (
                    <TableRow key={job.id.toString()}>
                      <TableCell className="font-mono text-muted-foreground">
                        #{job.id.toString()}
                      </TableCell>
                      <TableCell>
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
                            <CheckCircle2 className="size-3" />
                          ) : isFailed ? (
                            <XCircle className="size-3" />
                          ) : (
                            <Clock className="size-3" />
                          )}
                          <span>{job.status}</span>
                        </Badge>
                      </TableCell>
                      <TableCell className="font-mono font-medium">{job.runnerName}</TableCell>
                      <TableCell>
                        <Badge variant="secondary">
                          {job.poolName || `Pool #${job.poolId.toString()}`}
                        </Badge>
                      </TableCell>
                      <TableCell className="font-mono">
                        {formatDuration(job.durationSeconds)}
                      </TableCell>
                      <TableCell className="font-mono text-muted-foreground">
                        {job.queueTimeSeconds > 0 ? `${job.queueTimeSeconds.toFixed(1)}s` : "—"}
                      </TableCell>
                      <TableCell className="font-mono text-muted-foreground">
                        {formatTimestamp(job.startedAt)}
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
                  );
                })}
              </TableBody>
            </Table>
          </div>

          {/* Pagination Footer */}
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between border-t border-border/60 px-(--card-spacing) py-3.5 text-xs text-muted-foreground">
            <div>
              Showing{" "}
              <span className="font-semibold text-foreground">
                {totalCount === 0 ? 0 : offset + 1}
              </span>{" "}
              to{" "}
              <span className="font-semibold text-foreground">
                {Math.min(offset + pageSize, totalCount)}
              </span>{" "}
              of <span className="font-semibold text-foreground">{totalCount}</span> runs
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
        </Card>
      )}
    </div>
  );
}
