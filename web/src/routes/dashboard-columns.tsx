import { createColumnHelper } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { LinkButton } from "../lib/link-button";
import { cn } from "cn";
import { CheckCircle2, Terminal, XCircle } from "lucide-react";
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
 * Column defs for the dashboard "recent executions" table (docs/31 §5
 * phase 1). Rendered by the lib/tables DataTable shell; classes that used
 * to sit on the hand-rolled <TableCell> now come from typed column meta.
 */
export const recentJobsColumns = () =>
  columnHelper.columns([
    columnHelper.accessor("status", {
      header: "Status",
      cell: ({ row }) => (
        <Badge
          className={cn(
            "uppercase tracking-wider",
            row.original.status === "success"
              ? "border-success/30 bg-success/10 text-success"
              : "border-destructive/30 bg-destructive/10 text-destructive",
          )}
        >
          {row.original.status === "success" ? (
            <CheckCircle2 className="size-3" />
          ) : (
            <XCircle className="size-3" />
          )}
          {row.original.status}
        </Badge>
      ),
    }),
    columnHelper.accessor("runnerName", {
      header: "Runner Name",
      meta: { cellClassName: "font-mono font-medium" },
    }),
    columnHelper.accessor("durationSeconds", {
      header: "Duration",
      meta: { cellClassName: "font-mono" },
      cell: (info) => formatDuration(info.getValue()),
    }),
    columnHelper.accessor("queueTimeSeconds", {
      header: "Queue Wait",
      meta: { cellClassName: "font-mono text-muted-foreground" },
      cell: (info) => (info.getValue() > 0 ? `${info.getValue().toFixed(1)}s` : "—"),
    }),
    columnHelper.accessor("completedAt", {
      header: "Completed At",
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
