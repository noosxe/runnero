import { useState, useMemo } from "react";
import { FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  useAppSettings,
  useSetAppSetting,
  usePools,
  useImageUpdates,
  useCheckImageUpdate,
} from "../lib/api/query-hooks";
import { ImageUpdateNotification } from "../components/notifications/image-update-notification";
import {
  Sliders,
  RefreshCw,
  Database,
  Check,
  Save,
  Loader2,
  Calendar,
  Layers,
  Clock,
  Archive,
} from "lucide-react";

export function SettingsPage() {
  const [activeTab, setActiveTab] = useState<"constraints" | "images" | "backups">("constraints");

  const { data: settings, isLoading: settingsLoading } = useAppSettings();
  const { data: pools } = usePools();
  const { data: updates } = useImageUpdates();
  const setSettingMutation = useSetAppSetting();
  const checkUpdateMutation = useCheckImageUpdate();

  // Form State for Global Constraints
  const [localOverrides, setLocalOverrides] = useState<Record<string, string>>({});
  const [saveSuccess, setSaveSuccess] = useState(false);
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
    setSaveSuccess(false);
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
      setSaveSuccess(true);
      setTimeout(() => setSaveSuccess(false), 3500);
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
        <div className="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900">
          <div className="border-b border-slate-100 pb-4 dark:border-slate-800">
            <h2 className="text-base font-bold text-slate-900 dark:text-white">
              System Concurrency & Resource Limits
            </h2>
            <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
              Host-wide guardrails enforced across all runner pools to prevent resource exhaustion.
            </p>
          </div>

          {settingsLoading ? (
            <div className="py-8 text-center text-xs text-slate-400">Loading settings...</div>
          ) : (
            <form onSubmit={handleSaveConstraints} className="mt-6 space-y-6 max-w-2xl">
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
                    <Loader2 data-icon="inline-start" className="animate-spin" />
                  ) : (
                    <Save data-icon="inline-start" />
                  )}
                  <span>{isSaving ? "Saving..." : "Save Changes"}</span>
                </Button>

                {saveSuccess && (
                  <div className="inline-flex items-center gap-1.5 text-xs font-semibold text-emerald-600 dark:text-emerald-400">
                    <Check className="h-4 w-4" />
                    <span>Settings successfully persisted!</span>
                  </div>
                )}
              </div>
            </form>
          )}
        </div>
      )}

      {/* Tab: Runner Image Updates */}
      {activeTab === "images" && (
        <div className="space-y-6">
          {/* Action Strip */}
          <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3 rounded-2xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
            <div>
              <h2 className="text-base font-bold text-slate-900 dark:text-white">
                Runner Image Update Management
              </h2>
              <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
                Periodically verifies upstream container image digests (GHCR, Docker Hub) and pulls
                updates gracefully.
              </p>
            </div>

            <Button
              variant="outline"
              size="sm"
              onClick={handleCheckUpdatesAll}
              disabled={isCheckingUpdates || !pools || pools.length === 0}
            >
              {isCheckingUpdates ? (
                <Loader2 data-icon="inline-start" className="animate-spin" />
              ) : (
                <RefreshCw data-icon="inline-start" />
              )}
              <span>{isCheckingUpdates ? "Checking Updates..." : "Check All Pools Now"}</span>
            </Button>
          </div>

          {/* Pending Notifications */}
          {updates && updates.length > 0 ? (
            <div className="space-y-2">
              <h3 className="text-xs font-bold uppercase tracking-wider text-slate-500">
                Pending Image Notifications
              </h3>
              <ImageUpdateNotification updates={updates} poolNameLookup={poolNameLookup} />
            </div>
          ) : (
            <div className="rounded-2xl border border-dashed border-slate-200 p-8 text-center text-xs text-slate-400 dark:border-slate-800">
              No pending image updates. All runner pools are running the latest image digest.
            </div>
          )}

          {/* Pools Image Registry Overview */}
          <div className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
            <div className="border-b border-slate-100 p-4 dark:border-slate-800">
              <h3 className="text-xs font-bold uppercase tracking-wider text-slate-500">
                Configured Pool Images
              </h3>
            </div>
            <table className="w-full text-left text-xs">
              <thead className="border-b border-slate-100 bg-slate-50 text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
                <tr>
                  <th className="p-3.5 font-semibold">Pool Name</th>
                  <th className="p-3.5 font-semibold">Configured Image</th>
                  <th className="p-3.5 font-semibold">Provider</th>
                  <th className="p-3.5 font-semibold text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {!pools || pools.length === 0 ? (
                  <tr>
                    <td colSpan={4} className="p-4 text-center text-slate-400">
                      No pools configured.
                    </td>
                  </tr>
                ) : (
                  pools.map((p) => (
                    <tr key={p.id.toString()}>
                      <td className="p-3.5 font-semibold text-slate-900 dark:text-white">
                        {p.name}
                      </td>
                      <td className="p-3.5 font-mono text-slate-700 dark:text-slate-300">
                        {p.runnerImage}
                      </td>
                      <td className="p-3.5 uppercase text-slate-500 font-mono text-[10px]">
                        {p.provider}
                      </td>
                      <td className="p-3.5 text-right">
                        <Button
                          variant="outline"
                          size="xs"
                          onClick={() => checkUpdateMutation.mutate(p.id)}
                        >
                          Check Update
                        </Button>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Tab: Database & Retention */}
      {activeTab === "backups" && (
        <div className="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-6">
          <div className="border-b border-slate-100 pb-4 dark:border-slate-800">
            <h2 className="text-base font-bold text-slate-900 dark:text-white">
              Database Retention & Periodic Cleanup
            </h2>
            <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
              Automatic pruning of historical job records and compressed JSONL log files.
            </p>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div className="rounded-xl border border-slate-200 p-4 dark:border-slate-800">
              <div className="flex items-center gap-2 text-xs font-bold text-slate-900 dark:text-white">
                <Archive className="h-4 w-4 text-blue-500" />
                <span>Pruning Interval</span>
              </div>
              <p className="mt-2 text-xs text-slate-600 dark:text-slate-400">
                Retention window active:{" "}
                <strong className="text-slate-900 dark:text-white">{jobRetentionDays} days</strong>.
                Records older than this threshold are pruned hourly.
              </p>
            </div>

            <div className="rounded-xl border border-slate-200 p-4 dark:border-slate-800">
              <div className="flex items-center gap-2 text-xs font-bold text-slate-900 dark:text-white">
                <Database className="h-4 w-4 text-emerald-500" />
                <span>Storage Engine</span>
              </div>
              <p className="mt-2 text-xs text-slate-600 dark:text-slate-400">
                Embedded SQLite engine with WAL mode and atomic transactions in{" "}
                <code>DATA_DIR/supervisor.db</code>.
              </p>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
