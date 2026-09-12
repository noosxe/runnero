import { useState, useMemo } from "react";
import { FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
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
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from "@/components/ui/table";
import {
  useAppSettings,
  useSetAppSetting,
  usePools,
  useImageUpdates,
  useCheckImageUpdate,
} from "../lib/api/query-hooks";
import { ImageUpdateNotification } from "../components/notifications/image-update-notification";
import { Sliders, Calendar, RefreshCw, Database, Save, Layers, Clock, Archive } from "lucide-react";

export function SettingsPage() {
  const [activeTab, setActiveTab] = useState<"constraints" | "images" | "backups">("constraints");

  const { data: settings, isLoading: settingsLoading } = useAppSettings();
  const { data: pools } = usePools();
  const { data: updates } = useImageUpdates();
  const setSettingMutation = useSetAppSetting();
  const checkUpdateMutation = useCheckImageUpdate();

  // Form State for Global Constraints
  const [localOverrides, setLocalOverrides] = useState<Record<string, string>>({});
  const [isSaving, setIsSaving] = useState(false);
  const [isCheckingUpdates, setIsCheckingUpdates] = useState(false);

  const settingsMap = useMemo(() => {
    return new Map(settings?.map((s) => [s.key, s.value]) ?? []);
  }, [settings]);

  const totalAllowedRunners =
    localOverrides.total_allowed_runners ?? settingsMap.get("total_allowed_runners") ?? "20";
  const totalIdleWarmPool =
    localOverrides.total_idle_warm_pool ?? settingsMap.get("total_idle_warm_pool") ?? "5";
  const gracefulShutdownTimeout =
    localOverrides.graceful_shutdown_timeout ??
    settingsMap.get("graceful_shutdown_timeout") ??
    "300";
  const jobRetentionDays =
    localOverrides.job_retention_days ?? settingsMap.get("job_retention_days") ?? "30";

  const handleSaveConstraints = async (e: React.FormEvent) => {
    e.preventDefault();
    setIsSaving(true);
    try {
      await Promise.all([
        setSettingMutation.mutateAsync({
          key: "total_allowed_runners",
          value: totalAllowedRunners,
        }),
        setSettingMutation.mutateAsync({
          key: "total_idle_warm_pool",
          value: totalIdleWarmPool,
        }),
        setSettingMutation.mutateAsync({
          key: "graceful_shutdown_timeout",
          value: gracefulShutdownTimeout,
        }),
        setSettingMutation.mutateAsync({
          key: "job_retention_days",
          value: jobRetentionDays,
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
    <div className="space-y-6">
      {/* Header */}
      <div>
        <h1 className="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
          Supervisor Settings & Administration
        </h1>
        <p className="text-sm text-slate-500 dark:text-slate-400">
          Global supervisor constraints, runner image lifecycle updates, and retention policies.
        </p>
      </div>

      {/* Navigation Tabs */}
      <div className="flex border-b border-slate-200 dark:border-slate-800">
        <button
          type="button"
          onClick={() => setActiveTab("constraints")}
          className={`flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors ${
            activeTab === "constraints"
              ? "border-blue-500 text-blue-600 dark:text-blue-400"
              : "border-transparent text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-200"
          }`}
        >
          <Sliders className="h-4 w-4" />
          <span>Global Constraints</span>
        </button>

        <button
          type="button"
          onClick={() => setActiveTab("images")}
          className={`relative flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors ${
            activeTab === "images"
              ? "border-blue-500 text-blue-600 dark:text-blue-400"
              : "border-transparent text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-200"
          }`}
        >
          <RefreshCw className="h-4 w-4" />
          <span>Runner Image Updates</span>
          {updates && updates.length > 0 && (
            <Badge className="h-4 bg-warning px-1.5 text-[10px] font-bold text-white">
              {updates.length}
            </Badge>
          )}
        </button>

        <button
          type="button"
          onClick={() => setActiveTab("backups")}
          className={`flex items-center gap-2 border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors ${
            activeTab === "backups"
              ? "border-blue-500 text-blue-600 dark:text-blue-400"
              : "border-transparent text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-200"
          }`}
        >
          <Database className="h-4 w-4" />
          <span>Database & Retention</span>
        </button>
      </div>

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
                <div key={i} className="space-y-1.5">
                  <Skeleton className="h-3 w-24" />
                  <Skeleton className="h-9 w-full" />
                </div>
              ))}
              <Skeleton className="h-9 w-32" />
            </CardContent>
          ) : (
            <CardContent className="max-w-2xl">
              <form onSubmit={handleSaveConstraints} className="space-y-6">
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                  {/* Total Allowed Runners */}
                  <div className="space-y-1.5">
                    <FieldLabel
                      htmlFor="total_allowed_runners"
                      className="flex items-center gap-1.5 text-xs uppercase tracking-wider dark:"
                    >
                      <Layers className="h-3.5 w-3.5 text-blue-500" />
                      <span>Global Runner Quota</span>
                    </FieldLabel>
                    <div className="flex rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-700 dark:bg-slate-800">
                      <Input
                        id="total_allowed_runners"
                        type="number"
                        min="1"
                        max="100"
                        value={totalAllowedRunners}
                        onChange={(e) =>
                          setLocalOverrides((prev) => ({
                            ...prev,
                            total_allowed_runners: e.target.value,
                          }))
                        }
                        className="w-full rounded-xl bg-transparent px-3 py-2 text-xs font-mono text-slate-900 focus:outline-hidden dark:text-white"
                      />
                      <span className="flex items-center px-3 text-xs text-slate-400">runners</span>
                    </div>
                    <p className="text-[11px] text-slate-400">
                      Maximum concurrent active containers across all pools combined.
                    </p>
                  </div>

                  {/* Warm Idle Pool Limit */}
                  <div className="space-y-1.5">
                    <FieldLabel
                      htmlFor="total_idle_warm_pool"
                      className="flex items-center gap-1.5 text-xs uppercase tracking-wider dark:"
                    >
                      <Clock className="h-3.5 w-3.5 text-indigo-500" />
                      <span>Warm Idle Pool Limit</span>
                    </FieldLabel>
                    <div className="flex rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-700 dark:bg-slate-800">
                      <Input
                        id="total_idle_warm_pool"
                        type="number"
                        min="0"
                        max="20"
                        value={totalIdleWarmPool}
                        onChange={(e) =>
                          setLocalOverrides((prev) => ({
                            ...prev,
                            total_idle_warm_pool: e.target.value,
                          }))
                        }
                        className="w-full rounded-xl bg-transparent px-3 py-2 text-xs font-mono text-slate-900 focus:outline-hidden dark:text-white"
                      />
                      <span className="flex items-center px-3 text-xs text-slate-400">runners</span>
                    </div>
                    <p className="text-[11px] text-slate-400">
                      Maximum standby idle runners kept warm for instant job dispatch.
                    </p>
                  </div>

                  {/* Graceful Shutdown Timeout */}
                  <div className="space-y-1.5">
                    <FieldLabel
                      htmlFor="graceful_shutdown_timeout"
                      className="flex items-center gap-1.5 text-xs uppercase tracking-wider dark:"
                    >
                      <Clock className="h-3.5 w-3.5 text-amber-500" />
                      <span>Graceful Drain Timeout</span>
                    </FieldLabel>
                    <div className="flex rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-700 dark:bg-slate-800">
                      <Input
                        id="graceful_shutdown_timeout"
                        type="number"
                        min="30"
                        max="3600"
                        value={gracefulShutdownTimeout}
                        onChange={(e) =>
                          setLocalOverrides((prev) => ({
                            ...prev,
                            graceful_shutdown_timeout: e.target.value,
                          }))
                        }
                        className="w-full rounded-xl bg-transparent px-3 py-2 text-xs font-mono text-slate-900 focus:outline-hidden dark:text-white"
                      />
                      <span className="flex items-center px-3 text-xs text-slate-400">seconds</span>
                    </div>
                    <p className="text-[11px] text-slate-400">
                      Maximum time to await active workflow completion before SIGKILL.
                    </p>
                  </div>

                  {/* History Retention Period */}
                  <div className="space-y-1.5">
                    <FieldLabel
                      htmlFor="job_retention_days"
                      className="flex items-center gap-1.5 text-xs uppercase tracking-wider dark:"
                    >
                      <Calendar className="h-3.5 w-3.5 text-emerald-500" />
                      <span>History Retention Period</span>
                    </FieldLabel>
                    <div className="flex rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-700 dark:bg-slate-800">
                      <Input
                        id="job_retention_days"
                        type="number"
                        min="1"
                        max="365"
                        value={jobRetentionDays}
                        onChange={(e) =>
                          setLocalOverrides((prev) => ({
                            ...prev,
                            job_retention_days: e.target.value,
                          }))
                        }
                        className="w-full rounded-xl bg-transparent px-3 py-2 text-xs font-mono text-slate-900 focus:outline-hidden dark:text-white"
                      />
                      <span className="flex items-center px-3 text-xs text-slate-400">days</span>
                    </div>
                    <p className="text-[11px] text-slate-400">
                      Automated background pruning threshold for finished jobs and log files.
                    </p>
                  </div>
                </div>

                {/* Submit Actions */}
                <div className="flex items-center gap-3 pt-2">
                  <Button type="submit" size="sm" disabled={isSaving}>
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
        <div className="space-y-6">
          {/* Action Strip */}
          <Card>
            <CardHeader>
              <div>
                <CardTitle className="text-base font-bold">
                  Runner Image Update Management
                </CardTitle>
                <CardDescription className="text-xs">
                  Periodically verifies upstream container image digests (GHCR, Docker Hub) and
                  pulls updates gracefully.
                </CardDescription>
              </div>
            </CardHeader>
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
          </Card>
          {/* Pending Notifications */}
          {updates && updates.length > 0 ? (
            <div className="space-y-2">
              <h3 className="text-xs font-bold uppercase tracking-wider text-slate-500">
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
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Pool Name</TableHead>
                  <TableHead>Configured Image</TableHead>
                  <TableHead>Provider</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {!pools || pools.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={4} className="h-24 text-center text-muted-foreground">
                      No pools configured.
                    </TableCell>
                  </TableRow>
                ) : (
                  pools.map((p) => (
                    <TableRow key={p.id.toString()}>
                      <TableCell className="font-semibold">{p.name}</TableCell>
                      <TableCell className="font-mono">{p.runnerImage}</TableCell>
                      <TableCell className="font-mono uppercase text-muted-foreground">
                        {p.provider}
                      </TableCell>
                      <TableCell className="text-right">
                        <Button
                          variant="outline"
                          size="xs"
                          onClick={() => checkUpdateMutation.mutate(p.id)}
                        >
                          Check Update
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
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
                <Archive className="h-4 w-4 text-primary" />
                <span>Pruning Interval</span>
              </div>
              <p className="mt-2 text-xs text-muted-foreground">
                Retention window active:{" "}
                <strong className="text-foreground">{jobRetentionDays} days</strong>. Records older
                than this threshold are pruned hourly.
              </p>
            </div>

            <div className="rounded-xl border border-border/60 bg-muted/50 p-4">
              <div className="flex items-center gap-2 text-xs font-bold text-foreground">
                <Database className="h-4 w-4 text-success" />
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
