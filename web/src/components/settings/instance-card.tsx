import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useSession } from "../../lib/api/query-hooks";

// InstanceCard shows the running supervisor build and host details
// (RUN-251, settings page spec): product version (ldflags-stamped, RUN-249/250)
// plus the host facts GetSession already reports. Post-auth only — the
// session endpoint requires a session, so the version never becomes a
// pre-auth fingerprint.
export function InstanceCard() {
  const { data: session } = useSession();

  return (
    <Card data-testid="instance-card">
      <CardHeader>
        <CardTitle>Instance</CardTitle>
        <CardDescription>
          Running build and host details for this supervisor deployment.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {session ? (
          <dl className="grid grid-cols-[7rem_1fr] items-baseline gap-x-6 gap-y-3 text-sm">
            <dt className="text-muted-foreground">Version</dt>
            <dd className="font-mono text-foreground" data-testid="instance-version">
              {session.version || "unknown"}
            </dd>
            <dt className="text-muted-foreground">Host OS</dt>
            <dd className="font-mono text-foreground" data-testid="instance-host-os">
              {session.hostOs || "unknown"}
            </dd>
            <dt className="text-muted-foreground">Host architecture</dt>
            <dd className="font-mono text-foreground" data-testid="instance-host-arch">
              {session.hostArch || "unknown"}
            </dd>
          </dl>
        ) : (
          <div className="flex flex-col gap-3" data-testid="instance-card-loading">
            <Skeleton className="h-4 w-48" />
            <Skeleton className="h-4 w-32" />
            <Skeleton className="h-4 w-32" />
          </div>
        )}
      </CardContent>
    </Card>
  );
}
