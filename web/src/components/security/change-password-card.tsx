import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { ChangePasswordRequestSchema } from "../../gen/api_pb";
import { validateMessage, groupByField, violationsFromConnectError } from "../../lib/forms";
import { useChangePassword } from "../../lib/api/query-hooks";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { KeyRound } from "lucide-react";

type PasswordFormValues = {
  currentPassword: string;
  newPassword: string;
  confirmPassword: string;
};

const FIELDS = ["currentPassword", "newPassword", "confirmPassword"] as const;

/** Proto wire field names → form keys (groupByField returns snake_case). */
const PROTO_FIELD_TO_FORM: Record<string, (typeof FIELDS)[number]> = {
  current_password: "currentPassword",
  new_password: "newPassword",
};

/**
 * Change-password card for the signed-in admin (RUN-237, docs/32 §4.4).
 * Server is the validation authority: empty-gating and the 12-character
 * class-C floor come from the ChangePasswordRequest protovalidate schema,
 * and a wrong current password returns a field-attached violation that is
 * mapped back inline. Success reports how many other sessions the server
 * revoked — the current session always survives.
 */
export function ChangePasswordCard() {
  const changePassword = useChangePassword();
  const [values, setValues] = useState<PasswordFormValues>({
    currentPassword: "",
    newPassword: "",
    confirmPassword: "",
  });
  const [fieldErrors, setFieldErrors] = useState<
    Partial<Record<(typeof FIELDS)[number], string[]>>
  >({});
  const [banner, setBanner] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  const setValue = (key: (typeof FIELDS)[number], value: string) => {
    setValues((v) => ({ ...v, [key]: value }));
  };

  const runEvaluation = (): Partial<Record<(typeof FIELDS)[number], string[]>> => {
    const errors: Partial<Record<(typeof FIELDS)[number], string[]>> = {};

    // Class A: protovalidate on the exact wire schema (empty-gating + the
    // 12-character floor are declarative in proto/api.proto).
    const message = create(ChangePasswordRequestSchema, {
      currentPassword: values.currentPassword,
      newPassword: values.newPassword,
    });
    const { byField, messageLevel } = groupByField(
      validateMessage(ChangePasswordRequestSchema, message),
    );
    for (const [protoField, messages] of byField) {
      const key = PROTO_FIELD_TO_FORM[protoField];
      if (key) {
        (errors[key] ??= []).push(...messages);
      }
    }
    for (const violation of messageLevel) {
      (errors.newPassword ??= []).push(violation.message);
    }

    // Class C: confirm-match check.
    if (values.newPassword !== values.confirmPassword) {
      (errors.confirmPassword ??= []).push("Passwords do not match");
    }

    return errors;
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBanner(null);
    setSuccess(null);

    const errors = runEvaluation();
    if (Object.keys(errors).length > 0) {
      setFieldErrors(errors);
      return;
    }
    setFieldErrors({});

    try {
      const res = await changePassword.mutateAsync({
        currentPassword: values.currentPassword,
        newPassword: values.newPassword,
      });
      const revoked = res.revokedSessions;
      const others = Number(revoked);
      setSuccess(
        others > 0
          ? `Password changed. ${others} other ${others === 1 ? "session was" : "sessions were"} signed out.`
          : "Password changed.",
      );
      setValues({ currentPassword: "", newPassword: "", confirmPassword: "" });
    } catch (err: unknown) {
      const violations = violationsFromConnectError(err);
      if (violations) {
        const { byField, messageLevel } = groupByField(violations);
        const serverErrors: Partial<Record<(typeof FIELDS)[number], string[]>> = {};
        const bannerParts: string[] = messageLevel.map((v) => v.message);
        for (const [protoField, messages] of byField) {
          const key = PROTO_FIELD_TO_FORM[protoField];
          if (key) {
            (serverErrors[key] ??= []).push(...messages);
          } else {
            bannerParts.push(...messages);
          }
        }
        setFieldErrors(serverErrors);
        setBanner(
          bannerParts.length > 0
            ? bannerParts.join(" ")
            : "Password change failed. Check the highlighted fields.",
        );
        return;
      }
      setBanner("Password change failed. Try again.");
    }
  };

  const errorFor = (key: (typeof FIELDS)[number]): string | undefined => fieldErrors[key]?.[0];

  return (
    <Card>
      <CardHeader className="border-b border-border/60">
        <CardTitle className="text-base font-bold">Change Password</CardTitle>
        <CardDescription className="text-xs">
          Update your own password. At least 12 characters. Signing in on other devices requires the
          new password — those sessions are signed out.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="flex max-w-sm flex-col gap-3" noValidate>
          <label className="text-sm font-medium" htmlFor="current-password">
            Current password
          </label>
          <Input
            id="current-password"
            type="password"
            autoComplete="current-password"
            value={values.currentPassword}
            onChange={(e) => setValue("currentPassword", e.target.value)}
            aria-invalid={errorFor("currentPassword") ? true : undefined}
          />
          {errorFor("currentPassword") && (
            <p className="text-xs text-destructive" role="alert">
              {errorFor("currentPassword")}
            </p>
          )}

          <label className="text-sm font-medium" htmlFor="new-password">
            New password
          </label>
          <Input
            id="new-password"
            type="password"
            autoComplete="new-password"
            value={values.newPassword}
            onChange={(e) => setValue("newPassword", e.target.value)}
            aria-invalid={errorFor("newPassword") ? true : undefined}
          />
          {errorFor("newPassword") && (
            <p className="text-xs text-destructive" role="alert">
              {errorFor("newPassword")}
            </p>
          )}

          <label className="text-sm font-medium" htmlFor="confirm-password">
            Confirm new password
          </label>
          <Input
            id="confirm-password"
            type="password"
            autoComplete="new-password"
            value={values.confirmPassword}
            onChange={(e) => setValue("confirmPassword", e.target.value)}
            aria-invalid={errorFor("confirmPassword") ? true : undefined}
          />
          {errorFor("confirmPassword") && (
            <p className="text-xs text-destructive" role="alert">
              {errorFor("confirmPassword")}
            </p>
          )}

          {banner && (
            <p className="text-xs text-destructive" role="alert">
              {banner}
            </p>
          )}
          {success && (
            <p className="text-xs text-emerald-600" role="status">
              {success}
            </p>
          )}

          <Button type="submit" size="sm" className="w-fit" disabled={changePassword.isPending}>
            <KeyRound className="size-4" />
            {changePassword.isPending ? "Changing…" : "Change password"}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
