import { useState, useMemo } from "react";
import { Button } from "@/components/ui/button";
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

export function QueueLatencyChart({
  trend,
  averageQueueSeconds,
  timeframeHours,
  onTimeframeChange,
}: QueueLatencyChartProps) {
  const [hoveredIdx, setHoveredIdx] = useState<number | null>(null);

  // Pad or sort data points
  const points = useMemo(() => {
    if (!trend || trend.length === 0) return [];
    return [...trend].sort((a, b) => a.timestamp.localeCompare(b.timestamp));
  }, [trend]);

  // Max latency ceiling for SVG scaling (minimum 10s for visual clarity)
  const maxLatency = useMemo(() => {
    let max = 10;
    for (const p of points) {
      if (p.avgQueueSeconds > max) max = p.avgQueueSeconds;
    }
    return Math.ceil(max * 1.2);
  }, [points]);

  // Dimensions
  const svgWidth = 700;
  const svgHeight = 220;
  const paddingX = 45;
  const paddingY = 25;
  const chartWidth = svgWidth - paddingX * 2;
  const chartHeight = svgHeight - paddingY * 2;

  // Compute SVG coordinates
  const coords = useMemo(() => {
    if (points.length === 0) return [];
    const step = points.length > 1 ? chartWidth / (points.length - 1) : chartWidth / 2;

    return points.map((p, idx) => {
      const x = points.length === 1 ? paddingX + chartWidth / 2 : paddingX + idx * step;
      const normalizedY = Math.min(1, Math.max(0, p.avgQueueSeconds / maxLatency));
      const y = paddingY + chartHeight - normalizedY * chartHeight;
      return { x, y, point: p };
    });
  }, [points, chartWidth, chartHeight, maxLatency]);

  // Generate SVG polyline / path
  const linePath = useMemo(() => {
    if (coords.length === 0) return "";
    return coords.reduce((acc, c, idx) => {
      return idx === 0 ? `M ${c.x} ${c.y}` : `${acc} L ${c.x} ${c.y}`;
    }, "");
  }, [coords]);

  const areaPath = useMemo(() => {
    if (coords.length === 0) return "";
    const first = coords[0];
    const last = coords[coords.length - 1];
    const bottomY = paddingY + chartHeight;
    return `${linePath} L ${last.x} ${bottomY} L ${first.x} ${bottomY} Z`;
  }, [coords, linePath, chartHeight]);

  const optimalY = paddingY + chartHeight - Math.min(1, 5 / maxLatency) * chartHeight;
  const constrainedY = paddingY + chartHeight - Math.min(1, 30 / maxLatency) * chartHeight;

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
          <div className="relative overflow-x-auto">
            <svg
              viewBox={`0 0 ${svgWidth} ${svgHeight}`}
              className="w-full h-56 select-none font-mono text-[10px]"
            >
              <defs>
                <linearGradient id="queueAreaGrad" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#3B82F6" stopOpacity="0.25" />
                  <stop offset="100%" stopColor="#3B82F6" stopOpacity="0.0" />
                </linearGradient>
              </defs>

              {/* Grid Lines & Thresholds */}
              <line
                x1={paddingX}
                y1={paddingY}
                x2={svgWidth - paddingX}
                y2={paddingY}
                stroke="currentColor"
                className="text-slate-100 dark:text-slate-800/80"
                strokeDasharray="3 3"
              />
              <line
                x1={paddingX}
                y1={paddingY + chartHeight / 2}
                x2={svgWidth - paddingX}
                y2={paddingY + chartHeight / 2}
                stroke="currentColor"
                className="text-slate-100 dark:text-slate-800/80"
                strokeDasharray="3 3"
              />
              <line
                x1={paddingX}
                y1={paddingY + chartHeight}
                x2={svgWidth - paddingX}
                y2={paddingY + chartHeight}
                stroke="currentColor"
                className="text-slate-200 dark:text-slate-800"
              />

              {/* Optimal reference threshold line (5s) */}
              {maxLatency >= 5 && (
                <g>
                  <line
                    x1={paddingX}
                    y1={optimalY}
                    x2={svgWidth - paddingX}
                    y2={optimalY}
                    stroke="#10B981"
                    strokeWidth="1"
                    strokeDasharray="4 4"
                    opacity="0.4"
                  />
                  <text
                    x={svgWidth - paddingX + 4}
                    y={optimalY + 3}
                    fill="#10B981"
                    className="text-[9px] font-mono select-none opacity-70"
                  >
                    5s (optimal)
                  </text>
                </g>
              )}

              {/* Constrained threshold line (30s) */}
              {maxLatency >= 30 && (
                <g>
                  <line
                    x1={paddingX}
                    y1={constrainedY}
                    x2={svgWidth - paddingX}
                    y2={constrainedY}
                    stroke="#EF4444"
                    strokeWidth="1"
                    strokeDasharray="4 4"
                    opacity="0.4"
                  />
                  <text
                    x={svgWidth - paddingX + 4}
                    y={constrainedY + 3}
                    fill="#EF4444"
                    className="text-[9px] font-mono select-none opacity-70"
                  >
                    30s (bottleneck)
                  </text>
                </g>
              )}

              {/* Y-Axis Labels */}
              <text
                x={paddingX - 6}
                y={paddingY + 3}
                textAnchor="end"
                className="fill-slate-400 text-[10px]"
              >
                {maxLatency}s
              </text>
              <text
                x={paddingX - 6}
                y={paddingY + chartHeight / 2 + 3}
                textAnchor="end"
                className="fill-slate-400 text-[10px]"
              >
                {Math.round(maxLatency / 2)}s
              </text>
              <text
                x={paddingX - 6}
                y={paddingY + chartHeight + 3}
                textAnchor="end"
                className="fill-slate-400 text-[10px]"
              >
                0s
              </text>

              {/* Area fill under curve */}
              <path d={areaPath} fill="url(#queueAreaGrad)" />

              {/* Trend Curve Line */}
              <path
                d={linePath}
                fill="none"
                stroke="#3B82F6"
                strokeWidth="2.5"
                strokeLinecap="round"
                strokeLinejoin="round"
              />

              {/* Interactive Data Points */}
              {coords.map((c, idx) => {
                const isHovered = hoveredIdx === idx;
                return (
                  <g key={idx}>
                    {/* Vertical guide line on hover */}
                    {isHovered && (
                      <line
                        x1={c.x}
                        y1={paddingY}
                        x2={c.x}
                        y2={paddingY + chartHeight}
                        stroke="#3B82F6"
                        strokeWidth="1"
                        strokeDasharray="2 2"
                        opacity="0.6"
                      />
                    )}

                    <circle
                      cx={c.x}
                      cy={c.y}
                      r={isHovered ? 5.5 : 3.5}
                      fill={isHovered ? "#2563EB" : "#3B82F6"}
                      stroke="#FFFFFF"
                      strokeWidth="1.5"
                      className="transition-all cursor-pointer"
                      onMouseEnter={() => setHoveredIdx(idx)}
                      onMouseLeave={() => setHoveredIdx(null)}
                    />
                  </g>
                );
              })}

              {/* X-Axis Timestamps */}
              {coords.map((c, idx) => {
                // Render every Nth label to prevent overlap
                const interval = Math.max(1, Math.floor(coords.length / 6));
                if (idx % interval !== 0 && idx !== coords.length - 1) return null;
                return (
                  <text
                    key={`label-${idx}`}
                    x={c.x}
                    y={paddingY + chartHeight + 16}
                    textAnchor="middle"
                    className="fill-slate-400 text-[9px]"
                  >
                    {formatHour(c.point.timestamp)}
                  </text>
                );
              })}
            </svg>

            {/* Hover Tooltip Card */}
            {hoveredIdx !== null && coords[hoveredIdx] && (
              <div
                className="absolute z-10 -translate-x-1/2 -translate-y-full pointer-events-none rounded-xl border border-slate-700 bg-slate-900/95 p-2.5 text-[11px] font-mono text-white shadow-xl backdrop-blur-xs transition-all"
                style={{
                  left: `${(coords[hoveredIdx].x / svgWidth) * 100}%`,
                  top: `${(coords[hoveredIdx].y / svgHeight) * 100 - 8}%`,
                }}
              >
                <div className="font-bold text-blue-400">
                  {formatHour(coords[hoveredIdx].point.timestamp)}
                </div>
                <div className="mt-1 flex items-center justify-between gap-4">
                  <span className="text-slate-400">Queue Latency:</span>
                  <span className="font-bold text-emerald-400">
                    {coords[hoveredIdx].point.avgQueueSeconds.toFixed(1)}s
                  </span>
                </div>
                <div className="flex items-center justify-between gap-4">
                  <span className="text-slate-400">Avg Runtime:</span>
                  <span className="font-bold text-slate-200">
                    {coords[hoveredIdx].point.avgRuntimeSeconds.toFixed(0)}s
                  </span>
                </div>
                <div className="flex items-center justify-between gap-4 border-t border-slate-800 pt-1 mt-1 text-[10px] text-slate-400">
                  <span>Jobs dispatched:</span>
                  <span className="text-white">{coords[hoveredIdx].point.totalJobs}</span>
                </div>
              </div>
            )}
          </div>
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
