import { useState, useRef, useEffect, useMemo, useCallback } from "react";
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
            <span
              className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-[10px] font-semibold border ${
                isPaused
                  ? "bg-amber-950/60 text-amber-400 border-amber-800"
                  : isConnected
                    ? "bg-emerald-950/60 text-emerald-400 border-emerald-800"
                    : isConnecting
                      ? "bg-sky-950/60 text-sky-400 border-sky-800"
                      : "bg-rose-950/60 text-rose-400 border-rose-800"
              }`}
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
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 rounded-full border border-slate-700 bg-slate-800 px-2 py-0.5 text-[10px] text-slate-300">
              <Clock className="h-3 w-3 text-slate-400" />
              <span>Historical Archive</span>
            </span>
          )}
        </div>

        {/* Action Controls */}
        <div className={`flex items-center gap-1.5 ${headerRightInset ? "mr-9" : ""}`}>
          {mode === "live" && (
            <button
              type="button"
              onClick={togglePause}
              className={`inline-flex items-center gap-1 rounded-lg border px-2.5 py-1 text-[11px] font-medium transition-colors ${
                isPaused
                  ? "border-amber-700 bg-amber-950/50 text-amber-300 hover:bg-amber-900/50"
                  : "border-slate-700 bg-slate-800 text-slate-300 hover:bg-slate-700"
              }`}
            >
              {isPaused ? <Play className="h-3 w-3" /> : <Pause className="h-3 w-3" />}
              <span>{isPaused ? "Resume" : "Pause"}</span>
            </button>
          )}

          <button
            type="button"
            onClick={() => setAutoScroll((prev) => !prev)}
            className={`inline-flex items-center gap-1 rounded-lg border px-2.5 py-1 text-[11px] font-medium transition-colors ${
              autoScroll
                ? "border-blue-700 bg-blue-950/50 text-blue-300 hover:bg-blue-900/50"
                : "border-slate-700 bg-slate-800 text-slate-400 hover:bg-slate-700"
            }`}
          >
            <ArrowDown className={`h-3 w-3 ${autoScroll ? "text-blue-400" : "text-slate-500"}`} />
            <span>Auto-scroll: {autoScroll ? "ON" : "OFF"}</span>
          </button>

          <button
            type="button"
            onClick={handleCopyAll}
            disabled={displayedLogs.length === 0}
            className="inline-flex items-center gap-1 rounded-lg border border-slate-700 bg-slate-800 px-2.5 py-1 text-[11px] font-medium text-slate-300 hover:bg-slate-700 disabled:opacity-40 transition-colors"
          >
            {copied ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
            <span>{copied ? "Copied" : "Copy"}</span>
          </button>

          <button
            type="button"
            onClick={handleDownload}
            disabled={displayedLogs.length === 0}
            className="inline-flex items-center gap-1 rounded-lg border border-slate-700 bg-slate-800 px-2.5 py-1 text-[11px] font-medium text-slate-300 hover:bg-slate-700 disabled:opacity-40 transition-colors"
          >
            <Download className="h-3 w-3" />
            <span>Export</span>
          </button>

          {onClear && mode === "live" && (
            <button
              type="button"
              onClick={onClear}
              className="inline-flex items-center gap-1 rounded-lg border border-slate-700 bg-slate-800 px-2.5 py-1 text-[11px] font-medium text-slate-400 hover:bg-slate-700 hover:text-rose-400 transition-colors"
            >
              <Trash2 className="h-3 w-3" />
              <span>Clear</span>
            </button>
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
            <button
              type="button"
              onClick={() => setStreamFilter("all")}
              className={`rounded-md px-2 py-0.5 text-[10px] font-medium transition-colors ${
                streamFilter === "all"
                  ? "bg-slate-800 text-white"
                  : "text-slate-400 hover:text-slate-200"
              }`}
            >
              All
            </button>
            <button
              type="button"
              onClick={() => setStreamFilter("stdout")}
              className={`rounded-md px-2 py-0.5 text-[10px] font-medium transition-colors ${
                streamFilter === "stdout"
                  ? "bg-cyan-950 text-cyan-300 border border-cyan-800"
                  : "text-slate-400 hover:text-slate-200"
              }`}
            >
              stdout
            </button>
            <button
              type="button"
              onClick={() => setStreamFilter("stderr")}
              className={`rounded-md px-2 py-0.5 text-[10px] font-medium transition-colors ${
                streamFilter === "stderr"
                  ? "bg-rose-950 text-rose-300 border border-rose-800"
                  : "text-slate-400 hover:text-slate-200"
              }`}
            >
              stderr
            </button>
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
          <button
            type="button"
            onClick={scrollToBottom}
            className="absolute bottom-4 right-6 inline-flex items-center gap-1.5 rounded-full border border-blue-600 bg-blue-950/90 px-3 py-1 text-[11px] font-semibold text-blue-200 shadow-lg hover:bg-blue-900 backdrop-blur-xs transition-colors"
          >
            <ArrowDown className="h-3 w-3" />
            <span>Resume Auto-scroll</span>
          </button>
        )}
      </div>
    </div>
  );
}
