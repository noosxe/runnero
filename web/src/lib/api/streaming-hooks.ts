import { useState, useEffect, useRef, useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { analyticsClient, poolClient, logClient } from "./transport";
import { queryKeys } from "./query-hooks";
import type {
  WatchDashboardResponse,
  WatchPoolsResponse,
  WatchRunnersResponse,
  LogChunk,
  Pool,
} from "../../gen/api_pb";

export type StreamStatus = "idle" | "connecting" | "connected" | "reconnecting" | "error";

export interface StreamOptions {
  enabled?: boolean;
  intervalMs?: number;
  maxBackoffMs?: number;
  initialBackoffMs?: number;
  onError?: (err: unknown) => void;
}

/**
 * useStreamSubscription manages a persistent ConnectRPC server-streaming call.
 * Handles automatic reconnect with exponential backoff + jitter and abort cleanup.
 */
export function useStreamSubscription<T>(
  streamFn: (signal: AbortSignal) => AsyncIterable<T>,
  onData: (data: T) => void,
  options: StreamOptions = {},
) {
  const { enabled = true, initialBackoffMs = 1000, maxBackoffMs = 30000, onError } = options;

  const [status, setStatus] = useState<StreamStatus>("idle");
  const [error, setError] = useState<Error | null>(null);

  const onDataRef = useRef(onData);
  const onErrorRef = useRef(onError);
  const streamFnRef = useRef(streamFn);

  useEffect(() => {
    onDataRef.current = onData;
    onErrorRef.current = onError;
    streamFnRef.current = streamFn;
  });

  useEffect(() => {
    if (!enabled) {
      return;
    }

    let active = true;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let backoff = initialBackoffMs;
    let abortController: AbortController | null = null;

    async function runLoop() {
      while (active) {
        abortController = new AbortController();
        setStatus((prev) => (prev === "connected" ? "reconnecting" : "connecting"));

        try {
          const stream = streamFnRef.current(abortController.signal);
          for await (const message of stream) {
            if (!active) break;
            backoff = initialBackoffMs;
            setStatus("connected");
            setError(null);
            onDataRef.current(message);
          }
        } catch (err: unknown) {
          if (!active || abortController.signal.aborted) {
            break;
          }
          const streamError = err instanceof Error ? err : new Error(String(err));
          setError(streamError);
          onErrorRef.current?.(streamError);
          setStatus("reconnecting");
        }

        if (!active) break;

        const jitter = Math.random() * 200;
        const delay = Math.min(backoff + jitter, maxBackoffMs);
        backoff = Math.min(backoff * 1.5, maxBackoffMs);

        await new Promise<void>((resolve) => {
          retryTimer = setTimeout(resolve, delay);
        });
      }
    }

    void runLoop();

    return () => {
      active = false;
      if (abortController) {
        abortController.abort();
      }
      if (retryTimer) {
        clearTimeout(retryTimer);
      }
    };
  }, [enabled, initialBackoffMs, maxBackoffMs]);

  return {
    status: enabled ? status : "idle",
    isConnected: enabled && status === "connected",
    isConnecting: enabled && status === "connecting",
    isReconnecting: enabled && status === "reconnecting",
    error,
  };
}

/**
 * useWatchDashboard streams near-realtime system stats, pool states, and recent jobs.
 * Pushes updates directly into the TanStack Query cache to avoid polling storms.
 */
export function useWatchDashboard(options?: StreamOptions) {
  const queryClient = useQueryClient();
  const intervalMs = options?.intervalMs ?? 1000;

  const streamFn = useCallback(
    (signal: AbortSignal) => {
      return analyticsClient.watchDashboard({ intervalMs }, { signal });
    },
    [intervalMs],
  );

  const onData = useCallback(
    (res: WatchDashboardResponse) => {
      if (res.stats) {
        queryClient.setQueryData(queryKeys.systemStats, res.stats);
        queryClient.setQueriesData({ queryKey: queryKeys.systemStats }, res.stats);
      }
      if (res.pools) {
        queryClient.setQueryData(queryKeys.pools, res.pools);
        for (const pool of res.pools) {
          if (pool.id) {
            queryClient.setQueryData(queryKeys.pool(pool.id), pool);
          }
        }
      }
      if (res.recentJobs && res.recentJobs.length > 0) {
        queryClient.setQueryData(
          queryKeys.jobHistory({ poolId: 0n, limit: 5, offset: 0, search: "", status: "" }),
          {
            jobs: res.recentJobs.slice(0, 5),
            totalCount: res.recentJobs.length,
          },
        );
      }
    },
    [queryClient],
  );

  return useStreamSubscription<WatchDashboardResponse>(streamFn, onData, options);
}

/**
 * useWatchPools streams near-realtime runner pool states and concurrency counts.
 */
export function useWatchPools(options?: StreamOptions) {
  const queryClient = useQueryClient();
  const intervalMs = options?.intervalMs ?? 1000;

  const streamFn = useCallback(
    (signal: AbortSignal) => {
      return poolClient.watchPools({ intervalMs }, { signal });
    },
    [intervalMs],
  );

  const onData = useCallback(
    (res: WatchPoolsResponse) => {
      if (res.pools) {
        queryClient.setQueryData(queryKeys.pools, res.pools);
        for (const pool of res.pools) {
          if (pool.id) {
            queryClient.setQueryData(queryKeys.pool(pool.id), pool);
          }
        }
      }
    },
    [queryClient],
  );

  return useStreamSubscription<WatchPoolsResponse>(streamFn, onData, options);
}

/**
 * useStreamRunnerLogs opens a live follow tail on an active runner container.
 */
export function useStreamRunnerLogs(runnerId: string, options?: StreamOptions) {
  const queryClient = useQueryClient();
  const [logs, setLogs] = useState<LogChunk[]>([]);

  const streamFn = useCallback(
    (signal: AbortSignal) => {
      return logClient.streamRunnerLogs({ runnerId }, { signal });
    },
    [runnerId],
  );

  const onData = useCallback(
    (chunk: LogChunk) => {
      setLogs((prev) => [...prev, chunk]);
      queryClient.setQueryData<LogChunk[]>(queryKeys.runnerLogs(runnerId), (prev) => [
        ...(prev ?? []),
        chunk,
      ]);
    },
    [queryClient, runnerId],
  );

  const sub = useStreamSubscription<LogChunk>(streamFn, onData, {
    ...options,
    enabled: (options?.enabled ?? true) && Boolean(runnerId),
  });

  const clearLogs = useCallback(() => {
    setLogs([]);
    queryClient.setQueryData<LogChunk[]>(queryKeys.runnerLogs(runnerId), []);
  }, [queryClient, runnerId]);

  return {
    ...sub,
    logs,
    clearLogs,
  };
}

/**
 * useWatchRunners streams near-realtime runner container instances for a specific pool.
 */
export function useWatchRunners(poolId: bigint, options?: StreamOptions) {
  const queryClient = useQueryClient();
  const intervalMs = options?.intervalMs ?? 1000;

  const streamFn = useCallback(
    (signal: AbortSignal) => {
      return poolClient.watchRunners({ poolId, intervalMs }, { signal });
    },
    [poolId, intervalMs],
  );

  const onData = useCallback(
    (res: WatchRunnersResponse) => {
      if (res.runners) {
        queryClient.setQueryData(queryKeys.runners(poolId), res.runners);
      }
      queryClient.setQueryData<Pool[]>(queryKeys.pools, (old) => {
        if (!old) return old;
        return old.map((p) => {
          if (p.id === poolId) {
            return {
              ...p,
              activeRunners: res.activeRunners,
              idleRunners: res.idleRunners,
              healthStatus: res.healthStatus,
              currentIntent: res.currentIntent,
              lastError: res.lastError,
              lastErrorCode: res.lastErrorCode,
              lastErrorTimestamp: res.lastErrorTimestamp,
              lastReconciledAt: res.lastReconciledAt,
            };
          }
          return p;
        });
      });
    },
    [queryClient, poolId],
  );

  return useStreamSubscription<WatchRunnersResponse>(streamFn, onData, {
    ...options,
    enabled: (options?.enabled ?? true) && poolId > 0n,
  });
}
