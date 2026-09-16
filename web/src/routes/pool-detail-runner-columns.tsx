import { createColumnHelper } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "cn";
import { Clock, Ellipsis, Terminal, Trash2 } from "lucide-react";
import type { Pool, RunnerInstance } from "../gen/api_pb";
import type { AppTableFeatures } from "../lib/tables/use-app-table";

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

const columnHelper = createColumnHelper<AppTableFeatures, RunnerInstance>();

type RunnerColumnActions = {
  /** Undefined before the pool query resolves; limits fall back like the page does. */
  pool: Pool | undefined;
  onViewLogs: (runner: RunnerInstance) => void;
  onTerminate: (runner: RunnerInstance) => void;
};

/**
 * Column defs for the pool-detail runners table (docs/31 §5 phase 3).
 * The table live-updates (polling) — call sites memoize the factory
 * result so column identity stays stable across ticks. The actions
 * column closes over the page's runner dialogs (logs viewer, terminate
 * confirm), mirroring the original hand-rolled row component.
 */
export const runnerColumns = ({ pool, onViewLogs, onTerminate }: RunnerColumnActions) =>
  columnHelper.columns([
    columnHelper.accessor("containerId", {
      header: "Container ID",
      meta: { cellClassName: "font-mono font-semibold" },
      cell: (info) => info.getValue().substring(0, 12),
    }),
    columnHelper.accessor("name", {
      header: "Runner Name",
      meta: { cellClassName: "font-mono font-medium" },
    }),
    columnHelper.accessor("status", {
      header: "State",
      cell: ({ row }) => {
        const isBusy = row.original.status === "busy";
        const isIdle = row.original.status === "idle";
        const isDegraded = row.original.status === "degraded";
        return (
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
              className={cn(
                "size-1.5 rounded-full",
                isBusy
                  ? "bg-success animate-pulse"
                  : isIdle
                    ? "bg-primary"
                    : isDegraded
                      ? "bg-destructive"
                      : "bg-muted-foreground",
              )}
            />
            <span>{row.original.status}</span>
          </Badge>
        );
      },
    }),
    columnHelper.accessor("ipAddress", {
      header: "IP Address",
      meta: { cellClassName: "font-mono text-muted-foreground" },
      cell: (info) => info.getValue() || "—",
    }),
    columnHelper.accessor("uptimeSeconds", {
      header: "Uptime",
      meta: { cellClassName: "font-mono" },
      cell: ({ row }) => (
        <span className="inline-flex items-center gap-1">
          <Clock className="size-3 text-muted-foreground" />
          {formatUptime(row.original.uptimeSeconds)}
        </span>
      ),
    }),
    columnHelper.display({
      id: "limits",
      header: "CPU / Mem Limit",
      meta: { cellClassName: "text-muted-foreground" },
      cell: ({ row }) => (
        <>
          {row.original.cpuLimit || pool?.cpuLimit || "Unlimited"} /{" "}
          {row.original.memoryLimit || pool?.memoryLimit || "Unlimited"}
        </>
      ),
    }),
    columnHelper.display({
      id: "actions",
      header: "Actions",
      meta: { headerClassName: "text-right", cellClassName: "text-right" },
      cell: ({ row }) => (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={<Button variant="ghost" size="icon-sm" aria-label="Runner actions" />}
          >
            <Ellipsis />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuGroup>
              <DropdownMenuItem onClick={() => onViewLogs(row.original)}>
                <Terminal />
                Logs
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onClick={() => onTerminate(row.original)}>
                <Trash2 />
                Terminate
              </DropdownMenuItem>
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      ),
    }),
  ]);

export { formatUptime };
