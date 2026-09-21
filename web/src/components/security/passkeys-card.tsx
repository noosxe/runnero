import { useMemo, useState } from "react";
import { createColumnHelper } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
  useDeletePasskey,
  useEnrollPasskey,
  usePasskeys,
  useRenamePasskey,
} from "../../lib/api/query-hooks";
import type { PasskeyInfo } from "../../gen/api_pb";
import type { AppTableFeatures } from "../../lib/tables/use-app-table";
import { AlertTriangle, Fingerprint, Pencil, Trash2 } from "lucide-react";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

const columnHelper = createColumnHelper<AppTableFeatures, PasskeyInfo>();

import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
/** Formats a protobuf Timestamp for the passkey table cells. */
function fmtTs(ts?: Timestamp | null): string {
  if (!ts) return "—";
  return timestampDate(ts).toLocaleString();
}

/**
 * Passkey management (RUN-248, docs/34 §4.2): the caller's registered
 * passkeys with the password-gated enrollment dialog, rename, delete, and
 * the clone-warning banner (a flagged credential refuses further
 * assertions until removed). Rendered only when WebAuthn is configured -
 * the parent gates on GetOnboardingStatus.passkey_available (RUN-282);
 * see PasskeysUnconfiguredCard for the unconfigured state.
 */
