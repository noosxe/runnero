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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { DataTable, useAppTable } from "../../lib/tables";
import {
  useCreateUser,
  useDeleteUser,
  useSession,
  useSetUserPassword,
  useSetUserRole,
  useUsers,
} from "../../lib/api/query-hooks";
import type { UserInfo } from "../../gen/api_pb";
import type { AppTableFeatures } from "../../lib/tables/use-app-table";
import { KeyRound, Plus, ShieldCheck, Trash2, UserRound } from "lucide-react";
import { toast } from "@/components/ui/toast";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { ConnectError } from "@connectrpc/connect";

const columnHelper = createColumnHelper<AppTableFeatures, UserInfo>();

/** Formats a protobuf Timestamp for the user table cells. */
function fmtTs(ts?: Timestamp | null): string {
  if (!ts) return "—";
  return timestampDate(ts).toLocaleString();
}

/** Extracts a human-readable message from a Connect error. */
function errMessage(err: unknown): string {
  if (err instanceof ConnectError) return err.message;
  return String(err);
}

/**
 * User management (RUN-236, docs/35 §2.4): the admin-only account list
 * with create, role change, password reset, and delete. Guard rails are
 * server-side (last admin, no self-delete); the UI mirrors them as
 * disabled states and warnings but never trusts them as enforcement.
 * Rendered by the Settings page only for admins.
 */
