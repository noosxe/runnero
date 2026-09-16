import { createColumnHelper } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { cn } from "cn";
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
      cell: ({ row }) => (
        <Badge
          className={cn(
            "uppercase tracking-wider",
            row.original.status === "running"
              ? "border-warning/30 bg-warning/10 text-warning"
              : row.original.status === "success"
                ? "border-success/30 bg-success/10 text-success"
                : "border-destructive/30 bg-destructive/10 text-destructive",
          )}
        >
          <span
            className={cn(
              "size-1.5 rounded-full",
              row.original.status === "running"
                ? "bg-warning animate-ping"
                : row.original.status === "success"
                  ? "bg-success"
                  : "bg-destructive",
            )}
          />
          <span>{row.original.status}</span>
        </Badge>
      ),
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
