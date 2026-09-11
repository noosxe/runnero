import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Spinner } from "@/components/ui/spinner";
import type { ImageUpdate } from "../../gen/api_pb";
import { usePullImage, useDismissImageUpdate } from "../../lib/api/query-hooks";
import { ArrowUpCircle, Check, DownloadCloud, X } from "lucide-react";

export interface ImageUpdateNotificationProps {
  updates: ImageUpdate[];
  poolNameLookup?: Record<string, string>;
}

export function ImageUpdateNotification({
  updates,
  poolNameLookup = {},
}: ImageUpdateNotificationProps) {
  const pullMutation = usePullImage();
  const dismissMutation = useDismissImageUpdate();
  const [activePullId, setActivePullId] = useState<bigint | null>(null);
  const [pulledIds, setPulledIds] = useState<Set<bigint>>(new Set());

  if (!updates || updates.length === 0) return null;

  const handlePull = async (poolId: bigint) => {
    setActivePullId(poolId);
    try {
      await pullMutation.mutateAsync(poolId);
      setPulledIds((prev) => new Set([...prev, poolId]));
    } finally {
      setActivePullId(null);
    }
  };

  const handleDismiss = async (updateId: bigint) => {
    await dismissMutation.mutateAsync(updateId);
  };

  return (
    <div className="space-y-3">
      {updates.map((up) => {
        const poolName = poolNameLookup[up.poolId.toString()] ?? `Pool #${up.poolId}`;
        const isPulling = activePullId === up.poolId;
        const isPulled = pulledIds.has(up.poolId);

        return (
          <Alert key={up.id.toString()} className="border-warning/30 bg-warning/5">
            <ArrowUpCircle className="text-warning" />
            <AlertTitle className="flex items-center gap-2">
              <span className="text-xs font-bold uppercase tracking-wider">
                Runner Image Update Available
              </span>
              <span className="rounded-md bg-warning/20 px-2 py-0.5 font-mono text-[10px] font-semibold">
                {poolName}
              </span>
            </AlertTitle>
            <AlertDescription>
              <p className="font-mono text-xs">
                Current: <span className="font-semibold">{up.currentImage}</span> &rarr; Latest:{" "}
                <span className="font-semibold">{up.latestDigest}</span>
              </p>
              <p className="mt-0.5 text-[11px]">
                New containers in this pool will use the updated image without interrupting active
                runners.
              </p>
              <div className="mt-2 flex items-center justify-end gap-2">
                {isPulled ? (
                  <span className="inline-flex items-center gap-1 text-xs font-semibold text-success">
                    <Check className="size-4" />
                    <span>Image Pulled</span>
                  </span>
                ) : (
                  <Button size="xs" onClick={() => handlePull(up.poolId)} disabled={isPulling}>
                    {isPulling ? (
                      <Spinner data-icon="inline-start" />
                    ) : (
                      <DownloadCloud data-icon="inline-start" />
                    )}
                    <span>{isPulling ? "Pulling..." : "Pull Update"}</span>
                  </Button>
                )}

                <Tooltip>
                  <TooltipTrigger
                    render={
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="Dismiss update notification"
                        onClick={() => handleDismiss(up.id)}
                      />
                    }
                  >
                    <X />
                  </TooltipTrigger>
                  <TooltipContent>Dismiss update notification</TooltipContent>
                </Tooltip>
              </div>
            </AlertDescription>
          </Alert>
        );
      })}
    </div>
  );
}