export function PasskeysCard() {
  const { data: passkeys, isLoading } = usePasskeys();
  const enroll = useEnrollPasskey();
  const rename = useRenamePasskey();
  const deletePasskey = useDeletePasskey();

  const [addOpen, setAddOpen] = useState(false);
  const [addPassword, setAddPassword] = useState("");
  const [addName, setAddName] = useState("");
  const [addError, setAddError] = useState<string | null>(null);
  const [renameTarget, setRenameTarget] = useState<PasskeyInfo | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [renameError, setRenameError] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<PasskeyInfo | null>(null);

  const openAdd = () => {
    setAddPassword("");
    setAddName("");
    setAddError(null);
    setAddOpen(true);
  };

  const submitAdd = async () => {
    if (!addPassword) {
      setAddError("Confirm your current password to add a passkey.");
      return;
    }
    setAddError(null);
    try {
      // Runs the server password re-check (docs/34 §4.2), then the browser
      // creation prompt, then Finish. The prompt can take a while; the
      // button stays pending throughout the ceremony.
      await enroll.mutateAsync({ currentPassword: addPassword, name: addName });
      setAddOpen(false);
    } catch (err: unknown) {
      setAddError(err instanceof Error ? err.message : "Failed to add passkey");
    }
  };

  const openRename = (pk: PasskeyInfo) => {
    setRenameTarget(pk);
    setRenameValue(pk.name);
    setRenameError(null);
  };

  const submitRename = async () => {
    if (!renameTarget) return;
    setRenameError(null);
    try {
      await rename.mutateAsync({ id: renameTarget.id, name: renameValue });
      setRenameTarget(null);
    } catch (err: unknown) {
      setRenameError(err instanceof Error ? err.message : "Failed to rename passkey");
    }
  };

  const columns = useMemo(
    () =>
      columnHelper.columns([
        columnHelper.accessor("name", {
          header: "Name",
          cell: (info) => (
            <span className="inline-flex items-center gap-2 font-semibold">
              <Fingerprint className="size-4 text-muted-foreground" />
              {info.getValue()}
            </span>
          ),
        }),
        columnHelper.accessor(
          (row) => (row.createdAt ? timestampDate(row.createdAt).getTime() : 0),
          {
            id: "created",
            header: "Added",
            sortFn: "basic",
            cell: ({ row }) => <span>{fmtTs(row.original.createdAt)}</span>,
          },
        ),
        columnHelper.accessor(
          (row) => (row.lastUsedAt ? timestampDate(row.lastUsedAt).getTime() : 0),
          {
            id: "lastUsed",
            header: "Last Used",
            sortFn: "basic",
            cell: ({ row }) => <span>{fmtTs(row.original.lastUsedAt)}</span>,
          },
        ),
        columnHelper.display({
          id: "signals",
          header: "Signals",
          cell: ({ row }) => (
            <span className="flex flex-wrap gap-1">
              <Badge variant="outline">
                {row.original.backupEligible ? "Synced" : "Device-bound"}
              </Badge>
              {row.original.cloneWarning && (
                <Badge variant="destructive" data-testid="clone-warning-badge">
                  <AlertTriangle className="size-3" />
                  Cloned?
                </Badge>
              )}
            </span>
          ),
        }),
        columnHelper.display({
          id: "actions",
          header: "Actions",
          meta: { headerClassName: "text-right", cellClassName: "text-right" },
          cell: ({ row }) => (
            <span className="inline-flex justify-end gap-1">
              <Button variant="outline" size="xs" onClick={() => openRename(row.original)}>
                <Pencil className="size-3.5" />
                Rename
              </Button>
              <Button
                variant="outline"
                size="xs"
                disabled={deletePasskey.isPending}
                onClick={() => setDeleteTarget(row.original)}
              >
                <Trash2 className="size-3.5" />
                Remove
              </Button>
            </span>
          ),
        }),
      ]),
    [deletePasskey.isPending],
  );

  const table = useAppTable({
    columns,
    data: passkeys ?? [],
    getRowId: (row) => row.id.toString(),
  });

  const cloneWarning = (passkeys ?? []).some((pk) => pk.cloneWarning);

  return (
    <Card>
      <CardHeader className="border-b border-border/60">
        <CardTitle className="text-base font-bold">Passkeys</CardTitle>
        <CardDescription className="text-xs">
          Passwordless sign-in credentials for your account (docs/34). A passkey can sign you in
          from any browser where its authenticator is available; your password keeps working as an
          independent fallback.
        </CardDescription>
        <Button
          variant="outline"
          size="sm"
          className="w-fit"
          onClick={openAdd}
          data-testid="add-passkey-button"
        >
          <Fingerprint className="size-4" />
          Add passkey
        </Button>
      </CardHeader>
      <CardContent>
        {cloneWarning && (
          <div
            className="mb-3 flex items-center gap-2 rounded-xl bg-destructive/10 p-3 text-xs text-destructive"
            data-testid="clone-warning-banner"
          >
            <AlertTriangle className="size-4 shrink-0" />
            <span>
              A passkey on this account appears cloned (its signature counter regressed). It can no
              longer sign in; remove it and enroll a new one.
            </span>
          </div>
        )}
        <DataTable
          table={table}
          empty={
            isLoading ? (
              <span className="text-sm text-muted-foreground">Loading passkeys…</span>
            ) : (
              <span className="text-sm text-muted-foreground">
                No passkeys yet. Add one to sign in without your password.
              </span>
            )
          }
        />
        {isLoading && (
          <p className="mt-2 text-xs text-muted-foreground" aria-busy="true">
            Loading…
          </p>
        )}
      </CardContent>

      {/* Enrollment: password re-check + optional label (docs/34 §4.2). */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add a passkey</DialogTitle>
            <DialogDescription>
              Confirm your current password, then follow your browser&apos;s prompt to create the
              passkey.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4 text-xs">
            {addError && (
              <div
                className="flex items-center gap-2 rounded-xl bg-destructive/10 p-3 text-xs text-destructive"
                data-testid="add-passkey-error"
              >
                <AlertTriangle className="size-4 shrink-0" />
                <span>{addError}</span>
              </div>
            )}
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="passkey-current-password">Current password</Label>
              <Input
                id="passkey-current-password"
                type="password"
                autoComplete="current-password"
                value={addPassword}
                onChange={(e) => setAddPassword(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="passkey-name">Name (optional)</Label>
              <Input
                id="passkey-name"
                placeholder="e.g. YubiKey 5C"
                maxLength={64}
                value={addName}
                onChange={(e) => setAddName(e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAddOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => void submitAdd()}
              disabled={enroll.isPending}
              data-testid="add-passkey-submit"
            >
              {enroll.isPending ? "Waiting for passkey..." : "Create passkey"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Rename. */}
      <Dialog open={renameTarget !== null} onOpenChange={(open) => !open && setRenameTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rename passkey</DialogTitle>
            <DialogDescription>Give this passkey a recognizable name.</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4 text-xs">
            {renameError && (
              <div className="flex items-center gap-2 rounded-xl bg-destructive/10 p-3 text-xs text-destructive">
                <AlertTriangle className="size-4 shrink-0" />
                <span>{renameError}</span>
              </div>
            )}
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="rename-passkey-name">Name</Label>
              <Input
                id="rename-passkey-name"
                maxLength={64}
                value={renameValue}
                onChange={(e) => setRenameValue(e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRenameTarget(null)}>
              Cancel
            </Button>
            <Button onClick={() => void submitRename()} disabled={rename.isPending}>
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Deletion guard. */}
      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove passkey “{deleteTarget?.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              It will no longer be able to sign in to this supervisor. Devices that already keep it
              registered will simply fail the ceremony. Your password fallback is unaffected.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              data-testid="confirm-remove-passkey"
              onClick={() => {
                if (deleteTarget) deletePasskey.mutate(deleteTarget.id);
                setDeleteTarget(null);
              }}
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}

/**
 * Unconfigured WebAuthn state (RUN-282, docs/37 §4.3): a card-shaped
 * Empty state explaining that passkey support needs supervisor
 * configuration (SUPERVISOR_WEBAUTHN_RP_ID / SUPERVISOR_WEBAUTHN_ORIGINS,
 * docs/34). Rendered instead of PasskeysCard so the capability is
 * discoverable on unconfigured stacks instead of silently hidden; the
 * caller decides role visibility (admins only).
 */
export function PasskeysUnconfiguredCard() {
  return (
    <Card>
      <CardHeader className="border-b border-border/60">
        <CardTitle className="text-base font-bold">Passkeys</CardTitle>
        <CardDescription className="text-xs">
          Passwordless sign-in credentials for your account (docs/34).
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Fingerprint />
            </EmptyMedia>
            <EmptyTitle>Passkey support is not configured</EmptyTitle>
            <EmptyDescription>
              Set <code>SUPERVISOR_WEBAUTHN_RP_ID</code> on the supervisor (and optionally
              <code>SUPERVISOR_WEBAUTHN_ORIGINS</code>) to enable passkey sign-in and enrollment.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </CardContent>
    </Card>
  );
}
