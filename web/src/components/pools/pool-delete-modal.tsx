import { useState } from "react";
import { Input } from "@/components/ui/input";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogMedia,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { useNavigate } from "@tanstack/react-router";
import { AlertTriangle, Loader2, Trash2 } from "lucide-react";
import { useDeletePool } from "../../lib/api/query-hooks";
import { cn } from "cn";

export type PoolDeleteMode = "drain" | "terminate";

export interface PoolDeleteModalProps {
  isOpen: boolean;
  onClose: () => void;
  poolId: bigint;
  poolName: string;
  /** Number of runners currently executing a job; drives the preselection (docs/25 §8.2). */
  busyCount: number;
  /** Number of idle standby runners. */
  idleCount: number;
  /** The pool's max job lifetime in seconds (0/unset = the 6h default backstop, docs/25 §8.3). */
  maxRunnerLifetimeSeconds?: number;
}

/**
 * Pool delete confirmation (RUN-127, docs/25 §4.7): the first delete UI for
 * pools. Shows the idle/busy runner split and lets the user choose between
 * graceful drain (busy runners finish their current job) and the hard
 * terminate-everything behavior. Idle runners are removed immediately in
 * both modes.
 */
export function PoolDeleteModal({
  isOpen,
  onClose,
  poolId,
  poolName,
  busyCount,
  idleCount,
  maxRunnerLifetimeSeconds,
}: PoolDeleteModalProps) {
  const navigate = useNavigate();
  const deletePool = useDeletePool();

  // Preselection follows the busy count (docs/25 §8.2): drain is the
  // conservative choice when work is in flight; terminate otherwise.
  const [mode, setMode] = useState<PoolDeleteMode>(busyCount > 0 ? "drain" : "terminate");
  const [error, setError] = useState<string | null>(null);

  const backstopLabel = maxRunnerLifetimeSeconds
    ? `${maxRunnerLifetimeSeconds}s`
    : "6h (default backstop)";

  const handleConfirm = async () => {
    setError(null);
    try {
      await deletePool.mutateAsync({ id: poolId, drainGraceful: mode === "drain" });
      navigate({ to: "/pools" });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete pool");
    }
  };

  return (
    <AlertDialog
      open={isOpen}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <AlertDialogContent size="sm">
        <AlertDialogMedia className="bg-destructive/10 text-destructive">
          <AlertTriangle />
        </AlertDialogMedia>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete Pool?</AlertDialogTitle>
          <AlertDialogDescription>
            This permanently removes pool{" "}
            <strong className="font-mono text-foreground">{poolName}</strong> and its configuration.
            Currently tracked: <strong className="text-foreground">{idleCount} idle</strong> ·{" "}
            <strong className="text-foreground">{busyCount} busy</strong>. Idle runners are removed
            immediately either way — choose what happens to busy runners.
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div className="grid gap-2" role="radiogroup" aria-label="Drain mode">
          <label
            className={cn(
              "flex cursor-pointer items-start gap-2.5 rounded-xl border p-3 text-xs transition-colors",
              mode === "drain"
                ? "border-primary/50 bg-primary/5"
                : "border-border hover:bg-muted/50",
            )}
          >
            <Input
              type="radio"
              name="pool-delete-mode"
              value="drain"
              checked={mode === "drain"}
              onChange={() => setMode("drain")}
              className="mt-0.5"
            />
            <span>
              <span className="block font-semibold text-foreground">Drain gracefully</span>
              <span className="mt-0.5 block text-muted-foreground">
                {busyCount} busy runner{busyCount === 1 ? "" : "s"} finish
                {busyCount === 1 ? "es" : ""} the current job before teardown. Hung jobs are
                force-terminated after {backstopLabel}.
              </span>
            </span>
          </label>

          <label
            className={cn(
              "flex cursor-pointer items-start gap-2.5 rounded-xl border p-3 text-xs transition-colors",
              mode === "terminate"
                ? "border-destructive/50 bg-destructive/5"
                : "border-border hover:bg-muted/50",
            )}
          >
            <Input
              type="radio"
              name="pool-delete-mode"
              value="terminate"
              checked={mode === "terminate"}
              onChange={() => setMode("terminate")}
              className="mt-0.5"
            />
            <span>
              <span className="block font-semibold text-foreground">Terminate everything now</span>
              <span className="mt-0.5 block text-muted-foreground">
                All runners are terminated immediately. Running jobs fail and are recorded as
                interrupted.
              </span>
            </span>
          </label>
        </div>

        {error && (
          <p className="rounded-xl bg-destructive/10 px-3 py-2 text-xs font-medium text-destructive">
            {error}
          </p>
        )}

        <AlertDialogFooter>
          <AlertDialogCancel size="sm" disabled={deletePool.isPending}>
            Cancel
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            size="sm"
            onClick={handleConfirm}
            disabled={deletePool.isPending}
          >
            {deletePool.isPending ? (
              <Loader2 data-icon="inline-start" className="animate-spin" />
            ) : (
              <Trash2 data-icon="inline-start" />
            )}
            {mode === "drain" ? "Delete & Drain" : "Delete & Terminate"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
