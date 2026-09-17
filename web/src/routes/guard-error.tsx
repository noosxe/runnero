import { useRouter } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import type { ErrorComponentProps } from "@tanstack/react-router";
import { CloudOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { queryKeys } from "../lib/api/query-hooks";

/**
 * GuardErrorPage (RUN-243) — fail-closed landing for route-guard RPC failures.
 *
 * Rendered by the router when a beforeLoad guard throws because
 * GetOnboardingStatus could not be fetched. This is deliberately NOT the
 * onboarding wizard: a failed status RPC must never look like a fresh
 * install, or a live server one network blip away from its dashboard would
 * invite the operator to re-run SetupAdmin against an existing database.
 *
 * Retry clears the cached guard queries and re-runs the guards.
 */
export function GuardErrorPage({ error }: ErrorComponentProps) {
  const router = useRouter();
  const queryClient = useQueryClient();

  const retry = async () => {
    queryClient.removeQueries({ queryKey: queryKeys.onboardingStatus });
    await router.invalidate();
  };

  const detail =
    error instanceof Error && error.message ? error.message : "Unknown connection error";

  return (
    <div className="flex min-h-svh items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader>
          <div className="mb-2 flex size-10 items-center justify-center rounded-full bg-destructive/10">
            <CloudOff className="size-5 text-destructive" aria-hidden />
          </div>
          <CardTitle>Can&rsquo;t reach the supervisor</CardTitle>
          <CardDescription>
            The connection check failed, so the app can&rsquo;t tell whether it is set up yet.
            Nothing was changed &mdash; retry, and if this keeps happening check that the supervisor
            is running and reachable.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <p
            data-testid="guard-error-detail"
            className="break-words font-mono text-xs text-muted-foreground"
          >
            {detail}
          </p>
        </CardContent>
        <CardFooter>
          <Button onClick={() => void retry()} data-testid="guard-error-retry">
            Retry
          </Button>
        </CardFooter>
      </Card>
    </div>
  );
}
