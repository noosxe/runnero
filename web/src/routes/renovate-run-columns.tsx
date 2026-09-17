import { createColumnHelper } from "@tanstack/react-table";
import { JobStatusBadge } from "@/components/common/job-status-badge";
import type { RenovateRun } from "../gen/api_pb";
import type { AppTableFeatures } from "../lib/tables/use-app-table";

const columnHelper = createColumnHelper<AppTableFeatures, RenovateRun>();

/**
 * Column defs for the pool-detail Renovate run history table (docs/31 §5
 * phase 1). Classes that used to sit on the hand-rolled <TableCell> now
 * come from typed column meta.
 */
export const renovateRunColumns = () =>
  columnHelper.columns([
    columnHelper.accessor("id", {
      header: "Run ID",
      meta: { cellClassName: "font-mono font-semibold" },
      cell: (info) => `#${info.getValue().toString()}`,
    }),
    columnHelper.accessor("status", {
      header: "Status",
      // Renovate runs reuse the job status vocabulary (running/success/
      // failure) and the shared badge — one status system app-wide.
      cell: ({ row }) => <JobStatusBadge status={row.original.status} />,
    }),
    columnHelper.accessor("startedAt", {
      header: "Started At",
      meta: { cellClassName: "font-mono text-muted-foreground" },
      cell: (info) => (info.getValue() ? new Date(info.getValue()).toLocaleString() : "—"),
    }),
    columnHelper.accessor("completedAt", {
      header: "Completed At",
      meta: { cellClassName: "font-mono text-muted-foreground" },
      cell: (info) => (info.getValue() ? new Date(info.getValue()).toLocaleString() : "—"),
    }),
    columnHelper.accessor("summary", {
      header: "Summary",
      meta: {
        cellClassName: "max-w-md truncate font-mono text-muted-foreground",
      },
      cell: (info) => info.getValue() || "—",
    }),
  ]);
