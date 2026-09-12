import { useState, type FormEvent } from "react";
import { FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Card } from "@/components/ui/card";
import { cn } from "cn";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useLogin } from "../lib/api/query-hooks";
import { useTheme } from "../hooks/use-theme";
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
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="Light Theme"
                onClick={() => setTheme("light")}
                aria-pressed={theme === "light"}
                className={cn(theme === "light" && "bg-muted text-primary")}
              />
            }
          >
            <Sun />
          </TooltipTrigger>
          <TooltipContent>Light Theme</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="Dark Theme"
                onClick={() => setTheme("dark")}
                aria-pressed={theme === "dark"}
                className={cn(theme === "dark" && "bg-muted text-primary")}
              />
            }
          >
            <Moon />
          </TooltipTrigger>
          <TooltipContent>Dark Theme</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="System Theme"
                onClick={() => setTheme("system")}
                aria-pressed={theme === "system"}
                className={cn(theme === "system" && "bg-muted text-primary")}
              />
            }
          >
            <Monitor />
          </TooltipTrigger>
          <TooltipContent>System Theme</TooltipContent>
        </Tooltip>
      </div>

      <Card className="w-full max-w-sm px-(--card-spacing)">
        <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-sm">
          <ShieldCheck className="h-6 w-6" />
        </div>

        <h1 className="text-center text-xl font-bold tracking-tight text-foreground">
          Sign In to Supervisor
        </h1>
        <p className="mt-1 text-center text-xs text-muted-foreground">
          Enter administrative credentials to access control interface
        </p>

        {error && (
          <div className="mt-4 flex items-center gap-2 rounded-xl bg-destructive/10 p-3 text-xs text-destructive">
            <AlertCircle className="h-4 w-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        <form onSubmit={handleSubmit} className="mt-6 space-y-4 text-xs">
          <div>
            <FieldLabel htmlFor="username">Username</FieldLabel>
            <Input
              id="username"
              type="text"
              value={username}
              onChange={(e) => setUsername(e.target.value)}

              required
              autoFocus
            />
          </div>

          <div>
            <FieldLabel htmlFor="password">Password</FieldLabel>
            <div className="relative mt-1">
              <Input
                id="password"
                type={showPassword ? "text" : "password"}
                value={password}
                onChange={(e) => setPassword(e.target.value)}

                required
              />
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="Toggle password visibility"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute inset-y-0 right-0"
                tabIndex={-1}
              >
                {showPassword ? <EyeOff /> : <Eye />}
              </Button>
            </div>
          </div>

          <Button type="submit" disabled={loginMutation.isPending} className="w-full">
            {loginMutation.isPending ? "Signing in..." : "Sign In"}
          </Button>
        </form>
      </Card>
    </div>
  );
}
