import { useState, type FormEvent } from "react";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group";
import { Button } from "@/components/ui/button";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Card } from "@/components/ui/card";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useLogin } from "../lib/api/query-hooks";
import { useTheme, type Theme } from "../hooks/use-theme";
import { ShieldCheck, AlertCircle, Eye, EyeOff, Sun, Moon, Monitor } from "lucide-react";

export function LoginPage() {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const { theme, setTheme } = useTheme();
  const search = useSearch({ strict: false }) as { redirect?: string };
  const navigate = useNavigate();
  const loginMutation = useLogin();

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      await loginMutation.mutateAsync({ username, password });
      const target = search?.redirect && search.redirect.startsWith("/") ? search.redirect : "/";
      navigate({ to: target });
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Invalid credentials");
    }
  };

  return (
    <div className="relative flex min-h-screen items-center justify-center p-4 bg-muted/50 text-foreground transition-colors ">
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
          <div className="mt-4 flex items-center gap-2 rounded-xl bg-destructive/10 p-3 text-xs text-destructive">
            <AlertCircle className="size-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        <form onSubmit={handleSubmit} className="mt-6 flex flex-col gap-4 text-xs">
          <FieldGroup className="gap-4">
            <Field>
              <FieldLabel htmlFor="username">Username</FieldLabel>
              <Input
                id="username"
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}

                required
                autoFocus
              />
            </Field>

            <Field>
              <FieldLabel htmlFor="password">Password</FieldLabel>
              <InputGroup>
                <InputGroupInput
                  id="password"
                  type={showPassword ? "text" : "password"}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}

                  required
                />
                <InputGroupAddon align="inline-end">
                  <InputGroupButton
                    variant="ghost"
                    size="icon-xs"
                    aria-label="Toggle password visibility"
                    onClick={() => setShowPassword(!showPassword)}
                    tabIndex={-1}
                  >
                    {showPassword ? <EyeOff /> : <Eye />}
                  </InputGroupButton>
                </InputGroupAddon>
              </InputGroup>
            </Field>
          </FieldGroup>

          <Button type="submit" disabled={loginMutation.isPending} className="w-full">
            {loginMutation.isPending ? "Signing in..." : "Sign In"}
          </Button>
        </form>
      </Card>
    </div>
  );
}
