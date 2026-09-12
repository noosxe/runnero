import { useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "cn";
import { useNavigate } from "@tanstack/react-router";
import { create } from "@bufbuild/protobuf";
import { PoolSchema } from "../gen/api_pb";
import { toWireAuthMethod } from "../lib/utils/auth-methods";
import { getSuggestedRunnerLabels } from "../lib/utils/labels";
import {
  useOnboardingStatus,
  useSession,
  useLogin,
  useSetupAdmin,
  useCreateAuthProfile,
  useSetAppSetting,
  useCreatePool,
  useCompleteOnboarding,
} from "../lib/api/query-hooks";
import { useTheme } from "../hooks/use-theme";
import {
  ShieldCheck,
  KeyRound,
  Sliders,
  CheckCircle2,
  AlertCircle,
  Sun,
  Moon,
  Monitor,
  Eye,
  EyeOff,
  ArrowRight,
  ArrowLeft,
  Server,
  Rocket,
  Info,
  FolderGit2,
  ExternalLink,
} from "lucide-react";

export function OnboardingPage() {
  const { data: status } = useOnboardingStatus();
  const { data: session } = useSession();
  const { theme, setTheme } = useTheme();
  const navigate = useNavigate();

  // Active step state (1: Admin, 2: Provider, 3: Safeguards, 4: Pool, 5: Review)
  const defaultStep = status?.adminCreated
    ? !session
      ? 1
      : !status.authProfileExists
        ? 2
        : !status.poolExists
          ? 4
          : 5
    : 1;
  const [stepOverride, setStepOverride] = useState<number | null>(null);
  const currentStep = stepOverride ?? defaultStep;
  const setCurrentStep = setStepOverride;
  const [error, setError] = useState<string | null>(null);

  // Skip states for optional steps
  const [gitProfileSkipped, setGitProfileSkipped] = useState(false);
  const [poolSkipped, setPoolSkipped] = useState(false);

  // Step 1: Admin Credentials State
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);

  // Step 2: Git Provider State
  const [profileName, setProfileName] = useState("github-primary");
  const [authMethod, setAuthMethod] = useState<
    "github_app" | "github_pat" | "gitea_pat" | "forgejo_pat"
  >("github_pat");
  const [appId, setAppId] = useState("");
  const [privateKeyPem, setPrivateKeyPem] = useState("");
  const [token, setToken] = useState("");
  const [showToken, setShowToken] = useState(false);
  const [createdAuthProfileId, setCreatedAuthProfileId] = useState<bigint | null>(null);
  const [githubAppInstallPrompt, setGithubAppInstallPrompt] = useState<{
    installUrl: string;
    profileName: string;
  } | null>(null);

  // Step 3: Global Safeguards State
  const [totalAllowedRunners, setTotalAllowedRunners] = useState(20);
  const [totalIdleWarmPool, setTotalIdleWarmPool] = useState(5);
  const [shutdownTimeoutSeconds, setShutdownTimeoutSeconds] = useState(300);
  const [jobRetentionDays, setJobRetentionDays] = useState(30);

  // Step 4: Initial Pool State
  const [poolName, setPoolName] = useState("default-pool");
  const [repositoryUrl, setRepositoryUrl] = useState("https://github.com/my-org/my-repo");
  const [scope, setScope] = useState<"repo" | "org">("repo");
  const suggestedLabels = getSuggestedRunnerLabels(
    status?.hostOs || session?.hostOs,
    status?.hostArch || session?.hostArch,
  );
  const [customLabels, setCustomLabels] = useState<string | null>(null);
  const labels = customLabels ?? suggestedLabels;

  const [runnerImage, setRunnerImage] = useState("ghcr.io/noosxe/runnero:latest");
  const [minIdleRunners, setMinIdleRunners] = useState(1);
  const [maxConcurrency, setMaxConcurrency] = useState(5);
  const [cpuLimit, setCpuLimit] = useState("2.0");
  const [memoryLimit, setMemoryLimit] = useState("4GB");
  const [allowDocker, setAllowDocker] = useState(true);
  const [renovateEnabled, setRenovateEnabled] = useState(false);
  const [renovateCron, setRenovateCron] = useState("0 2 * * *");
  const [renovateImage, setRenovateImage] = useState("renovate/renovate:latest");

  // Deduced provider
  const deducedProvider =
    authMethod === "github_app" || authMethod === "github_pat"
      ? "github"
      : authMethod === "gitea_pat"
        ? "gitea"
        : "forgejo";

  // Enforce allowDocker for Gitea and Forgejo (docs/05 §4)
  const isDockerLocked = deducedProvider === "gitea" || deducedProvider === "forgejo";
  const effectiveAllowDocker = isDockerLocked ? true : allowDocker;

  // Derived skip and pool availability
  const isGitProfileSkipped =
    gitProfileSkipped || (!createdAuthProfileId && !status?.authProfileExists);
  const isPoolSkipped = poolSkipped || isGitProfileSkipped;
  const hasPoolToLaunch = !isPoolSkipped;

  // Mutations
  const setupAdminMutation = useSetupAdmin();
  const loginMutation = useLogin();
  const createAuthProfileMutation = useCreateAuthProfile();
  const setAppSettingMutation = useSetAppSetting();
  const createPoolMutation = useCreatePool();
  const completeOnboardingMutation = useCompleteOnboarding();

  // Skip to Dashboard shortcut
  const handleSkipToDashboard = async () => {
    setError(null);
    if (status?.adminCreated && !session) {
      setError("Please log in with administrator credentials first to complete setup.");
      setCurrentStep(1);
      return;
    }
    try {
      await completeOnboardingMutation.mutateAsync();
      navigate({ to: "/" });
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to skip onboarding");
    }
  };

  const handleSkipProvider = () => {
    setError(null);
    setGitProfileSkipped(true);
    setCurrentStep(3);
  };

  const handleSkipSafeguards = () => {
    setError(null);
    setCurrentStep(4);
  };

  const handleSkipPool = () => {
    setError(null);
    setPoolSkipped(true);
    setCurrentStep(5);
  };

  // Step 1 Submission: Create Master Admin
  const handleAdminSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    if (password.length < 10) {
      setError("Password must be at least 10 characters long");
      return;
    }
    if (password !== confirmPassword) {
      setError("Passwords do not match");
      return;
    }

    try {
      await setupAdminMutation.mutateAsync({ username, password });
      setCurrentStep(2);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to create administrator");
    }
  };

  // Step 1 Submission: Log in when administrator is already configured
  const handleAdminLogin = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    if (!password) {
      setError("Password is required");
      return;
    }

    try {
      await loginMutation.mutateAsync({ username, password });
      setCurrentStep(!status?.authProfileExists ? 2 : !status?.poolExists ? 4 : 5);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Invalid administrator credentials");
    }
  };

  // Step 2 Submission
  const handleProviderSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    if (!profileName.trim()) {
      setError("Profile name is required");
      return;
    }

    try {
      let res;
      if (authMethod === "github_app") {
        if (!appId || !privateKeyPem.trim()) {
          setError("GitHub App ID and Private Key PEM are required");
          return;
        }
        const encoder = new TextEncoder();
        res = await createAuthProfileMutation.mutateAsync({
          name: profileName.trim(),
          authMethod: toWireAuthMethod("github_app"),
          appId: BigInt(appId.trim()),
          privateKey: encoder.encode(privateKeyPem.trim()),
          token: "",
        });
      } else {
        if (!token.trim()) {
          setError("Personal Access Token is required");
          return;
        }
        res = await createAuthProfileMutation.mutateAsync({
          name: profileName.trim(),
          authMethod: toWireAuthMethod(authMethod),
          appId: 0n,
          privateKey: new Uint8Array(),
          token: token.trim(),
        });
      }
      if (res?.profile?.id) {
        setCreatedAuthProfileId(res.profile.id);
      }
      setGitProfileSkipped(false);
      if (
        authMethod === "github_app" &&
        res?.profile?.installUrl &&
        (res.profile.installationsCount ?? 0) === 0
      ) {
        setGithubAppInstallPrompt({
          installUrl: res.profile.installUrl,
          profileName: res.profile.name || profileName.trim(),
        });
      } else {
        setCurrentStep(3);
      }
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to register Git auth profile");
    }
  };

  // Step 3 Submission
  const handleSafeguardsSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    try {
      await Promise.all([
        setAppSettingMutation.mutateAsync({
          key: "total_allowed_runners",
          value: String(totalAllowedRunners),
        }),
        setAppSettingMutation.mutateAsync({
          key: "total_idle_warm_pool",
          value: String(totalIdleWarmPool),
        }),
        setAppSettingMutation.mutateAsync({
          key: "shutdown_timeout_seconds",
          value: String(shutdownTimeoutSeconds),
        }),
        setAppSettingMutation.mutateAsync({
          key: "job_retention_days",
          value: String(jobRetentionDays),
        }),
      ]);
      setCurrentStep(4);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save global constraints");
    }
  };

  // Step 4 Submission (advances to review)
  const handlePoolSubmit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    if (!poolName.trim()) {
      setError("Pool name is required");
      return;
    }
    if (!repositoryUrl.trim()) {
      setError("Repository URL is required");
      return;
    }
    if (minIdleRunners > maxConcurrency) {
      setError("Min idle runners cannot exceed max concurrency");
      return;
    }

    setPoolSkipped(false);
    setCurrentStep(5);
  };

  // Step 5 Submission (creates pool if configured and completes onboarding)
  const handleLaunchSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    try {
      if (hasPoolToLaunch) {
        const effectiveLabels = labels.trim() || suggestedLabels;
        const parsedLabels = effectiveLabels
          .split(",")
          .map((l) => l.trim())
          .filter(Boolean);

        await createPoolMutation.mutateAsync({
          pool: create(PoolSchema, {
            name: poolName.trim(),
            provider: deducedProvider,
            repositoryUrl: repositoryUrl.trim(),
            minIdleRunners,
            maxConcurrency,
            labels:
              parsedLabels.length > 0
                ? parsedLabels
                : suggestedLabels
                    .split(",")
                    .map((l) => l.trim())
                    .filter(Boolean),
            runnerImage: runnerImage.trim() || "ghcr.io/noosxe/runnero:latest",
            allowDocker: effectiveAllowDocker,
            renovate: renovateEnabled
              ? {
                  enabled: true,
                  cronSchedule: renovateCron.trim() || "0 2 * * *",
                  image: renovateImage.trim() || "renovate/renovate:latest",
                }
              : undefined,
            authProfileId: createdAuthProfileId ?? 1n,
            scope,
            cpuLimit: cpuLimit.trim() || "2.0",
            memoryLimit: memoryLimit.trim() || "4GB",
            maxRunnerLifetimeSeconds: 7200,
            targetUrls: repositoryUrl.trim() ? [repositoryUrl.trim()] : [],
          }),
        });
      }

      await completeOnboardingMutation.mutateAsync();
      navigate({ to: "/" });
    } catch (err: unknown) {
      setError(
        err instanceof Error
          ? err.message
          : hasPoolToLaunch
            ? "Failed to launch runner pool"
            : "Failed to complete onboarding",
      );
    }
  };

  const steps = [
    { num: 1, label: "Admin", icon: ShieldCheck },
    { num: 2, label: "Git Auth", icon: KeyRound },
    { num: 3, label: "Safeguards", icon: Sliders },
    { num: 4, label: "Initial Pool", icon: Server },
    { num: 5, label: "Review", icon: Rocket },
  ];

  return (
    <div className="relative flex min-h-screen flex-col items-center justify-center p-4 bg-slate-50 text-slate-900 transition-colors dark:bg-slate-950 dark:text-slate-50">
      {/* Theme Switcher */}
      <div className="absolute top-4 right-4 flex items-center rounded-xl border border-slate-200 bg-white p-1 shadow-xs dark:border-slate-800 dark:bg-slate-900">
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setTheme("light")}
          aria-pressed={theme === "light"}
          className={cn(theme === "light" && "bg-muted text-primary")}
        >
          <Sun />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setTheme("dark")}
          aria-pressed={theme === "dark"}
          className={cn(theme === "dark" && "bg-muted text-primary")}
        >
          <Moon />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setTheme("system")}
          aria-pressed={theme === "system"}
          className={cn(theme === "system" && "bg-muted text-primary")}
        >
          <Monitor />
        </Button>
      </div>

      {/* Main Wizard Container */}
      <Card className="w-full max-w-2xl gap-0 p-6 sm:p-8">
        {/* Header */}
        <div className="relative text-center">
          {(status?.adminCreated || currentStep > 1) && (
            <Button
              variant="link"
              size="sm"
              onClick={handleSkipToDashboard}
              disabled={completeOnboardingMutation.isPending}
              className="mb-3 sm:absolute sm:right-0 sm:top-0 sm:mb-0"
            >
              Skip to Dashboard
              <ArrowRight data-icon="inline-end" />
            </Button>
          )}
          <div className="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-sm">
            <ShieldCheck className="h-6 w-6" />
          </div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">System Onboarding</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            Configure master administrator, connect Git provider, and set concurrency safeguards
          </p>
        </div>

        {/* Step Progress Bar */}
        <div className="mt-8 flex items-center justify-between border-b border-slate-100 pb-6 dark:border-slate-800">
          {steps.map((step) => {
            const Icon = step.icon;
            const isDone = currentStep > step.num;
            const isCurrent = currentStep === step.num;

            return (
              <div key={step.num} className="flex flex-1 flex-col items-center">
                <div
                  className={`flex h-9 w-9 items-center justify-center rounded-xl text-xs font-semibold transition-all ${
                    isDone
                      ? "bg-emerald-600 text-white"
                      : isCurrent
                        ? "bg-blue-600 text-white shadow-sm"
                        : "bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500"
                  }`}
                >
                  {isDone ? <CheckCircle2 className="h-4 w-4" /> : <Icon className="h-4 w-4" />}
                </div>
                <span
                  className={`mt-1.5 text-[11px] font-medium ${
                    isCurrent
                      ? "font-bold text-blue-600 dark:text-blue-400"
                      : isDone
                        ? "text-slate-700 dark:text-slate-300"
                        : "text-slate-400"
                  }`}
                >
                  {step.label}
                </span>
              </div>
            );
          })}
        </div>

        {/* Error Alert */}
        {error && (
          <div className="mt-6 flex items-center gap-2 rounded-xl bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-400">
            <AlertCircle className="h-4 w-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        {/* Step 1: Admin Setup / Authentication */}
        {currentStep === 1 &&
          (status?.adminCreated ? (
            session ? (
              <div className="mt-6 text-xs">
                <div>
                  <h2 className="text-sm font-bold text-slate-900 dark:text-white">
                    Step 1 of 5: Master Administrator Configured
                  </h2>
                  <p className="mt-0.5 text-slate-500 dark:text-slate-400">
                    Master administrator credentials are configured and authenticated.
                  </p>
                </div>

                <div className="flex items-center gap-3 rounded-xl border border-emerald-200 bg-emerald-50/50 p-4 dark:border-emerald-800/40 dark:bg-emerald-950/20">
                  <CheckCircle2 className="h-5 w-5 shrink-0 text-emerald-600 dark:text-emerald-400" />
                  <div>
                    <div className="font-semibold text-emerald-900 dark:text-emerald-300">
                      Active Administrator Session ({session.username})
                    </div>
                    <div className="text-[11px] text-emerald-700/80 dark:text-emerald-400/80">
                      Session token is active. You may proceed with configuring Git providers and
                      runner pools, or skip to the dashboard.
                    </div>
                  </div>
                </div>

                <div className="pt-2">
                  <Button
                    onClick={() =>
                      setCurrentStep(!status.authProfileExists ? 2 : !status.poolExists ? 4 : 5)
                    }
                    className="w-full"
                  >
                    Next: Git Provider
                    <ArrowRight data-icon="inline-end" />
                  </Button>
                </div>
              </div>
            ) : (
              <form onSubmit={handleAdminLogin} className="mt-6 text-xs">
                <FieldGroup>
                  <div>
                    <h2 className="text-sm font-bold text-slate-900 dark:text-white">
                      Step 1 of 5: Master Administrator Authentication
                    </h2>
                    <p className="mt-0.5 text-slate-500 dark:text-slate-400">
                      Administrator credentials are already configured. Please log in with your
                      master credentials to continue onboarding.
                    </p>
                  </div>

                  <Field>
                    <FieldLabel htmlFor="admin-username">Admin Username</FieldLabel>
                    <Input
                      id="admin-username"
                      type="text"
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                      required
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor="admin-password">Admin Password</FieldLabel>
                    <InputGroup>
                      <InputGroupInput
                        id="admin-password"
                        type={showPassword ? "text" : "password"}
                        value={password}
                        onChange={(e) => setPassword(e.target.value)}
                        required
                        autoFocus
                      />
                      <InputGroupAddon align="inline-end">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label="Toggle password visibility"
                          onClick={() => setShowPassword(!showPassword)}
                          tabIndex={-1}
                        >
                          {showPassword ? <EyeOff /> : <Eye />}
                        </Button>
                      </InputGroupAddon>
                    </InputGroup>
                  </Field>

                  <div className="pt-2">
                    <Button type="submit" disabled={loginMutation.isPending} className="w-full">
                      {loginMutation.isPending ? "Authenticating..." : "Log In to Continue Setup"}
                      <ArrowRight data-icon="inline-end" />
                    </Button>
                  </div>
                </FieldGroup>
              </form>
            )
          ) : (
            <form onSubmit={handleAdminSubmit} className="mt-6 text-xs">
              <FieldGroup>
                <div>
                  <h2 className="text-sm font-bold text-slate-900 dark:text-white">
                    Step 1 of 5: Create Master Administrator
                  </h2>
                  <p className="mt-0.5 text-slate-500 dark:text-slate-400">
                    Administrative credentials are protected with bcrypt password hashing and 24h
                    JWT sessions.
                  </p>
                </div>

                <Field>
                  <FieldLabel htmlFor="admin-username">Admin Username</FieldLabel>
                  <Input
                    id="admin-username"
                    type="text"
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    required
                    autoFocus
                  />
                </Field>

                <Field>
                  <FieldLabel htmlFor="admin-password">Password (min 10 characters)</FieldLabel>
                  <InputGroup>
                    <InputGroupInput
                      id="admin-password"
                      type={showPassword ? "text" : "password"}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      required
                    />
                    <InputGroupAddon align="inline-end">
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="Toggle password visibility"
                        onClick={() => setShowPassword(!showPassword)}
                        tabIndex={-1}
                      >
                        {showPassword ? <EyeOff /> : <Eye />}
                      </Button>
                    </InputGroupAddon>
                  </InputGroup>
                </Field>

                <Field>
                  <FieldLabel htmlFor="admin-confirm-password">Confirm Password</FieldLabel>
                  <Input
                    id="admin-confirm-password"
                    type={showPassword ? "text" : "password"}
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    required
                  />
                </Field>

                <div className="pt-2">
                  <Button type="submit" disabled={setupAdminMutation.isPending} className="w-full">
                    {setupAdminMutation.isPending ? "Creating Admin..." : "Next: Git Provider"}
                    <ArrowRight data-icon="inline-end" />
                  </Button>
                </div>
              </FieldGroup>
            </form>
          ))}

        {/* Step 2: Git Provider Auth Profile */}
        {currentStep === 2 && githubAppInstallPrompt ? (
          <div className="mt-6 text-xs">
            <div>
              <h2 className="text-sm font-bold text-slate-900 dark:text-white">
                Step 2 of 5: Install GitHub App on Your Account
              </h2>
              <p className="mt-0.5 text-slate-500 dark:text-slate-400">
                Profile &ldquo;{githubAppInstallPrompt.profileName}&rdquo; was created successfully.
              </p>
            </div>

            <Card className="gap-0 border-primary/25 bg-primary/5 p-5">
              <div className="flex items-start gap-3">
                <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-primary text-primary-foreground">
                  <FolderGit2 className="h-5 w-5" />
                </div>
                <div>
                  <h3 className="text-sm font-bold text-foreground">
                    Action Required: Install App in GitHub
                  </h3>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Your GitHub App credentials have been saved and encrypted. To allow runnero to
                    access your repositories and register self-hosted runners, install the app on
                    your GitHub user account or organization.
                  </p>
                  <div className="mt-4 flex flex-wrap items-center gap-3">
                    <Button
                      render={
                        <a
                          href={githubAppInstallPrompt.installUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                        />
                      }
                    >
                      <ExternalLink data-icon="inline-start" />
                      Install GitHub App on GitHub
                    </Button>
                  </div>
                </div>
              </div>
            </Card>

            <div className="flex gap-3 pt-2">
              <Button variant="outline" onClick={() => setGithubAppInstallPrompt(null)}>
                <ArrowLeft data-icon="inline-start" />
                Edit Credentials
              </Button>
              <Button
                className="flex-1"
                onClick={() => {
                  setGithubAppInstallPrompt(null);
                  setCurrentStep(3);
                }}
              >
                Continue: Safeguards
                <ArrowRight data-icon="inline-end" />
              </Button>
            </div>
          </div>
        ) : (
          currentStep === 2 && (
            <form onSubmit={handleProviderSubmit} className="mt-6 text-xs">
              <FieldGroup>
                <div>
                  <h2 className="text-sm font-bold text-slate-900 dark:text-white">
                    Step 2 of 5: Connect Git Provider
                  </h2>
                  <p className="mt-0.5 text-slate-500 dark:text-slate-400">
                    Register authentication credentials to fetch runner registration tokens and
                    orchestrate pools.
                  </p>
                </div>

                {/* Provider Type Selection */}
                <Field>
                  <FieldLabel>Provider Method</FieldLabel>
                  <div className="mt-2 grid grid-cols-2 gap-2 sm:grid-cols-4">
                    {[
                      { id: "github_pat", label: "GitHub PAT" },
                      { id: "github_app", label: "GitHub App" },
                      { id: "gitea_pat", label: "Gitea PAT" },
                      { id: "forgejo_pat", label: "Forgejo PAT" },
                    ].map((m) => (
                      <Button
                        key={m.id}
                        type="button"
                        variant="outline"
                        aria-pressed={authMethod === m.id}
                        onClick={() => setAuthMethod(m.id as any)}
                        className={cn(
                          "h-auto w-full py-2.5",
                          authMethod === m.id &&
                            "border-primary bg-primary/5 text-primary font-semibold",
                        )}
                      >
                        {m.label}
                      </Button>
                    ))}
                  </div>
                </Field>

                <Field>
                  <FieldLabel htmlFor="profile-name">Profile Name</FieldLabel>
                  <Input
                    id="profile-name"
                    type="text"
                    value={profileName}
                    onChange={(e) => setProfileName(e.target.value)}
                    required
                  />
                </Field>

                {authMethod === "github_app" ? (
                  <>
                    <Field>
                      <FieldLabel htmlFor="app-id">GitHub App ID</FieldLabel>
                      <Input
                        id="app-id"
                        type="number"
                        value={appId}
                        onChange={(e) => setAppId(e.target.value)}
                        placeholder="e.g. 123456"
                        required
                      />
                    </Field>

                    <Field>
                      <FieldLabel htmlFor="private-key">Private Key PEM</FieldLabel>
                      <Textarea
                        id="private-key"
                        rows={4}
                        value={privateKeyPem}
                        onChange={(e) => setPrivateKeyPem(e.target.value)}
                        placeholder="-----BEGIN RSA PRIVATE KEY-----&#10;...&#10;-----END RSA PRIVATE KEY-----"
                        className="font-mono text-[11px]"
                        required
                      />
                    </Field>
                  </>
                ) : (
                  <Field>
                    <FieldLabel htmlFor="provider-token">Personal Access Token (PAT)</FieldLabel>
                    <InputGroup>
                      <InputGroupInput
                        id="provider-token"
                        type={showToken ? "text" : "password"}
                        value={token}
                        onChange={(e) => setToken(e.target.value)}
                        placeholder="ghp_..."
                        required
                      />
                      <InputGroupAddon align="inline-end">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label="Toggle token visibility"
                          onClick={() => setShowToken(!showToken)}
                          tabIndex={-1}
                        >
                          {showToken ? <EyeOff /> : <Eye />}
                        </Button>
                      </InputGroupAddon>
                    </InputGroup>
                  </Field>
                )}

                <div className="flex gap-3 pt-2">
                  <Button variant="outline" onClick={() => setCurrentStep(1)}>
                    <ArrowLeft data-icon="inline-start" />
                    Back
                  </Button>
                  <Button variant="outline" onClick={handleSkipProvider}>
                    Skip this step
                  </Button>
                  <Button
                    type="submit"
                    disabled={createAuthProfileMutation.isPending}
                    className="flex-1"
                  >
                    {createAuthProfileMutation.isPending ? "Connecting..." : "Next: Safeguards"}
                    <ArrowRight data-icon="inline-end" />
                  </Button>
                </div>
              </FieldGroup>
            </form>
          )
        )}

        {/* Step 3: Global Scaling Safeguards */}
        {currentStep === 3 && (
          <form onSubmit={handleSafeguardsSubmit} className="mt-6 text-xs">
            <FieldGroup>
              <div>
                <h2 className="text-sm font-bold text-slate-900 dark:text-white">
                  Step 3 of 5: Global Scaling Safeguards
                </h2>
                <p className="mt-0.5 text-slate-500 dark:text-slate-400">
                  Configure supervisor-level guardrails to prevent host resource starvation.
                </p>
              </div>

              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <Field>
                  <FieldLabel htmlFor="max-runners">Total Allowed Runners</FieldLabel>
                  <FieldDescription>Maximum concurrent runners across all pools</FieldDescription>
                  <Input
                    id="max-runners"
                    type="number"
                    min={1}
                    max={100}
                    value={totalAllowedRunners}
                    onChange={(e) => setTotalAllowedRunners(Number(e.target.value))}
                    required
                  />
                </Field>

                <Field>
                  <FieldLabel htmlFor="idle-warm-pool">Warm Idle Reserve Ceiling</FieldLabel>
                  <FieldDescription>Max idle standby runners across all pools</FieldDescription>
                  <Input
                    id="idle-warm-pool"
                    type="number"
                    min={0}
                    max={50}
                    value={totalIdleWarmPool}
                    onChange={(e) => setTotalIdleWarmPool(Number(e.target.value))}
                    required
                  />
                </Field>

                <Field>
                  <FieldLabel htmlFor="shutdown-timeout">
                    Graceful Shutdown Timeout (seconds)
                  </FieldLabel>
                  <FieldDescription>Runner drain deadline upon SIGTERM / SIGINT</FieldDescription>
                  <Input
                    id="shutdown-timeout"
                    type="number"
                    min={10}
                    max={3600}
                    value={shutdownTimeoutSeconds}
                    onChange={(e) => setShutdownTimeoutSeconds(Number(e.target.value))}
                    required
                  />
                </Field>

                <Field>
                  <FieldLabel htmlFor="retention-days">Job History Retention (days)</FieldLabel>
                  <FieldDescription>Historical execution log prune interval</FieldDescription>
                  <Input
                    id="retention-days"
                    type="number"
                    min={1}
                    max={365}
                    value={jobRetentionDays}
                    onChange={(e) => setJobRetentionDays(Number(e.target.value))}
                    required
                  />
                </Field>
              </div>

              <div className="flex gap-3 pt-2">
                <Button variant="outline" onClick={() => setCurrentStep(2)}>
                  <ArrowLeft data-icon="inline-start" />
                  Back
                </Button>
                <Button variant="outline" onClick={handleSkipSafeguards}>
                  Keep defaults & continue
                </Button>
                <Button type="submit" disabled={setAppSettingMutation.isPending} className="flex-1">
                  {setAppSettingMutation.isPending ? "Saving Safeguards..." : "Next: Initial Pool"}
                  <ArrowRight data-icon="inline-end" />
                </Button>
              </div>
            </FieldGroup>
          </form>
        )}

        {/* Step 4: Initial Runner Pool Setup & Overrides */}
        {currentStep === 4 &&
          (!createdAuthProfileId && !status?.authProfileExists ? (
            <div className="mt-6 text-xs">
              <div>
                <h2 className="text-sm font-bold text-slate-900 dark:text-white">
                  Step 4 of 5: Initial Runner Pool Setup
                </h2>
                <p className="mt-0.5 text-slate-500 dark:text-slate-400">
                  Configure your first auto-scaling runner pool, concurrency targets, and container
                  resource limits.
                </p>
              </div>

              <div className="rounded-xl border border-amber-200 bg-amber-50/60 p-4 text-amber-800 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-300">
                <div className="flex items-start gap-3">
                  <Info className="mt-0.5 h-5 w-5 shrink-0 text-amber-600 dark:text-amber-400" />
                  <div className="space-y-1">
                    <p className="font-semibold text-slate-900 dark:text-white">
                      Git Authentication Profile Required
                    </p>
                    <p className="text-[11px] text-amber-700 dark:text-amber-400">
                      Runner pools require a Git authentication profile to register runners with
                      your Git provider. Since the Git Auth step was skipped, initial pool setup
                      cannot be configured right now. You can configure pools later from the
                      Dashboard.
                    </p>
                  </div>
                </div>
              </div>

              <div className="flex gap-3 pt-2">
                <Button variant="outline" onClick={() => setCurrentStep(2)}>
                  <ArrowLeft data-icon="inline-start" />
                  Configure Git Profile
                </Button>
                <Button className="flex-1" onClick={handleSkipPool}>
                  Skip Pool Setup & Review
                  <ArrowRight data-icon="inline-end" />
                </Button>
              </div>
            </div>
          ) : (
            <form onSubmit={handlePoolSubmit} className="mt-6 text-xs">
              <FieldGroup>
                <div>
                  <h2 className="text-sm font-bold text-slate-900 dark:text-white">
                    Step 4 of 5: Initial Runner Pool Setup
                  </h2>
                  <p className="mt-0.5 text-slate-500 dark:text-slate-400">
                    Configure your first auto-scaling runner pool, concurrency targets, and
                    container resource limits.
                  </p>
                </div>

                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                  <Field>
                    <FieldLabel htmlFor="pool-name">Pool Name</FieldLabel>
                    <Input
                      id="pool-name"
                      type="text"
                      value={poolName}
                      onChange={(e) => setPoolName(e.target.value)}
                      required
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor="pool-scope">Pool Scope</FieldLabel>
                    <Select
                      value={scope}
                      onValueChange={(v) => setScope(v as "repo" | "org")}
                      items={[
                        { value: "repo", label: "Repository Level (Single Repo)" },
                        { value: "org", label: "Organization Level (Org-wide Runners)" },
                      ]}
                    >
                      <SelectTrigger id="pool-scope">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectGroup>
                          <SelectItem value="repo">Repository Level (Single Repo)</SelectItem>
                          <SelectItem value="org">Organization Level (Org-wide Runners)</SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  </Field>

                  <Field className="sm:col-span-2">
                    <FieldLabel htmlFor="repo-url">Repository / Organization URL</FieldLabel>
                    <Input
                      id="repo-url"
                      type="url"
                      value={repositoryUrl}
                      onChange={(e) => setRepositoryUrl(e.target.value)}
                      placeholder="https://github.com/my-org/my-repo"
                      required
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor="min-idle">Min Idle Runners</FieldLabel>
                    <FieldDescription>
                      Warm standby containers ready for instant dispatch
                    </FieldDescription>
                    <Input
                      id="min-idle"
                      type="number"
                      min={0}
                      max={20}
                      value={minIdleRunners}
                      onChange={(e) => setMinIdleRunners(Number(e.target.value))}
                      required
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor="max-concurrency">Max Concurrency</FieldLabel>
                    <FieldDescription>
                      Peak simultaneous runner containers for this pool
                    </FieldDescription>
                    <Input
                      id="max-concurrency"
                      type="number"
                      min={1}
                      max={50}
                      value={maxConcurrency}
                      onChange={(e) => setMaxConcurrency(Number(e.target.value))}
                      required
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor="runner-labels">Runner Labels</FieldLabel>
                    <FieldDescription>
                      Comma-separated labels matched against workflow `runs-on`
                    </FieldDescription>
                    <Input
                      id="runner-labels"
                      type="text"
                      value={labels}
                      onChange={(e) => setCustomLabels(e.target.value)}
                      required
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor="runner-image">Runner Docker Image</FieldLabel>
                    <FieldDescription>
                      Base multi-arch image deployed for runner instances
                    </FieldDescription>
                    <Input
                      id="runner-image"
                      type="text"
                      value={runnerImage}
                      onChange={(e) => setRunnerImage(e.target.value)}
                      required
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor="cpu-limit">CPU Limit</FieldLabel>
                    <Input
                      id="cpu-limit"
                      type="text"
                      value={cpuLimit}
                      onChange={(e) => setCpuLimit(e.target.value)}
                      required
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor="mem-limit">Memory Limit</FieldLabel>
                    <Input
                      id="mem-limit"
                      type="text"
                      value={memoryLimit}
                      onChange={(e) => setMemoryLimit(e.target.value)}
                      required
                    />
                  </Field>
                </div>

                {/* Docker Policy (docs/05 §4 enforcement) */}
                <div className="rounded-xl border border-slate-100 bg-slate-50 p-3.5 dark:border-slate-800 dark:bg-slate-800/50">
                  <Field orientation="horizontal">
                    <FieldContent>
                      <FieldLabel htmlFor="allow-docker">Allow Docker in Container</FieldLabel>
                      <FieldDescription>
                        Exposes host Docker socket or runs DinD daemon inside worker containers.
                      </FieldDescription>
                    </FieldContent>
                    <Checkbox
                      id="allow-docker"
                      checked={effectiveAllowDocker}
                      disabled={isDockerLocked}
                      data-disabled={isDockerLocked || undefined}
                      onCheckedChange={(v) => setAllowDocker(v === true)}
                    />
                  </Field>
                  {isDockerLocked && (
                    <div className="mt-2 flex items-center gap-1.5 text-[10px] font-medium text-amber-600 dark:text-amber-400">
                      <Info className="h-3 w-3 shrink-0" />
                      <span>
                        Locked to Enabled for {deducedProvider.toUpperCase()} runners: workflow
                        execution requires Docker containerization (docs/05 §4).
                      </span>
                    </div>
                  )}
                </div>

                {/* Renovate Bot Toggle */}
                <div className="rounded-xl border border-slate-100 bg-slate-50 p-3.5 dark:border-slate-800 dark:bg-slate-800/50">
                  <Field orientation="horizontal">
                    <FieldContent>
                      <FieldLabel htmlFor="enable-renovate">
                        Enable Renovate Dependency Automation
                      </FieldLabel>
                      <FieldDescription>
                        Schedule automated dependency scanning and PR creation directly on this
                        runner pool.
                      </FieldDescription>
                    </FieldContent>
                    <Checkbox
                      id="enable-renovate"
                      checked={renovateEnabled}
                      onCheckedChange={(v) => setRenovateEnabled(v === true)}
                    />
                  </Field>

                  {renovateEnabled && (
                    <div className="mt-3 grid grid-cols-1 gap-3 border-t border-slate-200/60 pt-3 dark:border-slate-700/60 sm:grid-cols-2">
                      <Field>
                        <FieldLabel htmlFor="renovate-cron">Cron Schedule</FieldLabel>
                        <Input
                          id="renovate-cron"
                          type="text"
                          value={renovateCron}
                          onChange={(e) => setRenovateCron(e.target.value)}
                          placeholder="0 2 * * *"
                          required
                        />
                      </Field>
                      <Field>
                        <FieldLabel htmlFor="renovate-img">Renovate Image</FieldLabel>
                        <Input
                          id="renovate-img"
                          type="text"
                          value={renovateImage}
                          onChange={(e) => setRenovateImage(e.target.value)}
                          required
                        />
                      </Field>
                    </div>
                  )}
                </div>

                <div className="flex gap-3 pt-2">
                  <Button variant="outline" onClick={() => setCurrentStep(3)}>
                    <ArrowLeft data-icon="inline-start" />
                    Back
                  </Button>
                  <Button variant="outline" onClick={handleSkipPool}>
                    Skip this step
                  </Button>
                  <Button type="submit" className="flex-1">
                    Next: Review & Launch
                    <ArrowRight data-icon="inline-end" />
                  </Button>
                </div>
              </FieldGroup>
            </form>
          ))}

        {/* Step 5: Review & Confirm Launch */}
        {currentStep === 5 && (
          <form onSubmit={handleLaunchSubmit} className="mt-6 text-xs">
            <FieldGroup>
              <div>
                <h2 className="text-sm font-bold text-slate-900 dark:text-white">
                  Step 5 of 5: Review & Launch Supervisor
                </h2>
                <p className="mt-0.5 text-slate-500 dark:text-slate-400">
                  Verify system initialization settings before starting reconciliation control
                  loops.
                </p>
              </div>

              {/* Review Cards Grid */}
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                {/* Card 1: Admin */}
                <div className="rounded-xl border border-slate-200 bg-slate-50/50 p-3.5 dark:border-slate-800 dark:bg-slate-800/40">
                  <div className="flex items-center gap-2 font-bold text-slate-900 dark:text-white">
                    <ShieldCheck className="h-4 w-4 text-blue-600 dark:text-blue-400" />
                    <span>Master Administrator</span>
                  </div>
                  <div className="mt-2 space-y-1 text-slate-600 dark:text-slate-400">
                    <div className="flex justify-between">
                      <span>Username:</span>
                      <span className="font-semibold text-slate-800 dark:text-slate-200">
                        {username}
                      </span>
                    </div>
                    <div className="flex justify-between">
                      <span>Session:</span>
                      <span className="font-semibold text-emerald-600">Active</span>
                    </div>
                  </div>
                </div>

                {/* Card 2: Git Provider */}
                <div className="rounded-xl border border-slate-200 bg-slate-50/50 p-3.5 dark:border-slate-800 dark:bg-slate-800/40">
                  <div className="flex items-center gap-2 font-bold text-slate-900 dark:text-white">
                    <KeyRound
                      className={`h-4 w-4 ${isGitProfileSkipped ? "text-slate-400" : "text-blue-600 dark:text-blue-400"}`}
                    />
                    <span>Git Auth Profile</span>
                  </div>
                  {isGitProfileSkipped ? (
                    <div className="mt-2 text-slate-500 italic dark:text-slate-400">
                      Skipped &mdash; not configured
                    </div>
                  ) : (
                    <div className="mt-2 space-y-1 text-slate-600 dark:text-slate-400">
                      <div className="flex justify-between">
                        <span>Profile Name:</span>
                        <span className="font-semibold text-slate-800 dark:text-slate-200">
                          {profileName}
                        </span>
                      </div>
                      <div className="flex justify-between">
                        <span>Method:</span>
                        <span className="font-semibold uppercase text-slate-800 dark:text-slate-200">
                          {authMethod}
                        </span>
                      </div>
                    </div>
                  )}
                </div>

                {/* Card 3: Safeguards */}
                <div className="rounded-xl border border-slate-200 bg-slate-50/50 p-3.5 dark:border-slate-800 dark:bg-slate-800/40">
                  <div className="flex items-center gap-2 font-bold text-slate-900 dark:text-white">
                    <Sliders className="h-4 w-4 text-blue-600 dark:text-blue-400" />
                    <span>Global Constraints</span>
                  </div>
                  <div className="mt-2 space-y-1 text-slate-600 dark:text-slate-400">
                    <div className="flex justify-between">
                      <span>Max Runners:</span>
                      <span className="font-semibold text-slate-800 dark:text-slate-200">
                        {totalAllowedRunners}
                      </span>
                    </div>
                    <div className="flex justify-between">
                      <span>Warm Idle Pool:</span>
                      <span className="font-semibold text-slate-800 dark:text-slate-200">
                        {totalIdleWarmPool}
                      </span>
                    </div>
                    <div className="flex justify-between">
                      <span>Shutdown Timeout:</span>
                      <span className="font-semibold text-slate-800 dark:text-slate-200">
                        {shutdownTimeoutSeconds}s
                      </span>
                    </div>
                  </div>
                </div>

                {/* Card 4: Initial Pool */}
                <div className="rounded-xl border border-slate-200 bg-slate-50/50 p-3.5 dark:border-slate-800 dark:bg-slate-800/40">
                  <div className="flex items-center gap-2 font-bold text-slate-900 dark:text-white">
                    <Server
                      className={`h-4 w-4 ${isPoolSkipped ? "text-slate-400" : "text-blue-600 dark:text-blue-400"}`}
                    />
                    <span>{isPoolSkipped ? "Initial Pool" : `Initial Pool: ${poolName}`}</span>
                  </div>
                  {isPoolSkipped ? (
                    <div className="mt-2 text-slate-500 italic dark:text-slate-400">
                      Skipped &mdash; no pool created
                    </div>
                  ) : (
                    <div className="mt-2 space-y-1 text-slate-600 dark:text-slate-400">
                      <div className="flex justify-between">
                        <span>Target URL:</span>
                        <span className="max-w-[120px] truncate font-semibold text-slate-800 dark:text-slate-200">
                          {repositoryUrl}
                        </span>
                      </div>
                      <div className="flex justify-between">
                        <span>Concurrency:</span>
                        <span className="font-semibold text-slate-800 dark:text-slate-200">
                          {minIdleRunners} idle / {maxConcurrency} max
                        </span>
                      </div>
                      <div className="flex justify-between">
                        <span>Docker Access:</span>
                        <span className="font-semibold text-emerald-600">
                          {effectiveAllowDocker ? "Enabled" : "Disabled"}
                        </span>
                      </div>
                      <div className="flex justify-between">
                        <span>Renovate:</span>
                        <span className="font-semibold text-slate-800 dark:text-slate-200">
                          {renovateEnabled ? renovateCron : "Disabled"}
                        </span>
                      </div>
                    </div>
                  )}
                </div>
              </div>

              <div className="rounded-xl border border-emerald-200 bg-emerald-50/60 p-3 text-emerald-800 dark:border-emerald-900/50 dark:bg-emerald-950/30 dark:text-emerald-300">
                <div className="flex items-center gap-2 font-semibold">
                  <Rocket className="h-4 w-4" />
                  <span>{hasPoolToLaunch ? "Ready to Launch" : "Ready to Finish Setup"}</span>
                </div>
                <p className="mt-1 text-[11px] text-emerald-700 dark:text-emerald-400">
                  {hasPoolToLaunch
                    ? "Upon confirmation, the supervisor reconciler will start immediately, register runner containers with your Git provider, and transition to the live dashboard."
                    : "Upon confirmation, system initialization will be completed and you will transition to the dashboard where you can configure providers and pools at any time."}
                </p>
              </div>

              <div className="flex gap-3 pt-2">
                <Button variant="outline" onClick={() => setCurrentStep(4)}>
                  <ArrowLeft data-icon="inline-start" />
                  Back
                </Button>
                <Button
                  type="submit"
                  disabled={createPoolMutation.isPending || completeOnboardingMutation.isPending}
                  className="flex-1 bg-success text-white hover:bg-success/80"
                >
                  <Rocket data-icon="inline-start" />
                  {createPoolMutation.isPending || completeOnboardingMutation.isPending
                    ? "Completing Setup..."
                    : hasPoolToLaunch
                      ? "Confirm & Launch Supervisor"
                      : "Finish & Open Dashboard"}
                </Button>
              </div>
            </FieldGroup>
          </form>
        )}
      </Card>
    </div>
  );
}
