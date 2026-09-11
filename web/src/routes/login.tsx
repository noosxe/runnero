import { useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
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
    <div className="relative flex min-h-screen items-center justify-center p-4 bg-slate-50 text-slate-900 transition-colors dark:bg-slate-950 dark:text-slate-50">
      {/* Top Corner Theme Switcher */}
      <div className="absolute top-4 right-4 flex items-center rounded-xl border border-slate-200 bg-white p-1 shadow-xs dark:border-slate-800 dark:bg-slate-900">
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setTheme("light")}
          title="Light Theme"
          aria-pressed={theme === "light"}
          className={cn(theme === "light" && "bg-muted text-primary")}
        >
          <Sun />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setTheme("dark")}
          title="Dark Theme"
          aria-pressed={theme === "dark"}
          className={cn(theme === "dark" && "bg-muted text-primary")}
        >
          <Moon />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setTheme("system")}
          title="System Theme"
          aria-pressed={theme === "system"}
          className={cn(theme === "system" && "bg-muted text-primary")}
        >
          <Monitor />
        </Button>
      </div>

      <div className="w-full max-w-sm rounded-2xl border border-slate-200 bg-white p-8 shadow-xs dark:border-slate-800 dark:bg-slate-900">
        <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-blue-600 text-white shadow-sm">
          <ShieldCheck className="h-6 w-6" />
        </div>

        <h1 className="text-center text-xl font-bold tracking-tight text-slate-900 dark:text-white">
          Sign In to Supervisor
        </h1>
        <p className="mt-1 text-center text-xs text-slate-500 dark:text-slate-400">
          Enter administrative credentials to access control interface
        </p>

        {error && (
          <div className="mt-4 flex items-center gap-2 rounded-xl bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-400">
            <AlertCircle className="h-4 w-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        <form onSubmit={handleSubmit} className="mt-6 space-y-4 text-xs">
          <div>
            <label htmlFor="username" className="font-semibold text-slate-700 dark:text-slate-300">
              Username
            </label>
            <input
              id="username"
              type="text"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              className="mt-1 w-full rounded-xl border border-slate-300 bg-white px-3 py-2 text-slate-900 focus:border-blue-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
              required
              autoFocus
            />
          </div>

          <div>
            <label htmlFor="password" className="font-semibold text-slate-700 dark:text-slate-300">
              Password
            </label>
            <div className="relative mt-1">
              <input
                id="password"
                type={showPassword ? "text" : "password"}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full rounded-xl border border-slate-300 bg-white px-3 py-2 pr-10 text-slate-900 focus:border-blue-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
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
      </div>
    </div>
  );
}
