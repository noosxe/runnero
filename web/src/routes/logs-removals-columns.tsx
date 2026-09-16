import { createColumnHelper } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "cn";
import { useNavigate } from "@tanstack/react-router";
import type { RemovalRecordSummary } from "../gen/api_pb";
import type { AppTableFeatures } from "../lib/tables/use-app-table";

function formatBytes(bytes: bigint | number): string {
  const n = Number(bytes);
  if (n < 1024) {
    return `${n} B`;
  }
  const units = ["KiB", "MiB", "GiB"];
  let v = n / 1024;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return `${v.toFixed(1)} ${units[u]}`;
}

function formatTimestamp(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) {
    return ts;
  }
  return d.toLocaleString();
}

// Reason badge palette mirrors the pool diagnostics status colors:
// green = healthy exit, blue = informational, amber = capacity/lifecycle,
// red = failure, neutral = operator/system actions.
const REASON_BADGE_CLASS: Record<string, string> = {
  reap: "border-success/30 bg-success/10 text-success",
  "task-exit": "border-primary/30 bg-primary/10 text-primary",
  "lifetime-limit": "border-warning/30 bg-warning/10 text-warning",
  "idle-drain": "border-warning/30 bg-warning/10 text-warning",
  shutdown: "border-border bg-muted text-muted-foreground",
  "pool-drain": "border-border bg-muted text-muted-foreground",
  recycle: "border-border bg-muted text-muted-foreground",
  manual: "border-border bg-muted text-muted-foreground",
  "create-failure": "border-destructive/30 bg-destructive/10 text-destructive",
};

const columnHelper = createColumnHelper<AppTableFeatures, RemovalRecordSummary>();

/** Cell component: navigation needs the router hook. */
function ViewCaptureCell({ runnerId }: { runnerId: string }) {
  const navigate = useNavigate();
  return (
    <Button
      size="xs"
      variant="outline"
      data-testid="logs-removal-view-capture"
      onClick={() => navigate({ to: "/logs", search: { tab: "runners", runner: runnerId } })}
    >
      View capture
    </Button>
  );
}

/** Cell component: navigation needs the router hook. */
function ViewBootCell({ bootId }: { bootId: string }) {
  const navigate = useNavigate();
  return (
    <Button
      size="xs"
      variant="outline"
      data-testid="logs-removal-view-boot"
      disabled={!bootId}
      onClick={() => navigate({ to: "/logs", search: { tab: "supervisor", boot: bootId } })}
    >
      View boot
    </Button>
  );
}

/**
 * Column defs for the /logs removal-records table (docs/31 §5 phase 2).
 * The list is displayed from the accumulated infinite-query pages; paging
 * itself stays hook-driven ("Load more"). Row testids are attached via
 * DataTable's `getRowProps`.
 */
export const removalsColumns = () =>
  columnHelper.columns([
    columnHelper.accessor("ts", {
      header: "Time",
      meta: { cellClassName: "whitespace-nowrap text-xs" },
      cell: (info) => formatTimestamp(info.getValue()),
    }),
    columnHelper.accessor("poolName", {
      header: "Pool",
      meta: { cellClassName: "text-xs" },
      cell: (info) => info.getValue() || "—",
    }),
    columnHelper.display({
      id: "runner",
      header: "Runner",
      meta: { cellClassName: "font-mono text-xs" },
      cell: ({ row }) => row.original.runnerName || row.original.runnerId,
    }),
    columnHelper.accessor("reason", {
      header: "Reason",
      cell: (info) => (
        <Badge
          data-testid="logs-reason-badge"
          className={cn("border", REASON_BADGE_CLASS[info.getValue()])}
        >
          {info.getValue()}
        </Badge>
      ),
    }),
    columnHelper.accessor("providerBusy", {
      header: "Busy",
      cell: (info) =>
        info.getValue() ? (
          <Badge className="border border-warning/30 bg-warning/10 text-warning">busy</Badge>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    }),
    columnHelper.accessor("exitCode", {
      header: "Exit",
      meta: { cellClassName: "font-mono text-xs" },
      cell: (info) => (info.getValue() >= 0 ? info.getValue() : "—"),
    }),
    columnHelper.display({
      id: "capture",
      header: "Capture",
      cell: ({ row }) =>
        row.original.captureOk ? (
          <Badge className="border border-success/30 bg-success/10 text-success">
            {formatBytes(row.original.captureBytes)}
          </Badge>
        ) : (
          <Badge variant="secondary">none</Badge>
        ),
    }),
    columnHelper.display({
      id: "actions",
      header: "",
      cell: ({ row }) => (
        <div className="flex justify-end gap-1">
          <ViewCaptureCell runnerId={row.original.runnerId} />
          <ViewBootCell bootId={row.original.bootId} />
        </div>
      ),
    }),
  ]);

export { formatBytes, formatTimestamp };
