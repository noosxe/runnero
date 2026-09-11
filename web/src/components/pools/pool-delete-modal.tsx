import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useNavigate } from "@tanstack/react-router";
import { AlertTriangle, Loader2, Trash2 } from "lucide-react";
import { useDeletePool } from "../../lib/api/query-hooks";

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

  if (!isOpen) return null;

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
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-slate-950/60 backdrop-blur-xs p-4">
      <div className="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
        <div className="flex items-center gap-3 text-rose-600 dark:text-rose-400">
          <AlertTriangle className="h-6 w-6" />
          <h3 className="text-base font-bold text-slate-900 dark:text-white">Delete Pool?</h3>
        </div>

        <p className="mt-3 text-xs text-slate-600 dark:text-slate-300 leading-relaxed">
          This permanently removes pool{" "}
          <strong className="font-mono text-slate-900 dark:text-white">{poolName}</strong> and its
          configuration. Currently tracked:{" "}
          <strong className="text-slate-900 dark:text-white">{idleCount} idle</strong> ·{" "}
          <strong className="text-slate-900 dark:text-white">{busyCount} busy</strong>. Idle runners
          are removed immediately either way — choose what happens to busy runners.
        </p>

        <div className="mt-4 space-y-2" role="radiogroup" aria-label="Drain mode">
          <label
            className={`flex cursor-pointer items-start gap-2.5 rounded-xl border p-3 text-xs transition-colors ${
              mode === "drain"
                ? "border-blue-400 bg-blue-50 dark:border-blue-700 dark:bg-blue-950/40"
                : "border-slate-200 hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800"
            }`}
          >
            <input
              type="radio"
              name="pool-delete-mode"
              value="drain"
              checked={mode === "drain"}
              onChange={() => setMode("drain")}
              className="mt-0.5"
            />
            <span>
              <span className="block font-semibold text-slate-900 dark:text-white">
                Drain gracefully
              </span>
              <span className="mt-0.5 block text-slate-500 dark:text-slate-400">
                {busyCount} busy runner{busyCount === 1 ? "" : "s"} finish
                {busyCount === 1 ? "es" : ""} the current job before teardown. Hung jobs are
                force-terminated after {backstopLabel}.
              </span>
            </span>
          </label>

          <label
            className={`flex cursor-pointer items-start gap-2.5 rounded-xl border p-3 text-xs transition-colors ${
              mode === "terminate"
                ? "border-rose-400 bg-rose-50 dark:border-rose-700 dark:bg-rose-950/40"
                : "border-slate-200 hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800"
            }`}
          >
            <input
              type="radio"
              name="pool-delete-mode"
              value="terminate"
              checked={mode === "terminate"}
              onChange={() => setMode("terminate")}
              className="mt-0.5"
            />
            <span>
              <span className="block font-semibold text-slate-900 dark:text-white">
                Terminate everything now
              </span>
              <span className="mt-0.5 block text-slate-500 dark:text-slate-400">
                All runners are terminated immediately. Running jobs fail and are recorded as
                interrupted.
              </span>
            </span>
          </label>
        </div>

        {error && (
          <p className="mt-3 rounded-xl bg-rose-50 px-3 py-2 text-xs font-medium text-rose-700 dark:bg-rose-950/40 dark:text-rose-400">
            {error}
          </p>
        )}

        <div className="mt-6 flex items-center justify-end gap-3">
          <Button variant="outline" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button
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
          </Button>
        </div>
      </div>
    </div>
  );
}
