import { useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Card } from "@/components/ui/card";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { usePageTitle } from "../hooks/use-page-title";
import { create } from "@bufbuild/protobuf";
import { useStore } from "@tanstack/react-form";
import { LoginRequestSchema } from "../gen/api_pb";
import { useAppForm, validateMessage, groupByField, applyFieldErrors } from "../lib/forms";
import { useLogin, useOnboardingStatus, usePasskeyLogin } from "../lib/api/query-hooks";
import { useTheme, type Theme } from "../hooks/use-theme";
import { ShieldCheck, AlertCircle, Fingerprint, Sun, Moon, Monitor } from "lucide-react";

interface LoginFormValues {
  username: string;
  password: string;
}

const LOGIN_FIELDS = ["username", "password"] as const;

export function LoginPage() {
  usePageTitle("Sign In");
  const [error, setError] = useState<string | null>(null);
  const [passkeyError, setPasskeyError] = useState<string | null>(null);

  const { theme, setTheme } = useTheme();
  const search = useSearch({ strict: false }) as { redirect?: string };
  const navigate = useNavigate();
  const loginMutation = useLogin();
  const passkeyMutation = usePasskeyLogin();
  // The login guard preloads onboarding status (router.tsx beforeLoad), so
  // this hits the query cache - no extra RPC on the login screen. The
  // passkey entry point renders only when WebAuthn is configured
  // (docs/34 section 3.5); a button that always fails has no place here.
  const { data: onboarding } = useOnboardingStatus();
  const passkeyAvailable = onboarding?.passkeyAvailable ?? false;

  const target = search?.redirect && search.redirect.startsWith("/") ? search.redirect : "/";

  const handlePasskey = async () => {
    setPasskeyError(null);
    setError(null);
    try {
      await passkeyMutation.mutateAsync();
      navigate({ to: target });
    } catch (err: unknown) {
      // The ceremony store answer for "no credential this browser knows" is
      // the library's bad-request unwrap; surface a humane line for it.
      const message = err instanceof Error ? err.message : "Passkey sign-in failed";
      setPasskeyError(
        /no credential|not found|unknown credential/i.test(message)
          ? "No passkey on this device is registered with this supervisor."
          : message,
      );
    }
  };

  const form = useAppForm({
    defaultValues: {
      username: "admin",
      password: "",
    } as LoginFormValues,
  });
  const formValues = useStore(form.store, (s) => s.values);
  useStore(form.store, (s) => s.fieldMeta);

  /**
   * The shared violation map (docs/30 §5.4): one protovalidate evaluation on
   * the LoginRequest the submit will send feeds both inline field errors and
   * the submit gate. Empty-gating IS the wire rule (min_len on both fields).
   */
  const runEvaluation = (): Partial<Record<keyof LoginFormValues, string[]>> => {
    const fieldErrors: Partial<Record<keyof LoginFormValues, string[]>> = {};
    const violations = validateMessage(
      LoginRequestSchema,
      create(LoginRequestSchema, {
        username: formValues.username,
        password: formValues.password,
      }),
    );
    const { byField } = groupByField(violations);
    for (const [protoField, messages] of byField) {
      const key = protoField as keyof LoginFormValues;
      if (key === "username" || key === "password") {
        (fieldErrors[key] ??= []).push(...messages);
      }
    }
    applyFieldErrors(form, fieldErrors, LOGIN_FIELDS);
    return fieldErrors;
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    const fieldErrors = runEvaluation();
    if (Object.keys(fieldErrors).length > 0) {
      return;
    }
    try {
      await loginMutation.mutateAsync({
        username: formValues.username,
        password: formValues.password,
      });
      navigate({ to: target });
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Invalid credentials");
    }
  };

  return (
    <main className="relative flex min-h-screen items-center justify-center p-4 bg-muted/50 text-foreground transition-colors ">
      {/* Top Corner Theme Switcher */}
      <div className="absolute top-4 right-4 flex items-center rounded-xl border border-border bg-card p-1 shadow-xs bg-muted">
        <ToggleGroup
          size="sm"
          value={[theme]}
          onValueChange={(value) => {
            if (value[0]) setTheme(value[0] as Theme);
          }}
        >
          <ToggleGroupItem value="light" aria-label="Light">
            <Sun />
          </ToggleGroupItem>
          <ToggleGroupItem value="dark" aria-label="Dark">
            <Moon />
          </ToggleGroupItem>
          <ToggleGroupItem value="system" aria-label="System">
            <Monitor />
          </ToggleGroupItem>
        </ToggleGroup>
      </div>

      <Card className="w-full max-w-sm px-(--card-spacing)">
        <div className="mx-auto mb-4 flex size-12 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-sm">
          <ShieldCheck className="size-6" />
        </div>

        <h1 className="text-center text-xl font-bold tracking-tight text-foreground">
          Sign In to Supervisor
        </h1>
        <p className="mt-1 text-center text-xs text-muted-foreground">
          Enter administrative credentials to access control interface
        </p>

        {error && (
          <div
            role="alert"
            className="mt-4 flex items-center gap-2 rounded-xl bg-destructive/10 p-3 text-xs text-destructive"
          >
            <AlertCircle className="size-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        {passkeyAvailable && (
          <>
            <div
              className="mt-6 flex items-center gap-3 text-xs text-muted-foreground"
              role="separator"
            >
              <span className="h-px flex-1 bg-border" />
              or
              <span className="h-px flex-1 bg-border" />
            </div>
            {passkeyError && (
              <div
                role="alert"
                className="mt-4 flex items-center gap-2 rounded-xl bg-destructive/10 p-3 text-xs text-destructive"
              >
                <AlertCircle className="size-4 shrink-0" />
                <span>{passkeyError}</span>
              </div>
            )}
            <Button
              type="button"
              variant="outline"
              className="mt-4 w-full"
              disabled={passkeyMutation.isPending}
              onClick={() => void handlePasskey()}
              data-testid="passkey-login-button"
            >
              <Fingerprint className="size-4" />
              {passkeyMutation.isPending ? "Waiting for passkey..." : "Sign in with passkey"}
            </Button>
          </>
        )}

        <form onSubmit={handleSubmit} noValidate className="mt-6 flex flex-col gap-4 text-xs">
          <form.AppField name="username">
            {(field) => (
              <field.TextField
                label="Username"
                id="username"
                autoComplete="username"
                autoFocus
                onBlurExtra={runEvaluation}
              />
            )}
          </form.AppField>

          <form.AppField name="password">
            {(field) => (
              <field.PasswordField
                label="Password"
                id="password"
                autoComplete="current-password"
                onBlurExtra={runEvaluation}
              />
            )}
          </form.AppField>

          <Button
            type="submit"
            onMouseDown={(e) => e.preventDefault()}
            disabled={loginMutation.isPending}
            className="w-full"
          >
            {loginMutation.isPending ? "Signing in..." : "Sign In"}
          </Button>
        </form>
      </Card>
    </main>
  );
}
