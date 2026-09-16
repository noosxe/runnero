import { createColumnHelper } from "@tanstack/react-table";
import { SortableHeader } from "../lib/tables/sortable-header";
import { Badge } from "@/components/ui/badge";
import { LinkButton } from "../lib/link-button";
import { cn } from "cn";
import { CheckCircle2, Clock, Terminal, XCircle } from "lucide-react";
import type { JobRecord } from "../gen/api_pb";
import type { AppTableFeatures } from "../lib/tables/use-app-table";

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

const columnHelper = createColumnHelper<AppTableFeatures, JobRecord>();

/**
 * Column defs for the job history table (docs/31 §5 phase 2). Paging is
 * server-side (offset + totalCount) — the table only displays the current
 * page. Classes that used to sit on the hand-rolled <TableCell> now come
 * from typed column meta.
 */
export const jobHistoryColumns = () =>
  columnHelper.columns([
    columnHelper.accessor("id", {
      header: "ID",
      meta: { cellClassName: "font-mono text-muted-foreground" },
      cell: (info) => `#${info.getValue().toString()}`,
    }),
    columnHelper.accessor("status", {
      header: "Status",
      cell: ({ row }) => {
        const isSuccess = row.original.status === "success";
        const isFailed = row.original.status === "failure" || row.original.status === "failed";
        const isRunning = row.original.status === "running";
        return (
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
            <span>{row.original.status}</span>
          </Badge>
        );
      },
    }),
    columnHelper.accessor("runnerName", {
      header: "Runner Name",
      meta: { cellClassName: "font-mono font-medium" },
    }),
    columnHelper.display({
      id: "pool",
      header: "Pool",
      cell: ({ row }) => (
        <Badge variant="secondary">
          {row.original.poolName || `Pool #${row.original.poolId.toString()}`}
        </Badge>
      ),
    }),
    columnHelper.accessor("durationSeconds", {
      header: ({ column }) => <SortableHeader column={column}>Duration</SortableHeader>,
      sortFn: "basic",
      meta: { cellClassName: "font-mono" },
      cell: (info) => formatDuration(info.getValue()),
    }),
    columnHelper.accessor("queueTimeSeconds", {
      header: ({ column }) => <SortableHeader column={column}>Queue Wait</SortableHeader>,
      sortFn: "basic",
      meta: { cellClassName: "font-mono text-muted-foreground" },
      cell: (info) => (info.getValue() > 0 ? `${info.getValue().toFixed(1)}s` : "—"),
    }),
    columnHelper.accessor("startedAt", {
      header: ({ column }) => <SortableHeader column={column}>Started At</SortableHeader>,
      sortFn: "basic", // ISO-8601: lexicographic order = chronological
      meta: { cellClassName: "font-mono text-muted-foreground" },
      cell: (info) => formatTimestamp(info.getValue()),
    }),
    columnHelper.accessor("completedAt", {
      header: ({ column }) => <SortableHeader column={column}>Completed At</SortableHeader>,
      sortFn: "basic", // ISO-8601: lexicographic order = chronological
      meta: { cellClassName: "font-mono text-muted-foreground" },
      cell: (info) => formatTimestamp(info.getValue()),
    }),
    columnHelper.display({
      id: "actions",
      header: "Actions",
      meta: { headerClassName: "text-right", cellClassName: "text-right" },
      cell: ({ row }) => (
        <LinkButton
          to="/history/$jobId"
          params={{ jobId: row.original.id.toString() }}
          variant="outline"
          size="xs"
        >
          <Terminal data-icon="inline-start" />
          <span>Logs</span>
        </LinkButton>
      ),
    }),
  ]);

export { formatDuration, formatTimestamp };