export function UsersCard() {
  const { data: session } = useSession();
  const currentUsername = session?.username ?? "";

  const { data: users, isLoading } = useUsers();
  const createUser = useCreateUser();
  const setUserRole = useSetUserRole();
  const setUserPassword = useSetUserPassword();
  const deleteUser = useDeleteUser();

  // Add-user dialog state.
  const [addOpen, setAddOpen] = useState(false);
  const [addUsername, setAddUsername] = useState("");
  const [addPassword, setAddPassword] = useState("");
  const [addPasswordConfirm, setAddPasswordConfirm] = useState("");
  const [addRole, setAddRole] = useState<"admin" | "viewer">("viewer");
  const [addError, setAddError] = useState("");

  // Role-change dialog state (confirm before the flip).
  const [roleTarget, setRoleTarget] = useState<UserInfo | null>(null);
  const [roleNext, setRoleNext] = useState<"admin" | "viewer">("viewer");

  // Password-reset dialog state.
  const [resetTarget, setResetTarget] = useState<UserInfo | null>(null);
  const [resetPassword, setResetPassword] = useState("");
  const [resetConfirm, setResetConfirm] = useState("");
  const [resetError, setResetError] = useState("");

  // Delete confirm state.
  const [deleteTarget, setDeleteTarget] = useState<UserInfo | null>(null);

  const columns = useMemo(
    () =>
      columnHelper.columns([
        columnHelper.accessor("username", {
          header: "Username",
          cell: (info) => (
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">{info.getValue()}</span>
              {info.getValue() === currentUsername && (
                <Badge variant="outline" className="text-[10px]">
                  you
                </Badge>
              )}
            </div>
          ),
        }),
        columnHelper.accessor("role", {
          header: "Role",
          cell: (info) => {
            const role = info.getValue();
            const isAdmin = role === "admin";
            return (
              <Badge variant={isAdmin ? "default" : "secondary"} className="gap-1">
                {isAdmin ? (
                  <ShieldCheck data-icon="inline-start" />
                ) : (
                  <UserRound data-icon="inline-start" />
                )}
                {isAdmin ? "Admin" : "Viewer"}
              </Badge>
            );
          },
        }),
        columnHelper.accessor("createdAt", {
          header: "Created",
          cell: (info) => (
            <span className="text-xs text-muted-foreground">{fmtTs(info.getValue())}</span>
          ),
        }),
        columnHelper.display({
          id: "actions",
          header: "",
          cell: (info) => {
            const user = info.row.original;
            const isSelf = user.username === currentUsername;
            return (
              <div className="flex items-center justify-end gap-1">
                <Button
                  variant="ghost"
                  size="xs"
                  aria-label={`Change role for ${user.username}`}
                  onClick={() => {
                    setRoleNext(user.role === "admin" ? "viewer" : "admin");
                    setRoleTarget(user);
                  }}
                >
                  <ShieldCheck data-icon="inline-start" />
                  <span>Role</span>
                </Button>
                <Button
                  variant="ghost"
                  size="xs"
                  aria-label={`Reset password for ${user.username}`}
                  onClick={() => {
                    setResetPassword("");
                    setResetConfirm("");
                    setResetError("");
                    setResetTarget(user);
                  }}
                >
                  <KeyRound data-icon="inline-start" />
                  <span>Reset password</span>
                </Button>
                <Button
                  variant="ghost"
                  size="xs"
                  aria-label={`Delete ${user.username}`}
                  disabled={isSelf}
                  title={isSelf ? "You cannot delete your own account" : undefined}
                  onClick={() => setDeleteTarget(user)}
                >
                  <Trash2 data-icon="inline-start" />
                  <span>Delete</span>
                </Button>
              </div>
            );
          },
        }),
      ]),
    [currentUsername],
  );

  const table = useAppTable({
    columns,
    data: users ?? [],
    getRowId: (user) => user.username,
  });

  const handleAddUser = async () => {
    setAddError("");
    if (!addUsername.trim()) {
      setAddError("Username must not be empty.");
      return;
    }
    if (addPassword.length < 12) {
      setAddError("Password must be at least 12 characters.");
      return;
    }
    if (addPassword !== addPasswordConfirm) {
      setAddError("Passwords do not match.");
      return;
    }
    try {
      await createUser.mutateAsync({
        username: addUsername.trim(),
        password: addPassword,
        role: addRole,
      });
      toast.add({ title: "User created", description: addUsername.trim() });
      setAddOpen(false);
      setAddUsername("");
      setAddPassword("");
      setAddPasswordConfirm("");
      setAddRole("viewer");
    } catch (err) {
      setAddError(errMessage(err));
    }
  };

  const handleRoleChange = async () => {
    if (!roleTarget) return;
    const demotingSelf = roleTarget.username === currentUsername && roleNext === "viewer";
    if (
      demotingSelf &&
      !window.confirm(
        "Demote your own account to viewer? Your session downgrades on the next request.",
      )
    ) {
      setRoleTarget(null);
      return;
    }
    try {
      await setUserRole.mutateAsync({ username: roleTarget.username, role: roleNext });
      toast.add({
        title: "Role updated",
        description: `${roleTarget.username} is now ${roleNext === "admin" ? "an admin" : "a viewer"}`,
      });
      setRoleTarget(null);
    } catch (err) {
      // Last-admin refusals surface here (server is the source of truth).
      toast.add({ title: "Cannot change role", description: errMessage(err) });
      setRoleTarget(null);
    }
  };

  const handleResetPassword = async () => {
    if (!resetTarget) return;
    setResetError("");
    if (resetPassword.length < 12) {
      setResetError("Password must be at least 12 characters.");
      return;
    }
    if (resetPassword !== resetConfirm) {
      setResetError("Passwords do not match.");
      return;
    }
    try {
      const res = await setUserPassword.mutateAsync({
        username: resetTarget.username,
        password: resetPassword,
      });
      toast.add({
        title: "Password reset",
        description: `${resetTarget.username}'s other sessions were revoked (${res.revokedSessions}).`,
      });
      setResetTarget(null);
    } catch (err) {
      setResetError(errMessage(err));
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await deleteUser.mutateAsync({ username: deleteTarget.username });
      toast.add({ title: "User deleted", description: deleteTarget.username });
      setDeleteTarget(null);
    } catch (err) {
      toast.add({ title: "Cannot delete user", description: errMessage(err) });
      setDeleteTarget(null);
    }
  };

  return (
    <Card data-testid="users-card">
      <CardHeader className="border-b border-border/60">
        <CardTitle className="text-base font-bold">Users</CardTitle>
        <CardDescription className="text-xs">
          Admins manage the supervisor; viewers have read-only access to dashboards, history, and
          runner logs. The last admin can never be demoted or deleted.
        </CardDescription>
        <Button
          size="sm"
          data-testid="add-user-button"
          onClick={() => {
            setAddUsername("");
            setAddPassword("");
            setAddPasswordConfirm("");
            setAddError("");
            setAddRole("viewer");
            setAddOpen(true);
          }}
        >
          <Plus data-icon="inline-start" />
          <span>Add user</span>
        </Button>
      </CardHeader>
      <CardContent className="pt-4">
        {isLoading && <Skeleton className="h-24 w-full" />}
        {!isLoading && <DataTable table={table} />}
      </CardContent>

      {/* Add user */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add user</DialogTitle>
            <DialogDescription>
              Create an account with an initial password. The user can change it after signing in.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-3 py-2">
            <div className="grid gap-1.5">
              <Label htmlFor="new-username">Username</Label>
              <Input
                id="new-username"
                maxLength={64}
                value={addUsername}
                onChange={(e) => setAddUsername(e.target.value)}
                placeholder="observer"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="new-password">Initial password</Label>
              <Input
                id="new-password"
                type="password"
                value={addPassword}
                onChange={(e) => setAddPassword(e.target.value)}
                placeholder="At least 12 characters"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="new-password-confirm">Confirm password</Label>
              <Input
                id="new-password-confirm"
                type="password"
                value={addPasswordConfirm}
                onChange={(e) => setAddPasswordConfirm(e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="new-role">Role</Label>
              <Select value={addRole} onValueChange={(v) => setAddRole(v as "admin" | "viewer")}>
                <SelectTrigger id="new-role" className="w-40">
                  <SelectValue placeholder="Role" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="viewer">Viewer</SelectItem>
                  <SelectItem value="admin">Admin</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {addError && (
              <p data-testid="add-user-error" className="text-xs text-destructive">
                {addError}
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAddOpen(false)}>
              Cancel
            </Button>
            <Button data-testid="add-user-submit" onClick={handleAddUser}>
              Create user
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Role change confirm */}
      <AlertDialog open={roleTarget !== null} onOpenChange={(open) => !open && setRoleTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Change role</AlertDialogTitle>
            <AlertDialogDescription>
              {roleTarget && (
                <>
                  Make <strong>{roleTarget.username}</strong>{" "}
                  {roleNext === "admin" ? "an admin" : "a viewer"}?{" "}
                  {roleTarget.username === currentUsername
                    ? "Your own session downgrades on your next request."
                    : "The change applies to their session on the next request."}
                </>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction data-testid="confirm-role-change" onClick={handleRoleChange}>
              Change role
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Password reset */}
      <Dialog open={resetTarget !== null} onOpenChange={(open) => !open && setResetTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Reset password</DialogTitle>
            <DialogDescription>
              {resetTarget && (
                <>
                  Set a new password for <strong>{resetTarget.username}</strong>. All of their
                  sessions are revoked; their passkeys keep working. Set a new password for{" "}
                  <strong>{resetTarget.username}</strong>. All of their sessions are revoked; their
                  passkeys keep working.
                </>
              )}
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-3 py-2">
            <div className="grid gap-1.5">
              <Label htmlFor="reset-password">New password</Label>
              <Input
                id="reset-password"
                type="password"
                value={resetPassword}
                onChange={(e) => setResetPassword(e.target.value)}
                placeholder="At least 12 characters"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="reset-password-confirm">Confirm password</Label>
              <Input
                id="reset-password-confirm"
                type="password"
                value={resetConfirm}
                onChange={(e) => setResetConfirm(e.target.value)}
              />
            </div>
            {resetError && (
              <p data-testid="reset-password-error" className="text-xs text-destructive">
                {resetError}
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setResetTarget(null)}>
              Cancel
            </Button>
            <Button data-testid="reset-password-submit" onClick={handleResetPassword}>
              Reset password
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirm */}
      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete user</AlertDialogTitle>
            <AlertDialogDescription>
              {deleteTarget && (
                <>
                  Delete <strong>{deleteTarget.username}</strong>? Their sessions and passkeys are
                  removed with the account. This cannot be undone.
                </>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction data-testid="confirm-delete-user" onClick={handleDelete}>
              Delete user
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}
