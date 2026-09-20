import { useState, useRef, useEffect, useMemo, useCallback } from "react";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
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

/**
 * Live-region policy (docs/36 §5.4, decided): the viewport is `role="log"`
 * with `aria-live="off"` — permanently. Uncontrolled polite announcement of
 * streaming stdout is noise that makes the page unusable for screen-reader
 * users; nobody wants live dictation of logs. Keyboard/SR users pause the
 * stream (Pause, aria-pressed) and read statically, and filter/search
 * changes are visible text on the page. Do not "helpfully" enable polite
 * streaming here — see docs/36-accessibility.md §5.4 before touching it.
 */
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
    <div className="flex h-full flex-col overflow-hidden rounded-2xl border border-terminal-border bg-terminal font-mono text-terminal-fg shadow-2xl text-xs">
      {/* Terminal Top Bar */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-terminal-border bg-terminal-surface/90 px-4 py-3">
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <Terminal className="size-4 text-terminal-accent" />
            <span className="font-bold text-terminal-title">
              {title || runnerName || "Terminal Console"}
            </span>
          </div>

          {containerId && (
            <span className="rounded bg-terminal-raised px-1.5 py-0.5 text-[10px] text-terminal-muted">
              {containerId.substring(0, 12)}
            </span>
          )}

          {/* Status Indicator */}
          {mode === "live" ? (
            <Badge
              className={cn(
                "border",
                isPaused
                  ? "border-terminal-err-soft/30 bg-terminal-err-soft/10 text-terminal-err-soft"
                  : isConnected
                    ? "border-terminal-out/40 bg-terminal-out/10 text-terminal-out"
                    : isConnecting
                      ? "border-terminal-accent/40 bg-terminal-accent/10 text-terminal-accent"
                      : "border-terminal-err/40 bg-terminal-err/10 text-terminal-err",
              )}
            >
              <span
                className={cn(
                  "size-1.5 rounded-full",
                  isPaused
                    ? "bg-terminal-err-soft"
                    : isConnected
                      ? "bg-terminal-out animate-pulse"
                      : "bg-terminal-accent animate-ping",
                )}
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
              <Clock className="size-3 text-terminal-muted" />
              <span>Historical Archive</span>
            </Badge>
          )}
        </div>

        {/* Action Controls */}
        <div className={cn("flex items-center gap-1.5", headerRightInset && "mr-9")}>
          {mode === "live" && (
            <Button
              variant="outline"
              size="xs"
              onClick={togglePause}
              aria-pressed={isPaused}
              className={cn(
                isPaused &&
                  "border-terminal-err-soft/60 bg-terminal-err-soft/10 text-terminal-err-soft hover:bg-terminal-err-soft/20",
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
            className={cn(
              autoScroll && "border-terminal-accent/40 bg-terminal-accent/10 text-terminal-accent",
            )}
          >
            <ArrowDown
              data-icon="inline-start"
              className={autoScroll ? "text-terminal-accent" : "text-terminal-muted"}
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
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-terminal-border/80 bg-terminal-surface/40 px-4 py-2">
        <div className="flex items-center gap-2 flex-1 max-w-sm">
          <InputGroup>
            <InputGroupAddon align="inline-start">
              <Search className="size-3.5 text-terminal-muted" />
            </InputGroupAddon>
            <InputGroupInput
              type="text"
              value={search}
              aria-label="Filter log output"
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Filter log output..."
              className="font-mono text-[11px]"
            />
          </InputGroup>
        </div>

        <div className="flex items-center gap-3">
          {/* Stream Filter Switcher */}
          <div className="flex items-center rounded-lg border border-terminal-border bg-terminal p-0.5">
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
                streamFilter === "stdout" &&
                  "bg-terminal-out/10 text-terminal-out border border-terminal-out/40",
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
                streamFilter === "stderr" &&
                  "bg-terminal-err/10 text-terminal-err border border-terminal-err/40",
              )}
            >
              stderr
            </Button>
          </div>

          <span className="text-[10px] text-terminal-muted">
            {filteredLogs.length} / {displayedLogs.length} lines
          </span>
        </div>
      </div>

      {/* Terminal Viewport */}
      <div
        ref={terminalRef}
        role="log"
        aria-live="off" /* permanent — policy + rationale in the header above (§5.4) */
        aria-busy={isLoading || undefined}
        onScroll={handleScroll}
        className="relative flex-1 overflow-y-auto p-4 text-[11px] leading-relaxed text-terminal-fg selection:bg-terminal-accent/40"
      >
        {isLoading ? (
          <div className="flex h-32 items-center justify-center text-terminal-muted">
            <Radio className="size-4 animate-spin text-terminal-accent mr-2" />
            <span>Loading log stream...</span>
          </div>
        ) : filteredLogs.length === 0 ? (
          <div className="flex h-32 flex-col items-center justify-center text-terminal-muted text-center">
            {search || streamFilter !== "all" ? (
              <>
                <p className="font-semibold text-terminal-muted">No matching log lines</p>
                <p className="text-[10px] text-terminal-faint mt-1">
                  Try resetting search or stream filters
                </p>
              </>
            ) : mode === "live" ? (
              <>
                <p className="font-semibold text-terminal-muted">Waiting for runner output...</p>
                <p className="text-[10px] text-terminal-faint mt-1">
                  Container output will stream here in real-time
                </p>
              </>
            ) : (
              <p className="font-semibold text-terminal-muted">
                No log output recorded for this runner execution
              </p>
            )}
          </div>
        ) : (
          <div className="flex flex-col gap-0.5">
            {filteredLogs.map((chunk, idx) => {
              const isErr = chunk.stream === "stderr";
              return (
                <div
                  key={idx}
                  className={cn(
                    "flex items-start gap-2 rounded px-1 py-0.5 hover:bg-terminal-surface/60 transition-colors",
                    isErr && "bg-terminal-err/10 text-terminal-err-soft",
                  )}
                >
                  <span className="w-10 shrink-0 select-none text-right font-mono text-[10px] text-terminal-faint">
                    {idx + 1}
                  </span>
                  {chunk.timestamp && (
                    <span className="shrink-0 select-none font-mono text-[10px] text-terminal-muted">
                      {chunk.timestamp.length > 19
                        ? chunk.timestamp.substring(11, 19)
                        : chunk.timestamp}
                    </span>
                  )}
                  <span
                    className={cn(
                      "shrink-0 select-none font-mono text-[10px] font-semibold",
                      isErr ? "text-terminal-err" : "text-terminal-out",
                    )}
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
