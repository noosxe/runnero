import { useState, useRef, useEffect, useMemo, useCallback } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { cn } from "cn";
import type { LogChunk } from "../../gen/api_pb";
import {
  Play,
  Pause,
  ArrowDown,
  Download,
  Copy,
  Check,
  Trash2,
  Search,
  Radio,
  Clock,
  Terminal,
} from "lucide-react";

export interface LogTerminalProps {
  logs: LogChunk[];
  mode: "live" | "historical";
  runnerName: string;
  isConnected?: boolean;
  isConnecting?: boolean;
  isLoading?: boolean;
  onClear?: () => void;
  title?: string;
  containerId?: string;
  /** Reserve room at the top-right for an overlaid dialog close button. */
  headerRightInset?: boolean;
}

export function LogTerminal({
  logs,
  mode,
  runnerName,
  isConnected = false,
  isConnecting = false,
  isLoading = false,
  onClear,
  title,
  containerId,
  headerRightInset = false,
}: LogTerminalProps) {
  const [isPaused, setIsPaused] = useState(false);
  const [autoScroll, setAutoScroll] = useState(true);
  const [streamFilter, setStreamFilter] = useState<"all" | "stdout" | "stderr">("all");
  const [search, setSearch] = useState("");
  const [copied, setCopied] = useState(false);

  // Snapshot logs when paused
  const [frozenLogs, setFrozenLogs] = useState<LogChunk[]>([]);

  const togglePause = useCallback(() => {
    setIsPaused((prev) => {
      const next = !prev;
      if (next) {
        setFrozenLogs([...logs]);
      }
      return next;
    });
  }, [logs]);

  const displayedLogs = isPaused ? frozenLogs : logs;

  const filteredLogs = useMemo(() => {
    return displayedLogs.filter((chunk) => {
      if (streamFilter !== "all" && chunk.stream !== streamFilter) {
        return false;
      }
      if (search.trim()) {
        const q = search.toLowerCase();
        return (
          chunk.content.toLowerCase().includes(q) ||
          chunk.timestamp.toLowerCase().includes(q) ||
          chunk.stream.toLowerCase().includes(q)
        );
      }
      return true;
    });
  }, [displayedLogs, streamFilter, search]);

  const terminalRef = useRef<HTMLDivElement>(null);

  // Auto-scroll to bottom on new logs when enabled and not paused
  useEffect(() => {
    if (autoScroll && !isPaused && terminalRef.current) {
      terminalRef.current.scrollTop = terminalRef.current.scrollHeight;
    }
  }, [logs, autoScroll, isPaused]);

  // Scroll listener to detect manual scrolling
  const handleScroll = () => {
    if (!terminalRef.current) return;
    const { scrollTop, scrollHeight, clientHeight } = terminalRef.current;
    const distanceToBottom = scrollHeight - scrollTop - clientHeight;
    if (distanceToBottom > 40 && autoScroll) {
      setAutoScroll(false);
    } else if (distanceToBottom <= 10 && !autoScroll) {
      setAutoScroll(true);
    }
  };

  const scrollToBottom = () => {
    if (terminalRef.current) {
      terminalRef.current.scrollTop = terminalRef.current.scrollHeight;
      setAutoScroll(true);
    }
  };

  const handleCopyAll = async () => {
    if (displayedLogs.length === 0) return;
    const text = displayedLogs.map((l) => `[${l.timestamp}] [${l.stream}] ${l.content}`).join("\n");
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // ignore
    }
  };

  const handleDownload = () => {
    if (displayedLogs.length === 0) return;
    const text = displayedLogs.map((l) => `[${l.timestamp}] [${l.stream}] ${l.content}`).join("\n");
    const blob = new Blob([text], { type: "text/plain;charset=utf-8;" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${runnerName || "runner"}-logs.txt`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  };

  return (
    <div className="flex h-full flex-col overflow-hidden rounded-2xl border border-slate-800 bg-slate-950 font-mono shadow-2xl text-xs">
      {/* Terminal Top Bar */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-800 bg-slate-900/90 px-4 py-3">
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <Terminal className="h-4 w-4 text-blue-400" />
            <span className="font-bold text-slate-100">
              {title || runnerName || "Terminal Console"}
            </span>
          </div>

          {containerId && (
            <span className="rounded bg-slate-800 px-1.5 py-0.5 text-[10px] text-slate-400">
              {containerId.substring(0, 12)}
            </span>
          )}

          {/* Status Indicator */}
          {mode === "live" ? (
            <Badge
              className={cn(
                "border",
                isPaused
                  ? "border-warning/30 bg-warning/10 text-warning"
                  : isConnected
                    ? "border-success/30 bg-success/10 text-success"
                    : isConnecting
                      ? "border-primary/30 bg-primary/10 text-primary"
                      : "border-destructive/30 bg-destructive/10 text-destructive",
              )}
            >
              <span
                className={`h-1.5 w-1.5 rounded-full ${
                  isPaused
                    ? "bg-amber-400"
                    : isConnected
                      ? "bg-emerald-400 animate-pulse"
                      : "bg-sky-400 animate-ping"
                }`}
              />
              <span>
                {isPaused
                  ? "Stream Paused"
                  : isConnected
                    ? "Live Stream"
                    : isConnecting
                      ? "Connecting..."
                      : "Offline"}
              </span>
            </Badge>
          ) : (
            <Badge variant="secondary" className="gap-1">
              <Clock className="h-3 w-3 text-slate-400" />
              <span>Historical Archive</span>
            </Badge>
          )}
        </div>

        {/* Action Controls */}
        <div className={`flex items-center gap-1.5 ${headerRightInset ? "mr-9" : ""}`}>
          {mode === "live" && (
            <Button
              variant="outline"
              size="xs"
              onClick={togglePause}
              aria-pressed={isPaused}
              className={cn(
                isPaused && "border-warning/60 bg-warning/10 text-warning hover:bg-warning/20",
              )}
            >
              {isPaused ? <Play data-icon="inline-start" /> : <Pause data-icon="inline-start" />}
              <span>{isPaused ? "Resume" : "Pause"}</span>
            </Button>
          )}

          <Button
            variant="outline"
            size="xs"
            onClick={() => setAutoScroll((prev) => !prev)}
            aria-pressed={autoScroll}
            className={cn(autoScroll && "border-primary/60 bg-primary/10 text-primary")}
          >
            <ArrowDown
              data-icon="inline-start"
              className={autoScroll ? "text-primary" : "text-muted-foreground"}
            />
            <span>Auto-scroll: {autoScroll ? "ON" : "OFF"}</span>
          </Button>

          <Button
            variant="outline"
            size="xs"
            onClick={handleCopyAll}
            disabled={displayedLogs.length === 0}
          >
            {copied ? (
              <Check data-icon="inline-start" className="text-success" />
            ) : (
              <Copy data-icon="inline-start" />
            )}
            <span>{copied ? "Copied" : "Copy"}</span>
          </Button>

          <Button
            variant="outline"
            size="xs"
            onClick={handleDownload}
            disabled={displayedLogs.length === 0}
          >
            <Download data-icon="inline-start" />
            <span>Export</span>
          </Button>

          {onClear && mode === "live" && (
            <Button
              variant="outline"
              size="xs"
              onClick={onClear}
              className="text-muted-foreground hover:text-destructive"
            >
              <Trash2 data-icon="inline-start" />
              <span>Clear</span>
            </Button>
          )}
        </div>
      </div>

      {/* Filter and Search Toolbar */}
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-800/80 bg-slate-900/40 px-4 py-2">
        <div className="flex items-center gap-2 flex-1 max-w-sm">
          <div className="relative w-full">
            <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-slate-500" />
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Filter log output..."
              className="w-full rounded-lg border border-slate-800 bg-slate-950 py-1 pl-8 pr-3 text-[11px] text-slate-200 placeholder-slate-500 focus:border-blue-500 focus:outline-hidden"
            />
          </div>
        </div>

        <div className="flex items-center gap-3">
          {/* Stream Filter Switcher */}
          <div className="flex items-center rounded-lg border border-slate-800 bg-slate-950 p-0.5">
            <Button
              variant="ghost"
              size="xs"
              onClick={() => setStreamFilter("all")}
              aria-pressed={streamFilter === "all"}
              className={cn(streamFilter === "all" && "bg-muted text-foreground")}
            >
              All
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => setStreamFilter("stdout")}
              aria-pressed={streamFilter === "stdout"}
              className={cn(
                streamFilter === "stdout" && "bg-cyan-950 text-cyan-300 border border-cyan-800",
              )}
            >
              stdout
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => setStreamFilter("stderr")}
              aria-pressed={streamFilter === "stderr"}
              className={cn(
                streamFilter === "stderr" && "bg-rose-950 text-rose-300 border border-rose-800",
              )}
            >
              stderr
            </Button>
          </div>

          <span className="text-[10px] text-slate-500">
            {filteredLogs.length} / {displayedLogs.length} lines
          </span>
        </div>
      </div>

      {/* Terminal Viewport */}
      <div
        ref={terminalRef}
        onScroll={handleScroll}
        className="relative flex-1 overflow-y-auto p-4 text-[11px] leading-relaxed text-slate-300 selection:bg-blue-600/40"
      >
        {isLoading ? (
          <div className="flex h-32 items-center justify-center text-slate-500">
            <Radio className="h-4 w-4 animate-spin text-blue-500 mr-2" />
            <span>Loading log stream...</span>
          </div>
        ) : filteredLogs.length === 0 ? (
          <div className="flex h-32 flex-col items-center justify-center text-slate-500 text-center">
            {search || streamFilter !== "all" ? (
              <>
                <p className="font-semibold text-slate-400">No matching log lines</p>
                <p className="text-[10px] text-slate-600 mt-1">
                  Try resetting search or stream filters
                </p>
              </>
            ) : mode === "live" ? (
              <>
                <p className="font-semibold text-slate-400">Waiting for runner output...</p>
                <p className="text-[10px] text-slate-600 mt-1">
                  Container output will stream here in real-time
                </p>
              </>
            ) : (
              <p className="font-semibold text-slate-400">
                No log output recorded for this runner execution
              </p>
            )}
          </div>
        ) : (
          <div className="space-y-0.5">
            {filteredLogs.map((chunk, idx) => {
              const isErr = chunk.stream === "stderr";
              return (
                <div
                  key={idx}
                  className={`flex items-start gap-2 rounded px-1 py-0.5 hover:bg-slate-900/60 transition-colors ${
                    isErr ? "bg-rose-950/20 text-rose-200" : ""
                  }`}
                >
                  <span className="w-10 shrink-0 select-none text-right font-mono text-[10px] text-slate-600">
                    {idx + 1}
                  </span>
                  {chunk.timestamp && (
                    <span className="shrink-0 select-none font-mono text-[10px] text-slate-500">
                      {chunk.timestamp.length > 19
                        ? chunk.timestamp.substring(11, 19)
                        : chunk.timestamp}
                    </span>
                  )}
                  <span
                    className={`shrink-0 select-none font-mono text-[10px] font-semibold ${
                      isErr ? "text-rose-400" : "text-cyan-400"
                    }`}
                  >
                    [{chunk.stream || "stdout"}]
                  </span>
                  <span className="flex-1 whitespace-pre-wrap break-all font-mono">
                    {chunk.content}
                  </span>
                </div>
              );
            })}
          </div>
        )}

        {/* Floating scroll to bottom button when user scrolled up */}
        {!autoScroll && filteredLogs.length > 10 && (
          <Button
            size="xs"
            onClick={scrollToBottom}
            className="absolute bottom-4 right-6 rounded-full shadow-lg"
          >
            <ArrowDown data-icon="inline-start" />
            <span>Resume Auto-scroll</span>
          </Button>
        )}
      </div>
    </div>
  );
}
