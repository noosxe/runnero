import { CheckCircle2, XCircle, PieChart, Timer } from "lucide-react";

export interface SuccessFailureWidgetProps {
  totalJobs: number;
  successfulJobs: number;
  failedJobs: number;
  /** Null when no concluded jobs exist in the window (docs/21 §5.6) — renders as "—". */
  successRatePercent: number | null;
  averageRuntimeSeconds: number;
}

function formatDuration(seconds: number): string {
  if (!seconds || seconds <= 0) return "0s";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const mins = Math.floor(seconds / 60);
  const remSec = Math.round(seconds % 60);
  if (mins < 60) return `${mins}m ${remSec}s`;
  const hours = Math.floor(mins / 60);
  const remMins = mins % 60;
  return `${hours}h ${remMins}m`;
}

export function SuccessFailureWidget({
  totalJobs,
  successfulJobs,
  failedJobs,
  successRatePercent,
  averageRuntimeSeconds,
}: SuccessFailureWidgetProps) {
  const successPct = totalJobs > 0 ? (successfulJobs / totalJobs) * 100 : 100;
  const failurePct = totalJobs > 0 ? (failedJobs / totalJobs) * 100 : 0;

  return (
    <div className="flex flex-col justify-between rounded-2xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
      <div>
        {/* Header */}
        <div className="flex items-center justify-between border-b border-slate-100 pb-3 dark:border-slate-800">
          <div className="flex items-center gap-2">
            <PieChart className="h-4 w-4 text-emerald-500" />
            <h2 className="text-base font-bold text-slate-900 dark:text-white">
              Execution Health & Ratio
            </h2>
          </div>
          <span className="rounded-md bg-slate-100 px-2 py-0.5 text-xs font-semibold text-slate-600 dark:bg-slate-800 dark:text-slate-300">
            {totalJobs} total runs
          </span>
        </div>

        {/* Primary Metric Ring / Stat */}
        <div className="mt-4 flex items-center justify-between">
          <div>
            <div className="text-3xl font-extrabold tracking-tight text-slate-900 dark:text-white font-mono">
              {successRatePercent === null ? "—" : `${successRatePercent.toFixed(1)}%`}
            </div>
            <p className="text-xs font-medium text-slate-500 dark:text-slate-400 mt-0.5">
              {successRatePercent === null
                ? "No concluded jobs in window"
                : "Success Rate Across All Pools"}
            </p>
          </div>

          <div className="flex items-center gap-1.5 rounded-xl border border-slate-100 bg-slate-50 px-3 py-2 text-xs dark:border-slate-800 dark:bg-slate-800/60">
            <Timer className="h-4 w-4 text-indigo-500" />
            <div>
              <p className="text-[10px] uppercase font-semibold text-slate-400">Avg Runtime</p>
              <p className="font-mono font-bold text-slate-800 dark:text-slate-200">
                {formatDuration(averageRuntimeSeconds)}
              </p>
            </div>
          </div>
        </div>

        {/* Stacked Ratio Progress Bar */}
        <div className="mt-5 space-y-1.5">
          <div className="flex h-3 w-full overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
            <div
              style={{ width: `${successPct}%` }}
              className="bg-emerald-500 transition-all duration-500"
              title={`Success: ${successfulJobs} (${successPct.toFixed(1)}%)`}
            />
            <div
              style={{ width: `${failurePct}%` }}
              className="bg-rose-500 transition-all duration-500"
              title={`Failed: ${failedJobs} (${failurePct.toFixed(1)}%)`}
            />
          </div>

          <div className="flex items-center justify-between text-[11px] font-mono text-slate-500">
            <span>{successPct.toFixed(1)}% success</span>
            <span>{failurePct.toFixed(1)}% failures</span>
          </div>
        </div>
      </div>

      {/* Breakdown Cards */}
      <div className="mt-6 grid grid-cols-2 gap-3 border-t border-slate-100 pt-4 dark:border-slate-800">
        <div className="rounded-xl border border-emerald-100 bg-emerald-50/50 p-3 dark:border-emerald-950/60 dark:bg-emerald-950/20">
          <div className="flex items-center gap-1.5 text-xs font-semibold text-emerald-700 dark:text-emerald-400">
            <CheckCircle2 className="h-3.5 w-3.5" />
            <span>Successful</span>
          </div>
          <div className="mt-1 font-mono text-xl font-bold text-emerald-800 dark:text-emerald-300">
            {successfulJobs}
          </div>
          <p className="text-[10px] text-emerald-600/80 dark:text-emerald-400/70 mt-0.5">
            Exit status 0
          </p>
        </div>

        <div className="rounded-xl border border-rose-100 bg-rose-50/50 p-3 dark:border-rose-950/60 dark:bg-rose-950/20">
          <div className="flex items-center gap-1.5 text-xs font-semibold text-rose-700 dark:text-rose-400">
            <XCircle className="h-3.5 w-3.5" />
            <span>Failed</span>
          </div>
          <div className="mt-1 font-mono text-xl font-bold text-rose-800 dark:text-rose-300">
            {failedJobs}
          </div>
          <p className="text-[10px] text-rose-600/80 dark:text-rose-400/70 mt-0.5">
            Non-zero exit or cancelled
          </p>
        </div>
      </div>
    </div>
  );
}
