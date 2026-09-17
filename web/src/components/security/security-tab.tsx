import { useMemo, useState } from "react";
import { createColumnHelper } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { DataTable, useAppTable } from "../../lib/tables";
import {
  useOnboardingStatus,
  useRevokeOtherSessions,
  useRevokeSession,
  useSessions,
} from "../../lib/api/query-hooks";
import type { SessionInfo } from "../../gen/api_pb";
import type { AppTableFeatures } from "../../lib/tables/use-app-table";
import { MonitorSmartphone, ShieldOff } from "lucide-react";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { ChangePasswordCard } from "./change-password-card";
import { PasskeysCard } from "./passkeys-card";

const columnHelper = createColumnHelper<AppTableFeatures, SessionInfo>();

/** Formats a protobuf Timestamp for the session table cells. */
function fmtTs(ts?: Timestamp | null): string {
  if (!ts) return "—";
  return timestampDate(ts).toLocaleString();
}

/**
 * Security tab (RUN-232, docs/32 §7): the caller's active sessions with
 * device labels, the current-session marker, per-row revoke and a
 * confirm-guarded "revoke all other sessions". Token material never
 * reaches this surface - only row ids and display metadata.
 */
export function SecurityTab() {
  const { data: sessions, isLoading } = useSessions();
  const revokeSession = useRevokeSession();
  const revokeOthers = useRevokeOtherSessions();
  const [confirmOpen, setConfirmOpen] = useState(false);
  // Cached from the authenticated route guard's beforeLoad fetch - no extra
  // RPC. The passkey card renders only when WebAuthn is configured
  // (docs/34 section 3.5).
  const { data: onboarding } = useOnboardingStatus();
  const passkeyAvailable = onboarding?.passkeyAvailable ?? false;

  const columns = useMemo(
    () =>
      columnHelper.columns([
        columnHelper.accessor("deviceLabel", {
          header: "Device",
          cell: (info) => <span className="font-semibold">{info.getValue()}</span>,
        }),
        columnHelper.accessor(
          (row) => (row.createdAt ? timestampDate(row.createdAt).getTime() : 0),
          {
            id: "created",
            header: "Signed In",
            sortFn: "basic",
            cell: ({ row }) => <span>{fmtTs(row.original.createdAt)}</span>,
          },
        ),
        columnHelper.accessor(
          (row) => (row.lastSeenAt ? timestampDate(row.lastSeenAt).getTime() : 0),
          {
            id: "lastSeen",
            header: "Last Seen",
            sortFn: "basic",
            cell: ({ row }) => <span>{fmtTs(row.original.lastSeenAt)}</span>,
          },
        ),
        columnHelper.accessor(
          (row) => (row.expiresAt ? timestampDate(row.expiresAt).getTime() : 0),
          {
            id: "expires",
            header: "Idle Expiry",
            sortFn: "basic",
            cell: ({ row }) => (
              <span>
                {fmtTs(row.original.expiresAt)}
                <span className="block text-xs text-muted-foreground">
                  absolute {fmtTs(row.original.absoluteExpiresAt)}
                </span>
              </span>
            ),
          },
        ),
        columnHelper.display({
          id: "status",
          header: "Status",
          cell: ({ row }) =>
            row.original.isCurrent ? (
              <Badge variant="secondary">Current session</Badge>
            ) : (
              <span className="text-xs text-muted-foreground">—</span>
            ),
        }),
        columnHelper.display({
          id: "actions",
          header: "Actions",
          meta: { headerClassName: "text-right", cellClassName: "text-right" },
          cell: ({ row }) => (
            <Button
              variant="outline"
              size="xs"
              disabled={revokeSession.isPending}
              onClick={() => revokeSession.mutate(row.original.id)}
            >
              <ShieldOff className="size-3.5" />
              Revoke
            </Button>
          ),
        }),
      ]),
    [revokeSession],
  );

  const table = useAppTable({
    columns,
    data: sessions ?? [],
    getRowId: (row) => row.id.toString(),
  });

  const others = (sessions ?? []).filter((s) => !s.isCurrent).length;

  return (
    <div className="flex flex-col gap-6">
      <ChangePasswordCard />
      {passkeyAvailable && <PasskeysCard />}
      <Card>
        <CardHeader className="border-b border-border/60">
          <CardTitle className="text-base font-bold">Active Sessions</CardTitle>
          <CardDescription className="text-xs">
            Every browser or client signed in with your account. Revoking a session signs that
            device out server-side; the current session is marked.
          </CardDescription>
          {others > 0 && (
            <Button
              variant="outline"
              size="sm"
              className="w-fit"
              disabled={revokeOthers.isPending}
              onClick={() => setConfirmOpen(true)}
            >
              <MonitorSmartphone className="size-4" />
              Revoke all other sessions ({others})
            </Button>
          )}
        </CardHeader>
        <CardContent>
          <DataTable
            table={table}
            empty={<span className="text-sm text-muted-foreground">Loading sessions…</span>}
          />
          {isLoading && (
            <p className="mt-2 text-xs text-muted-foreground" aria-busy="true">
              Loading…
            </p>
          )}
        </CardContent>

        <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Revoke all other sessions?</AlertDialogTitle>
              <AlertDialogDescription>
                Every device except this one will be signed out. This cannot be undone.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => {
                  setConfirmOpen(false);
                  revokeOthers.mutate();
                }}
              >
                Revoke others
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </Card>
    </div>
  );
}
