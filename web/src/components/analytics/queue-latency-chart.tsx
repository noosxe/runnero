import { useMemo } from "react";
import { Area, AreaChart, CartesianGrid, ReferenceLine, XAxis, YAxis } from "recharts";
import { Button } from "@/components/ui/button";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart";
import { cn } from "cn";
import type { LatencyBucket } from "../../gen/api_pb";
import { CapacityHealthBadge } from "./capacity-health-badge";
import { TrendingUp, Clock, Info } from "lucide-react";

export interface QueueLatencyChartProps {
  trend: LatencyBucket[];
  averageQueueSeconds: number;
  timeframeHours: number;
  onTimeframeChange: (hours: number) => void;
}

function formatHour(isoString: string): string {
  if (!isoString) return "";
  try {
    const d = new Date(isoString);
    if (isNaN(d.getTime())) return isoString;
    return d.toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    });
  } catch {
    return isoString;
  }
}

const chartConfig = {
  latency: {
    label: "Queue Latency",
    color: "var(--chart-1)",
  },
  runtime: {
    label: "Avg Runtime",
    color: "var(--chart-2)",
  },
  jobs: {
    label: "Jobs dispatched",
    color: "var(--chart-3)",
  },
} satisfies ChartConfig;

export function QueueLatencyChart({
  trend,
  averageQueueSeconds,
  timeframeHours,
  onTimeframeChange,
}: QueueLatencyChartProps) {
  // Sort data points chronologically
  const points = useMemo(() => {
    if (!trend || trend.length === 0) return [];
    return [...trend].sort((a, b) => a.timestamp.localeCompare(b.timestamp));
  }, [trend]);

  const data = useMemo(
    () =>
      points.map((p) => ({
        timestamp: p.timestamp,
        latency: p.avgQueueSeconds,
        runtime: p.avgRuntimeSeconds,
        jobs: p.totalJobs,
      })),
    [points],
  );

  // Max latency ceiling for the Y axis (minimum 10s for visual clarity)
  const maxLatency = useMemo(() => {
    let max = 10;
    for (const p of points) {
      if (p.avgQueueSeconds > max) max = p.avgQueueSeconds;
    }
    return Math.ceil(max * 1.2);
  }, [points]);

  return (
    <div className="rounded-2xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
      {/* Header */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between border-b border-slate-100 pb-4 dark:border-slate-800">
        <div>
          <div className="flex items-center gap-2">
            <TrendingUp className="h-4 w-4 text-blue-500" />
            <h2 className="text-base font-bold text-slate-900 dark:text-white">
              Queue Wait-Time Latency
            </h2>
          </div>
          <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
            Dispatch latency (<code>started_at − queued_at</code>) tracked over time to measure pool
            dispatch capacity.
          </p>
        </div>

        <div className="flex items-center gap-3">
          <CapacityHealthBadge avgQueueSeconds={averageQueueSeconds} />

          {/* Timeframe Selector */}
          <div className="flex items-center rounded-xl border border-slate-200 bg-slate-50 p-0.5 text-xs font-semibold dark:border-slate-800 dark:bg-slate-800">
            <Button
              variant="ghost"
              size="xs"
              onClick={() => onTimeframeChange(24)}
              aria-pressed={timeframeHours === 24}
              className={cn(timeframeHours === 24 && "bg-background text-foreground shadow-2xs")}
            >
              24h
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => onTimeframeChange(168)}
              aria-pressed={timeframeHours === 168}
              className={cn(timeframeHours === 168 && "bg-background text-foreground shadow-2xs")}
            >
              7d
            </Button>
          </div>
        </div>
      </div>

      {/* Chart Canvas */}
      <div className="relative mt-4">
        {points.length === 0 ? (
          <div className="flex h-52 flex-col items-center justify-center text-center text-xs text-slate-400">
            <Clock className="h-6 w-6 text-slate-300 dark:text-slate-600 mb-1" />
            <p className="font-semibold text-slate-600 dark:text-slate-400">
              No queue latency data yet
            </p>
            <p className="text-[11px] text-slate-400 mt-0.5">
              Completed and in-flight runner jobs will generate latency trends.
            </p>
          </div>
        ) : (
          <ChartContainer config={chartConfig} className="h-56 w-full aspect-auto">
            <AreaChart accessibilityLayer data={data} margin={{ left: 12, right: 12, top: 8 }}>
              <defs>
                <linearGradient id="fillLatency" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="var(--color-latency)" stopOpacity={0.25} />
                  <stop offset="100%" stopColor="var(--color-latency)" stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid vertical={false} strokeDasharray="3 3" />
              <XAxis
                dataKey="timestamp"
                tickLine={false}
                axisLine={false}
                tickMargin={8}
                minTickGap={40}
                tickFormatter={(value) => formatHour(value)}
              />
              <YAxis
                tickLine={false}
                axisLine={false}
                width={40}
                domain={[0, maxLatency]}
                ticks={[0, Math.round(maxLatency / 2), maxLatency]}
                tickFormatter={(value) => `${value}s`}
              />
              <ChartTooltip
                cursor={{ stroke: "var(--color-latency)", strokeDasharray: "2 2" }}
                content={
                  <ChartTooltipContent
                    labelFormatter={(label) => formatHour(String(label))}
                    formatter={(value, name) => (
                      <div className="flex w-full flex-wrap items-stretch gap-2">
                        <span
                          className="mt-0.5 size-2.5 shrink-0 rounded-[2px] bg-(--color-latency)"
                          aria-hidden
                        />
                        <span className="text-muted-foreground">
                          {chartConfig[name as keyof typeof chartConfig]?.label ?? name}
                        </span>
                        <span className="ml-auto font-mono font-medium tabular-nums text-foreground">
                          {name === "jobs"
                            ? `${value}`
                            : name === "runtime"
                              ? `${Math.round(Number(value))}s`
                              : `${Number(value).toFixed(1)}s`}
                        </span>
                      </div>
                    )}
                  />
                }
              />
              {/* Optimal reference threshold line (5s) */}
              {maxLatency >= 5 && (
                <ReferenceLine
                  y={5}
                  stroke="var(--success)"
                  strokeDasharray="4 4"
                  strokeOpacity={0.4}
                  label={{
                    value: "5s (optimal)",
                    position: "insideTopRight",
                    fill: "var(--success)",
                    fontSize: 9,
                    fontFamily: "monospace",
                    opacity: 0.7,
                  }}
                />
              )}
              {/* Constrained threshold line (30s) */}
              {maxLatency >= 30 && (
                <ReferenceLine
                  y={30}
                  stroke="var(--destructive)"
                  strokeDasharray="4 4"
                  strokeOpacity={0.4}
                  label={{
                    value: "30s (bottleneck)",
                    position: "insideTopRight",
                    fill: "var(--destructive)",
                    fontSize: 9,
                    fontFamily: "monospace",
                    opacity: 0.7,
                  }}
                />
              )}
              <Area
                dataKey="latency"
                type="monotone"
                stroke="var(--color-latency)"
                strokeWidth={2.5}
                fill="url(#fillLatency)"
                dot={{ r: 3 }}
                activeDot={{ r: 5 }}
              />
            </AreaChart>
          </ChartContainer>
        )}
      </div>

      {/* Footer Notes */}
      <div className="mt-3 flex items-center gap-1.5 text-[11px] text-slate-400 dark:text-slate-500">
        <Info className="h-3.5 w-3.5 shrink-0" />
        <span>
          Lower queue latency means workflow runs execute immediately without container launch
          delays.
        </span>
      </div>
    </div>
  );
}
