import type { Pool } from "../../gen/api_pb";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

/**
 * Resolve the effective target list of a pool: the `targetUrls` field, falling
 * back to the legacy single-target `repositoryUrl` field for old records.
 */
export function poolTargetList(pool: Pick<Pool, "targetUrls" | "repositoryUrl">): string[] {
  if ((pool.targetUrls?.length ?? 0) > 0) return pool.targetUrls as string[];
  return pool.repositoryUrl ? [pool.repositoryUrl] : [];
}

/**
 * "N repos" count badge for multi-target pools, with the full target list as
 * the native tooltip. Renders nothing for single-target pools so a classic
 * one-repo pool keeps its original single-URL presentation.
 */
export function TargetCountBadge({
  pool,
  className = "",
}: {
  pool: Pick<Pool, "targetUrls" | "repositoryUrl">;
  className?: string;
}) {
  const targets = poolTargetList(pool);
  if (targets.length <= 1) return null;
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            className={`inline-flex shrink-0 items-center rounded-md border border-primary/30 bg-primary/10 px-1.5 py-0.5 text-[10px] font-semibold text-primary ${className}`}
          />
        }
      >
        {targets.length} repos
      </TooltipTrigger>
      <TooltipContent className="whitespace-pre-line">{targets.join("\n")}</TooltipContent>
    </Tooltip>
  );
}
