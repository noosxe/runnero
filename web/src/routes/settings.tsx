import { useEffect, useState } from "react";
import { useStore } from "@tanstack/react-form";
import { useNavigate } from "@tanstack/react-router";
import { usePageTitle } from "../hooks/use-page-title";
import { useAppForm, applyFieldErrors } from "../lib/forms";
import { cn } from "cn";
import { Button } from "@/components/ui/button";
import { WarningBadge } from "@/components/common/warning-badge";
import { Empty, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { toast } from "@/components/ui/toast";
import { DataTable, useAppTable } from "../lib/tables";
import { poolImagesColumns, poolImagesEmpty } from "./settings-columns";
import {
  useAppSettings,
  useSetAppSetting,
  usePools,
  useImageUpdates,
  useCheckImageUpdate,
  useIsAdmin,
} from "../lib/api/query-hooks";
import { UsersCard } from "../components/settings/users-card";
import { InstanceCard } from "../components/settings/instance-card";
import { ImageUpdateNotification } from "../components/notifications/image-update-notification";
import {
  Sliders,
  RefreshCw,
  Database,
  Save,
  Archive,
  ShieldCheck,
  Users,
  Info,
} from "lucide-react";
import { SecurityTab } from "../components/security/security-tab";
import { resolveRouteTab } from "../lib/route-tab";

export interface SettingsPageSearch {
  tab?: string;
}

/**
 * Settings tabs and their role visibility (docs/35 section 2.4): the tab
 * strip shows instance + security for every role; the admin surfaces are
 * admin-only. Tab selection lives in the URL (RUN-257), so this list is
 * the clamp set for `?tab=` values.
 */
type SettingsTab = "instance" | "constraints" | "images" | "backups" | "security" | "users";

const SETTINGS_TABS: readonly { id: SettingsTab; adminOnly: boolean }[] = [
  { id: "instance", adminOnly: false },
  { id: "constraints", adminOnly: true },
  { id: "images", adminOnly: true },
  { id: "backups", adminOnly: true },
  { id: "security", adminOnly: false },
  { id: "users", adminOnly: true },
];

/**
 * Global constraints form (docs/30 §5.4): the four retention/quota values
 * persist via SetAppSettingRequest whose only wire rule is the key — the
 * value ranges are UI policy, so they live as class C rules in the form
 * layer and gate the save (docs/30 §5.1).
 */
interface ConstraintFormValues {
  totalAllowedRunners: string;
  totalIdleWarmPool: string;
  gracefulShutdownTimeout: string;
  jobRetentionDays: string;
}

const CONSTRAINT_FIELDS = [
  "totalAllowedRunners",
  "totalIdleWarmPool",
  "gracefulShutdownTimeout",
  "jobRetentionDays",
] as const;

const CONSTRAINT_BOUNDS: Record<
  keyof ConstraintFormValues,
  { min: number; max: number; label: string }
> = {
  totalAllowedRunners: { min: 1, max: 100, label: "Global Runner Quota" },
  totalIdleWarmPool: { min: 0, max: 20, label: "Warm Idle Pool Limit" },
  gracefulShutdownTimeout: { min: 30, max: 3600, label: "Graceful Drain Timeout" },
  jobRetentionDays: { min: 1, max: 365, label: "History Retention Period" },
};

export function SettingsPage({ search }: { search: SettingsPageSearch }) {
  usePageTitle("Settings");
  const isAdmin = useIsAdmin();
  const navigate = useNavigate();
  // Tab selection is URL state (RUN-257): `/settings?tab=…` deep links
  // work and browser back/forward restores the previous tab. Visible tabs
  // are role-scoped (docs/35 section 2.4): a viewer's settings page is the
  // Security tab only, so unknown or admin-only `?tab=` values clamp to
  // the role default — admins land on Global Constraints, viewers on
  // Security. The session query is prefetched by the authenticated route
  // guard, so the role (and thus the default) is known on first paint.
  const visibleTabs = SETTINGS_TABS.filter((t) => isAdmin || !t.adminOnly);
  const activeTab = resolveRouteTab(
    search.tab,
    visibleTabs.map((t) => t.id),
    isAdmin ? "constraints" : "security",
  );
  const selectTab = (tab: SettingsTab) => {
    void navigate({ to: "/settings", search: { tab } });
  };

  // Admin-bucket read: only fired for admins (docs/35 section 2.2).
  const { data: settings, isLoading: settingsLoading } = useAppSettings(isAdmin);
  const { data: pools } = usePools();
  const { data: updates } = useImageUpdates();
  const setSettingMutation = useSetAppSetting();
  const checkUpdateMutation = useCheckImageUpdate();

  // Configured Pool Images table (docs/31 §5 phase 0): hook lives at the
  // component top level — never inside the conditionally-rendered tab JSX.
  const poolImagesTable = useAppTable({
    columns: poolImagesColumns((poolId) => checkUpdateMutation.mutate(poolId)),
    data: pools ?? [],
    getRowId: (pool) => pool.id.toString(),
  });

  // Form State for Global Constraints (docs/30 toolkit — no hand-rolled stack)
  const form = useAppForm({
    defaultValues: {
      totalAllowedRunners: "20",
      totalIdleWarmPool: "5",
      gracefulShutdownTimeout: "300",
      jobRetentionDays: "30",
    } as ConstraintFormValues,
  });
  const formValues = useStore(form.store, (s) => s.values);
  useStore(form.store, (s) => s.fieldMeta);
  const [isSaving, setIsSaving] = useState(false);
  const [isCheckingUpdates, setIsCheckingUpdates] = useState(false);

  // Adopt server values once loaded (local edits before load are dropped —
  // the load happens long before a user can type).
  useEffect(() => {
    if (!settings) return;
    const map = new Map(settings.map((s) => [s.key, s.value]));
    // v1.33: form.reset(values) does not propagate new values — set per field.
    form.setFieldValue("totalAllowedRunners", map.get("total_allowed_runners") ?? "20");
    form.setFieldValue("totalIdleWarmPool", map.get("total_idle_warm_pool") ?? "5");
    form.setFieldValue("gracefulShutdownTimeout", map.get("graceful_shutdown_timeout") ?? "300");
    form.setFieldValue("jobRetentionDays", map.get("job_retention_days") ?? "30");
    // eslint-disable-next-line react-hooks/exhaustive-deps -- form API is stable
  }, [settings]);

  /**
   * Class C evaluation (docs/30 §5.1): required + integer + range per field.
   * One map feeds the inline errors and the save gate.
   */
  const runEvaluation = (): Partial<Record<keyof ConstraintFormValues, string[]>> => {
    const fieldErrors: Partial<Record<keyof ConstraintFormValues, string[]>> = {};
    for (const key of CONSTRAINT_FIELDS) {
      const raw = formValues[key].trim();
      if (!raw) {
        (fieldErrors[key] ??= []).push(`${CONSTRAINT_BOUNDS[key].label} is required.`);
        continue;
      }
      const n = Number(raw);
      if (!Number.isFinite(n) || !Number.isInteger(n)) {
        (fieldErrors[key] ??= []).push(`${CONSTRAINT_BOUNDS[key].label} must be a whole number.`);
        continue;
      }
      const { min, max, label } = CONSTRAINT_BOUNDS[key];
      if (n < min || n > max) {
        (fieldErrors[key] ??= []).push(`${label} must be between ${min} and ${max}.`);
      }
    }
    applyFieldErrors(form, fieldErrors, CONSTRAINT_FIELDS);
    return fieldErrors;
  };

  const handleSaveConstraints = async (e: React.FormEvent) => {
    e.preventDefault();
    const fieldErrors = runEvaluation();
    if (Object.keys(fieldErrors).length > 0) {
      return;
    }
    setIsSaving(true);
    try {
      await Promise.all([
        setSettingMutation.mutateAsync({
          key: "total_allowed_runners",
          value: formValues.totalAllowedRunners.trim(),
        }),
        setSettingMutation.mutateAsync({
          key: "total_idle_warm_pool",
          value: formValues.totalIdleWarmPool.trim(),
        }),
        setSettingMutation.mutateAsync({
          key: "graceful_shutdown_timeout",
          value: formValues.gracefulShutdownTimeout.trim(),
        }),
        setSettingMutation.mutateAsync({
          key: "job_retention_days",
          value: formValues.jobRetentionDays.trim(),
        }),
      ]);
      toast.add({
        title: "Settings saved",
        description: "Global constraints successfully persisted.",
        type: "success",
      });
    } finally {
      setIsSaving(false);
    }
  };

  const handleCheckUpdatesAll = async () => {
    if (!pools || pools.length === 0) return;
    setIsCheckingUpdates(true);
    try {
      for (const p of pools) {
        await checkUpdateMutation.mutateAsync(p.id);
      }
    } finally {
      setIsCheckingUpdates(false);
    }
  };

  // Map pool ID to name for notification badges
  const poolNameLookup: Record<string, string> = {};
  if (pools) {
    for (const p of pools) {
      poolNameLookup[p.id.toString()] = p.name;
    }
  }

  return (
    <div className="flex flex-col gap-6">
      {/* Header */}
      <div>
        <h1 className="text-2xl font-bold tracking-tight text-foreground ">
          Supervisor Settings & Administration
        </h1>
        <p className="text-sm text-muted-foreground ">
          Global supervisor constraints, runner image lifecycle updates, and retention policies.
        </p>
      </div>

      {/* Navigation Tabs */}
      <div className="flex border-b border-border ">
        {/* Tab: Instance - informational for every role (RUN-251, docs/09 §4) */}
        <button
          type="button"
          onClick={() => selectTab("instance")}
          className={cn(
            "flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors",
            activeTab === "instance"
              ? "border-primary/50 text-primary"
              : "border-transparent text-muted-foreground hover:text-foreground",
          )}
        >
          <Info className="size-4" />
          <span>Instance</span>
        </button>

        {isAdmin && (
          <button
            type="button"
            onClick={() => selectTab("constraints")}
            className={cn(
              "flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors",
              activeTab === "constraints"
                ? "border-primary/50 text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            <Sliders className="size-4" />
            <span>Global Constraints</span>
          </button>
        )}

        {isAdmin && (
          <button
            type="button"
            onClick={() => selectTab("images")}
            className={cn(
              "relative flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors",
              activeTab === "images"
                ? "border-primary/50 text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            <RefreshCw className="size-4" />
            <span>Runner Image Updates</span>
            {updates && updates.length > 0 && <WarningBadge>{updates.length}</WarningBadge>}
          </button>
        )}

        {isAdmin && (
          <button
            type="button"
            onClick={() => selectTab("backups")}
            className={cn(
              "flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors",
              activeTab === "backups"
                ? "border-primary/50 text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            <Database className="size-4" />
            <span>Database & Retention</span>
          </button>
        )}

        <button
          type="button"
          onClick={() => selectTab("security")}
          className={cn(
            "flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors",
            activeTab === "security"
              ? "border-primary/50 text-primary"
              : "border-transparent text-muted-foreground hover:text-foreground",
          )}
        >
          <ShieldCheck className="size-4" />
          <span>Security</span>
        </button>

        {isAdmin && (
          <button
            type="button"
            onClick={() => selectTab("users")}
            className={cn(
              "flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors",
              activeTab === "users"
                ? "border-primary/50 text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            <Users className="size-4" />
            <span>Users</span>
          </button>
        )}
      </div>

      {/* Tab: Instance - build/host info for every role (RUN-251) */}
      {activeTab === "instance" && <InstanceCard />}

      {/* Tab: Security - self-service for every role (docs/35 section 2.2) */}
      {activeTab === "security" && <SecurityTab />}

      {/* Tab: Users - admin-only management surface (RUN-236) */}
      {isAdmin && activeTab === "users" && <UsersCard />}

      {/* Tab: Global Constraints */}
      {activeTab === "constraints" && (
        <Card>
          <CardHeader className="border-b border-border/60">
            <CardTitle className="text-base font-bold">
              System Concurrency & Resource Limits
            </CardTitle>
            <CardDescription className="text-xs">
              Host-wide guardrails enforced across all runner pools to prevent resource exhaustion.
            </CardDescription>
          </CardHeader>

          {settingsLoading ? (
            <CardContent className="grid max-w-2xl grid-cols-1 gap-4 sm:grid-cols-2">
              {Array.from({ length: 6 }).map((_, i) => (
                <div key={i} className="flex flex-col gap-1.5">
                  <Skeleton className="h-3 w-24" />
                  <Skeleton className="h-9 w-full" />
                </div>
              ))}
              <Skeleton className="h-9 w-32" />
            </CardContent>
          ) : (
            <CardContent className="max-w-2xl">
              <form onSubmit={handleSaveConstraints} noValidate className="flex flex-col gap-6">
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                  <form.AppField name="totalAllowedRunners">
                    {(field) => (
                      <field.TextField
                        label="Global Runner Quota"
                        id="total_allowed_runners"
                        type="number"
                        min={1}
                        max={100}
                        suffix="runners"
                        inputClassName="w-full rounded-xl bg-transparent px-3 py-2 text-xs font-mono text-foreground focus:outline-hidden"
                        description="Maximum concurrent active containers across all pools combined."
                        onBlurExtra={runEvaluation}
                      />
                    )}
                  </form.AppField>

                  <form.AppField name="totalIdleWarmPool">
                    {(field) => (
                      <field.TextField
                        label="Warm Idle Pool Limit"
                        id="total_idle_warm_pool"
                        type="number"
                        min={0}
                        max={20}
                        suffix="runners"
                        inputClassName="w-full rounded-xl bg-transparent px-3 py-2 text-xs font-mono text-foreground focus:outline-hidden"
                        description="Maximum standby idle runners kept warm for instant job dispatch."
                        onBlurExtra={runEvaluation}
                      />
                    )}
                  </form.AppField>

                  <form.AppField name="gracefulShutdownTimeout">
                    {(field) => (
                      <field.TextField
                        label="Graceful Drain Timeout"
                        id="graceful_shutdown_timeout"
                        type="number"
                        min={30}
                        max={3600}
                        suffix="seconds"
                        inputClassName="w-full rounded-xl bg-transparent px-3 py-2 text-xs font-mono text-foreground focus:outline-hidden"
                        description="Maximum time to await active workflow completion before SIGKILL."
                        onBlurExtra={runEvaluation}
                      />
                    )}
                  </form.AppField>

                  <form.AppField name="jobRetentionDays">
                    {(field) => (
                      <field.TextField
                        label="History Retention Period"
                        id="job_retention_days"
                        type="number"
                        min={1}
                        max={365}
                        suffix="days"
                        inputClassName="w-full rounded-xl bg-transparent px-3 py-2 text-xs font-mono text-foreground focus:outline-hidden"
                        description="Automated background pruning threshold for finished jobs and log files."
                        onBlurExtra={runEvaluation}
                      />
                    )}
                  </form.AppField>
                </div>
                {/* Submit Actions */}
                <div className="flex items-center gap-3 pt-2">
                  <Button
                    type="submit"
                    onMouseDown={(e) => e.preventDefault()}
                    size="sm"
                    disabled={isSaving}
                  >
                    {isSaving ? (
                      <Spinner data-icon="inline-start" />
                    ) : (
                      <Save data-icon="inline-start" />
                    )}
                    <span>{isSaving ? "Saving..." : "Save Changes"}</span>
                  </Button>
                </div>
              </form>
            </CardContent>
          )}
        </Card>
      )}

      {/* Tab: Runner Image Updates */}
      {activeTab === "images" && (
        <div className="flex flex-col gap-6">
          {/* Action Strip */}
          <Card>
            <CardHeader>
              <CardTitle className="text-base font-bold">Runner Image Update Management</CardTitle>
              <CardDescription className="text-xs">
                Periodically verifies upstream container image digests (GHCR, Docker Hub) and pulls
                updates gracefully.
              </CardDescription>
              <CardAction>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={handleCheckUpdatesAll}
                  disabled={isCheckingUpdates || !pools || pools.length === 0}
                >
                  {isCheckingUpdates ? (
                    <Spinner data-icon="inline-start" />
                  ) : (
                    <RefreshCw data-icon="inline-start" />
                  )}
                  <span>{isCheckingUpdates ? "Checking Updates..." : "Check All Pools Now"}</span>
                </Button>
              </CardAction>
            </CardHeader>
          </Card>
          {/* Pending Notifications */}
          {updates && updates.length > 0 ? (
            <div className="flex flex-col gap-2">
              <h3 className="text-xs font-bold uppercase tracking-wider text-muted-foreground">
                Pending Image Notifications
              </h3>
              <ImageUpdateNotification updates={updates} poolNameLookup={poolNameLookup} />
            </div>
          ) : (
            <Empty>
              <EmptyHeader>
                <EmptyTitle>
                  No pending image updates. All runner pools are running the latest image digest.
                </EmptyTitle>
              </EmptyHeader>
            </Empty>
          )}

          {/* Pools Image Registry Overview */}
          <Card className="py-0">
            <div className="border-b border-border/60 p-4">
              <h3 className="text-xs font-bold uppercase tracking-wider text-muted-foreground">
                Configured Pool Images
              </h3>
            </div>
            <DataTable table={poolImagesTable} empty={poolImagesEmpty} />
          </Card>
        </div>
      )}

      {/* Tab: Database & Retention */}
      {activeTab === "backups" && (
        <Card className="px-(--card-spacing)">
          <CardHeader className="border-b border-border/60">
            <CardTitle className="text-base font-bold">
              Database Retention & Periodic Cleanup
            </CardTitle>
            <CardDescription className="text-xs">
              Automatic pruning of historical job records and compressed JSONL log files.
            </CardDescription>
          </CardHeader>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div className="rounded-xl border border-border/60 bg-muted/50 p-4">
              <div className="flex items-center gap-2 text-xs font-bold text-foreground">
                <Archive className="size-4 text-primary" />
                <span>Pruning Interval</span>
              </div>
              <p className="mt-2 text-xs text-muted-foreground">
                Retention window active:{" "}
                <strong className="text-foreground">{formValues.jobRetentionDays} days</strong>.
                Records older than this threshold are pruned hourly.
              </p>
            </div>

            <div className="rounded-xl border border-border/60 bg-muted/50 p-4">
              <div className="flex items-center gap-2 text-xs font-bold text-foreground">
                <Database className="size-4 text-success" />
                <span>Storage Engine</span>
              </div>
              <p className="mt-2 text-xs text-muted-foreground">
                Embedded SQLite engine with WAL mode and atomic transactions in{" "}
                <code>DATA_DIR/supervisor.db</code>.
              </p>
            </div>
          </div>
        </Card>
      )}
    </div>
  );
}
