import { CheckCircle2, XCircle, PieChart, Timer } from "lucide-react";
import {
  Card,
  CardAction,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

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
    <Card className="gap-0 py-5">
      <CardHeader className="border-b [.border-b]:pb-3">
        <CardTitle className="flex items-center gap-2 text-base font-bold">
          <PieChart className="h-4 w-4 text-emerald-500" />
          Execution Health &amp; Ratio
        </CardTitle>
        <CardAction>
          <Badge variant="secondary">{totalJobs} total runs</Badge>
        </CardAction>
      </CardHeader>

      <CardContent className="mt-4">
        {/* Primary Metric Ring / Stat */}
        <div className="flex items-center justify-between">
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
              className="bg-success transition-all duration-500"
              title={`Success: ${successfulJobs} (${successPct.toFixed(1)}%)`}
            />
            <div
              style={{ width: `${failurePct}%` }}
              className="bg-destructive transition-all duration-500"
              title={`Failed: ${failedJobs} (${failurePct.toFixed(1)}%)`}
            />
          </div>

          <div className="flex items-center justify-between text-[11px] font-mono text-slate-500">
            <span>{successPct.toFixed(1)}% success</span>
            <span>{failurePct.toFixed(1)}% failures</span>
          </div>
        </div>
      </CardContent>

      {/* Breakdown Cards */}
      <CardFooter className="mt-6 border-t [.border-t]:pt-4">
        <div className="grid w-full grid-cols-2 gap-3">
          <div className="rounded-xl border border-success/20 bg-success/5 p-3">
            <div className="flex items-center gap-1.5 text-xs font-semibold text-success">
              <CheckCircle2 className="h-3.5 w-3.5" />
              <span>Successful</span>
            </div>
            <div className="mt-1 font-mono text-xl font-bold text-success">{successfulJobs}</div>
            <p className="text-[10px] text-success/80 mt-0.5">Exit status 0</p>
          </div>

          <div className="rounded-xl border border-destructive/20 bg-destructive/5 p-3">
            <div className="flex items-center gap-1.5 text-xs font-semibold text-destructive">
              <XCircle className="h-3.5 w-3.5" />
              <span>Failed</span>
            </div>
            <div className="mt-1 font-mono text-xl font-bold text-destructive">{failedJobs}</div>
            <p className="text-[10px] text-destructive/80 mt-0.5">Non-zero exit or cancelled</p>
          </div>
        </div>
      </CardFooter>
    </Card>
  );
}
