import { RefreshCw } from "lucide-react";
import { Card, CardContent, CardTitle } from "@/components/ui/card";
import type { Pool } from "../../gen/api_pb";

export interface PoolPollStatusProps {
  pool: Pool;
  className?: string;
}

function formatTimestamp(isoString?: string): string {
  if (!isoString) return "";
  try {
    const d = new Date(isoString);
    if (isNaN(d.getTime())) return "";
    return d.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  } catch {
    return "";
  }
}

// PoolPollStatus renders the demand-polling observation for pools that poll
// for queued jobs (docs/24 §5.9): natively polling providers (Forgejo) and
// GitHub pools with poll_fallback enabled. It answers "why isn't it scaling?"
// on hosts without inbound webhooks: the last poll time, the queued-jobs
// count observed, and any poll-skip or failure note. Rendered only when the
// pool participates in demand polling.
export function PoolPollStatus({ pool, className = "" }: PoolPollStatusProps) {
  const pollsDemand = pool.pollFallback || pool.provider.toLowerCase() === "forgejo";
  if (!pollsDemand) return null;

  const lastPoll = formatTimestamp(pool.lastPollAt);
  const hasNote = Boolean(pool.lastPollError);

  return (
    <Card size="sm" data-testid="pool-poll-status" className={className}>
      <CardContent>
        <div className="flex items-start gap-3">
          <div className="mt-0.5 rounded-xl bg-muted/50 p-2 text-muted-foreground">
            <RefreshCw className="h-4 w-4" />
          </div>
          <div className="flex-1 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <CardTitle className="text-sm font-semibold">Demand Polling</CardTitle>
              <span className="text-xs text-muted-foreground">
                {lastPoll ? `Last poll ${lastPoll}` : "Not polled yet"}
              </span>
            </div>
            <p className="text-xs text-muted-foreground">
              {pool.lastPollQueuedCount === 1
                ? "1 queued job observed at last poll"
                : `${pool.lastPollQueuedCount} queued jobs observed at last poll`}
            </p>
            {hasNote && <p className="text-xs text-warning">{pool.lastPollError}</p>}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
