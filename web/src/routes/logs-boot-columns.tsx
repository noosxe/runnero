import { createColumnHelper } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { formatBytes, formatTimestamp } from "./logs-removals-columns";
import type { SupervisorBootLog } from "../gen/api_pb";
import type { AppTableFeatures } from "../lib/tables/use-app-table";

function shortBootId(bootId: string): string {
  return bootId.length > 8 ? bootId.substring(0, 8) : bootId;
}

const columnHelper = createColumnHelper<AppTableFeatures, SupervisorBootLog>();

/**
 * Column defs for the /logs supervisor boots table (docs/31 §5 phase 3).
 * Row click/selection affordances (cursor, highlight, data attributes)
 * are supplied by the call site via DataTable's `getRowProps`, since they
 * depend on the selected boot state.
 */
export const bootColumns = () =>
  columnHelper.columns([
    columnHelper.accessor("startedAt", {
      header: "Boot time",
      cell: (info) => formatTimestamp(info.getValue()),
    }),
    columnHelper.accessor("bootId", {
      header: "Boot ID",
      meta: { cellClassName: "font-mono text-xs" },
      cell: (info) => shortBootId(info.getValue()),
    }),
    columnHelper.accessor("sizeBytes", {
      header: "Size",
      cell: (info) => formatBytes(info.getValue()),
    }),
    columnHelper.accessor("rotationSeq", {
      header: "Rotation",
      cell: (info) => (info.getValue() > 0 ? `#${info.getValue()}` : "—"),
    }),
    columnHelper.display({
      id: "current",
      // SR name for the visually empty current-boot column (docs/36 §5.8).
      header: () => <span className="sr-only">Current boot</span>,
      cell: ({ row }) =>
        row.original.isCurrent ? (
          <Badge
            data-testid="logs-boot-current-badge"
            className="border border-success/30 bg-success/10 text-success"
          >
            current
          </Badge>
        ) : undefined,
    }),
  ]);

export { shortBootId };
