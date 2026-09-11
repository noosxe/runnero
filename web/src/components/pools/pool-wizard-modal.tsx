import { useState, useMemo, type FormEvent } from "react";
import { FieldLabel } from "@/components/ui/field";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Badge } from "@/components/ui/badge";
import { create } from "@bufbuild/protobuf";
import { PoolSchema, type Pool } from "../../gen/api_pb";
import { useCreatePool, useUpdatePool, useDiscoverTargets } from "../../lib/api/query-hooks";
import { getSuggestedRunnerLabels } from "../../lib/utils/labels";
import {
  Server,
  X,
  ChevronRight,
  ChevronLeft,
  Search,
  Check,
  CheckSquare,
  Square,
  ExternalLink,
  AlertCircle,
  Info,
  Loader2,
  Lock,
  Globe,
  Building,
  FolderGit2,
  Layers,
  Bot,
  Pencil,
} from "lucide-react";

export interface PoolWizardModalProps {
  isOpen: boolean;
  onClose: () => void;
  /** Create (default) or edit mode; edit prefills every step from `pool` (docs/22 §7.2). */
  mode?: "create" | "edit";
  /** The pool being edited; required in edit mode, ignored in create mode. */
  pool?: Pool;
  authProfiles?: Array<{
    id: bigint;
    name: string;
    authMethod: string;
  }>;
  hostOs?: string;
  hostArch?: string;
}

export function PoolWizardModal({
  isOpen,
  onClose,
  mode = "create",
  pool,
  authProfiles,
  hostOs,
  hostArch,
}: PoolWizardModalProps) {
  const isEdit = mode === "edit";
  const createPoolMutation = useCreatePool();
  const updatePoolMutation = useUpdatePool();
  const suggestedLabels = getSuggestedRunnerLabels(hostOs, hostArch);

  // Step indicator: 1: Identity, 2: Targets, 3: Specs, 4: Review
  const [currentStep, setCurrentStep] = useState<1 | 2 | 3 | 4>(1);
  const [error, setError] = useState<string | null>(null);

  // Step 1: Identity & Credentials (edit mode prefills from the pool, docs/22 §7.2)
  const [poolName, setPoolName] = useState(pool?.name ?? "");
  const [authProfileId, setAuthProfileId] = useState<string>(
    pool
      ? pool.authProfileId.toString()
      : authProfiles && authProfiles.length > 0
        ? authProfiles[0].id.toString()
        : "",
  );

  // Step 2: Scope & Discovery Targets
  const [scope, setScope] = useState<"repo" | "org">(pool?.scope === "org" ? "org" : "repo");
  const [targetSearch, setTargetSearch] = useState("");
  const [selectedTargetUrls, setSelectedTargetUrls] = useState<string[]>(() => {
    if (!pool) return [];
    if (pool.targetUrls.length > 0) return pool.targetUrls;
    return pool.repositoryUrl ? [pool.repositoryUrl] : [];
  });

  // Step 3: Specs & Quotas
  const [minIdleRunners, setMinIdleRunners] = useState(pool?.minIdleRunners ?? 1);
  const [maxConcurrency, setMaxConcurrency] = useState(pool?.maxConcurrency ?? 5);
  const [customLabels, setCustomLabels] = useState<string | null>(
    pool ? pool.labels.join(",") : null,
  );
  const labels = customLabels ?? suggestedLabels;
  const [runnerImage, setRunnerImage] = useState(
    pool?.runnerImage || "ghcr.io/noosxe/runnero:latest",
  );
  const [allowDocker, setAllowDocker] = useState(pool?.allowDocker ?? true);
  // Demand polling fallback (docs/24 §5.9): GitHub pools only; Forgejo polls
  // natively and Gitea has no repo-scoped queued-jobs API.
  const [pollFallback, setPollFallback] = useState(pool?.pollFallback ?? false);
  const [cpuLimit, setCpuLimit] = useState(pool?.cpuLimit || "2.0");
  const [memoryLimit, setMemoryLimit] = useState(pool?.memoryLimit || "4GB");
  // Lifetime is not wizard-editable; edit mode preserves the stored value
  // instead of silently resetting it to the create-mode default (docs/22 §7.2).
  const [maxRunnerLifetimeSeconds] = useState(pool?.maxRunnerLifetimeSeconds ?? 7200);

  // Renovate Config
  const [renovateEnabled, setRenovateEnabled] = useState(pool?.renovate?.enabled ?? false);
  const [renovateCron, setRenovateCron] = useState(pool?.renovate?.cronSchedule || "0 2 * * *");
  const [renovateImage, setRenovateImage] = useState(
    pool?.renovate?.image || "renovate/renovate:latest",
  );

  // Auth Profile and Provider Resolution.
  // Provider is immutable after creation (docs/22 §5.3): in edit mode the
  // profile selector is locked to profiles of the pool's provider family so
  // the constraint is visible instead of error-driven.
  const providerFamilyOf = (authMethod: string) =>
    authMethod.startsWith("gitea")
      ? "gitea"
      : authMethod.startsWith("forgejo")
        ? "forgejo"
        : "github";

  const selectableAuthProfiles = useMemo(() => {
    if (!authProfiles) return undefined;
    if (!isEdit || !pool) return authProfiles;
    return authProfiles.filter((p) => providerFamilyOf(p.authMethod) === pool.provider);
  }, [authProfiles, isEdit, pool]);

  const selectedAuthProfile = useMemo(() => {
    if (!selectableAuthProfiles || selectableAuthProfiles.length === 0) return null;
    if (authProfileId) {
      return (
        selectableAuthProfiles.find((p) => p.id.toString() === authProfileId) ??
        selectableAuthProfiles[0]
      );
    }
    return selectableAuthProfiles[0];
  }, [selectableAuthProfiles, authProfileId]);

  const deducedProvider = useMemo(() => {
    if (isEdit && pool) return pool.provider;
    const m = selectedAuthProfile?.authMethod;
    if (!m) return "github";
    return providerFamilyOf(m);
  }, [isEdit, pool, selectedAuthProfile]);

  const isDockerLocked = deducedProvider === "gitea" || deducedProvider === "forgejo";

  // Slug validation for pool name: lowercase letters, numbers, and hyphens only
  const isNameSlugValid = useMemo(() => {
    const trimmed = poolName.trim();
    if (!trimmed) return false;
    return /^[a-z0-9-]+$/.test(trimmed);
  }, [poolName]);

  // Edit-mode change detection (docs/22 §5.2, §7.2): drives the review-step
  // changed-fields diff and the runner-impact banners.
  const effectiveRenovateImage = renovateImage.trim() || "renovate/renovate:latest";
  const effectiveRenovateCron = renovateCron.trim() || "0 2 * * *";
  const normalizeSet = (values: string[]) =>
    Array.from(new Set(values.map((v) => v.trim()).filter(Boolean))).sort();

  const changes = useMemo(() => {
    if (!isEdit || !pool) return [];
    const diffs: Array<{ field: string; before: string; after: string; identity: boolean }> = [];
    const add = (field: string, before: string, after: string, identity = false) => {
      if (before !== after) diffs.push({ field, before, after, identity });
    };
    const profileName = selectedAuthProfile?.name ?? pool.authProfileId.toString();
    const beforeProfileName = authProfiles?.find((p) => p.id === pool.authProfileId)?.name;
    add("Name", pool.name, poolName.trim());
    add("Auth Profile", beforeProfileName ?? pool.authProfileId.toString(), profileName, true);
    add("Scope", pool.scope || "repo", scope, true);
    add(
      "Targets",
      normalizeSet(pool.targetUrls.length ? pool.targetUrls : [pool.repositoryUrl]).join(", "),
      normalizeSet(selectedTargetUrls).join(", "),
      true,
    );
    add(
      "Labels",
      normalizeSet(pool.labels).join(", "),
      normalizeSet(labels.split(",")).join(", "),
      true,
    );
    add("Runner Image", pool.runnerImage, runnerImage.trim(), true);
    add("Docker Access", String(pool.allowDocker), String(isDockerLocked || allowDocker), true);
    if (deducedProvider === "github") {
      add(
        "Poll Fallback",
        pool.pollFallback ? "Enabled" : "Disabled",
        pollFallback ? "Enabled" : "Disabled",
      );
    }
    add("CPU Limit", pool.cpuLimit, cpuLimit.trim(), true);
    add("Memory Limit", pool.memoryLimit, memoryLimit.trim(), true);
    add("Min Idle Runners", String(pool.minIdleRunners), String(minIdleRunners));
    add("Max Concurrency", String(pool.maxConcurrency), String(maxConcurrency));
    add(
      "Renovate",
      pool.renovate?.enabled
        ? `enabled (${pool.renovate?.cronSchedule || "0 2 * * *"})`
        : "disabled",
      renovateEnabled ? `enabled (${effectiveRenovateCron})` : "disabled",
    );
    return diffs;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    isEdit,
    pool,
    poolName,
    selectedAuthProfile,
    authProfiles,
    scope,
    selectedTargetUrls,
    labels,
    runnerImage,
    isDockerLocked,
    deducedProvider,
    pollFallback,
    allowDocker,
    cpuLimit,
    memoryLimit,
    minIdleRunners,
    maxConcurrency,
    renovateEnabled,
    renovateCron,
  ]);

  const identityChanged = changes.some((c) => c.identity);
  const renamed = isEdit && !!pool && poolName.trim() !== pool.name;

  // Target discovery query hook
  const activeProfileBigInt = useMemo(() => {
    return selectedAuthProfile ? selectedAuthProfile.id : 0n;
  }, [selectedAuthProfile]);

  const {
    data: discoveryData,
    isLoading: isDiscovering,
    error: discoveryError,
    refetch: refetchDiscovery,
  } = useDiscoverTargets(activeProfileBigInt, scope);

  const discoveredTargets = useMemo(() => {
    if (Array.isArray(discoveryData)) return discoveryData;
    return discoveryData?.targets ?? [];
  }, [discoveryData]);
  const installUrl = Array.isArray(discoveryData) ? "" : (discoveryData?.installUrl ?? "");
  const installations = useMemo(() => {
    if (Array.isArray(discoveryData)) return [];
    return discoveryData?.installations ?? [];
  }, [discoveryData]);

  const manageAccessUrl = useMemo(() => {
    if (installations.length === 1 && installations[0].htmlUrl) {
      return installations[0].htmlUrl;
    }
    return installUrl || "";
  }, [installations, installUrl]);

  // Client-side search filtering of discovered targets
  const filteredDiscoveredTargets = useMemo(() => {
    if (!discoveredTargets) return [];
    if (!targetSearch.trim()) return discoveredTargets;
    const term = targetSearch.toLowerCase();
    return discoveredTargets.filter(
      (t) =>
        t.name.toLowerCase().includes(term) ||
        t.fullName.toLowerCase().includes(term) ||
        t.description.toLowerCase().includes(term) ||
        t.htmlUrl.toLowerCase().includes(term),
    );
  }, [discoveredTargets, targetSearch]);

  const handleToggleTarget = (url: string) => {
    setSelectedTargetUrls((prev) =>
      prev.includes(url) ? prev.filter((u) => u !== url) : [...prev, url],
    );
  };

  const handleSelectAllFiltered = () => {
    const filteredUrls = filteredDiscoveredTargets.map((t) => t.htmlUrl);
    setSelectedTargetUrls((prev) => Array.from(new Set([...prev, ...filteredUrls])));
  };

  const handleClearSelection = () => {
    setSelectedTargetUrls([]);
  };

  // Step Navigation Handlers
  const handleNextFromStep1 = () => {
    setError(null);
    if (!poolName.trim()) {
      setError("Pool name is required");
      return;
    }
    if (!isNameSlugValid) {
      setError(
        "Pool name must contain only lowercase alphanumeric characters and hyphens (e.g. arm64-ci-pool)",
      );
      return;
    }
    if (!selectedAuthProfile) {
      setError("Please select a Git Auth Profile");
      return;
    }
    setCurrentStep(2);
  };

  const handleNextFromStep2 = () => {
    setError(null);
    if (selectedTargetUrls.length === 0) {
      setError(`Please select at least one ${scope === "repo" ? "repository" : "organization"}`);
      return;
    }
    setCurrentStep(3);
  };

  const handleNextFromStep3 = () => {
    setError(null);
    if (minIdleRunners > maxConcurrency) {
      setError("Min idle warm runners cannot exceed max concurrency");
      return;
    }
    setCurrentStep(4);
  };

  const handleSubmitPool = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    if (selectedTargetUrls.length === 0) {
      setError("At least one target URL is required");
      return;
    }

    const editingPool = isEdit ? pool : undefined;
    if (isEdit && !editingPool) {
      setError("No pool to update");
      return;
    }

    const effectiveLabels = labels.trim() || suggestedLabels;
    const parsedLabels = effectiveLabels
      .split(",")
      .map((l) => l.trim())
      .filter(Boolean);

    const poolPayload = create(PoolSchema, {
      ...(editingPool ? { id: editingPool.id } : {}),
      name: poolName.trim(),
      provider: deducedProvider,
      repositoryUrl: selectedTargetUrls[0] || "",
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
      allowDocker: isDockerLocked ? true : allowDocker,
      // The interval is a DB-level knob in v1 (docs/24 §5.4); omitted here so the
      // server applies/preserves the stored cadence.
      pollFallback: deducedProvider === "github" ? pollFallback : false,
      renovate: renovateEnabled
        ? {
            enabled: true,
            cronSchedule: effectiveRenovateCron,
            image: effectiveRenovateImage,
          }
        : undefined,
      authProfileId: selectedAuthProfile?.id ?? 0n,
      scope,
      cpuLimit: cpuLimit.trim() || "2.0",
      memoryLimit: memoryLimit.trim() || "4GB",
      maxRunnerLifetimeSeconds,
      targetUrls: selectedTargetUrls,
    });

    try {
      if (editingPool) {
        await updatePoolMutation.mutateAsync({ pool: poolPayload });
      } else {
        await createPoolMutation.mutateAsync({ pool: poolPayload });
      }
      onClose();
    } catch (err: unknown) {
      setError(
        err instanceof Error
          ? err.message
          : isEdit
            ? "Failed to update runner pool"
            : "Failed to create runner pool",
      );
    }
  };

  return (
    <Dialog
      open={isOpen}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[calc(100dvh-2rem)] gap-4 overflow-y-auto p-6 text-xs sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-base">
            <Server className="size-5 text-primary" />
            {isEdit ? "Edit Runner Pool" : "Create Runner Pool Wizard"}
          </DialogTitle>
        </DialogHeader>

        {/* Step Progress Stepper */}
        <div className="mt-4 flex items-center justify-between border-b border-slate-100 pb-4 dark:border-slate-800">
          {[
            { step: 1, label: "Identity & Auth" },
            { step: 2, label: "Scope & Discovery" },
            { step: 3, label: "Runner Specs" },
            { step: 4, label: isEdit ? "Review & Save" : "Review & Create" },
          ].map((s) => {
            const isActive = currentStep === s.step;
            const isCompleted = currentStep > s.step;
            return (
              <div key={s.step} className="flex items-center gap-2">
                <div
                  className={`flex h-6 w-6 items-center justify-center rounded-full font-bold transition-colors ${
                    isCompleted
                      ? "bg-emerald-600 text-white"
                      : isActive
                        ? "bg-blue-600 text-white"
                        : "bg-slate-100 text-slate-400 dark:bg-slate-800"
                  }`}
                >
                  {isCompleted ? <Check className="h-3.5 w-3.5" /> : s.step}
                </div>
                <span
                  className={`font-semibold hidden sm:inline ${
                    isActive
                      ? "text-blue-600 dark:text-blue-400"
                      : isCompleted
                        ? "text-slate-900 dark:text-white"
                        : "text-slate-400"
                  }`}
                >
                  {s.label}
                </span>
              </div>
            );
          })}
        </div>

        {/* Error Notification */}
        {error && (
          <div className="mt-4 flex items-center gap-2 rounded-xl border border-rose-200 bg-rose-50/80 p-3 text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/30 dark:text-rose-300">
            <AlertCircle className="h-4 w-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        {/* Step 1: Identity & Credentials */}
        {currentStep === 1 && (
          <div className="mt-5 space-y-4">
            <div>
              <FieldLabel htmlFor="wizard-pool-name" className="dark:block mb-1">
                Pool Name (Slug)
              </FieldLabel>
              <Input
                id="wizard-pool-name"
                type="text"
                placeholder="e.g. arm64-ci-pool"
                value={poolName}
                onChange={(e) => setPoolName(e.target.value.toLowerCase())}
              />
              <p className="mt-1 text-[11px] text-slate-500">
                Lowercase letters, digits, and hyphens only. Used as container identifier prefix.
              </p>
              {poolName && !isNameSlugValid && (
                <p className="mt-1 text-[11px] text-rose-500 font-medium">
                  Invalid slug format: must contain only a-z, 0-9, and hyphens.
                </p>
              )}
            </div>

            <div>
              <FieldLabel htmlFor="wizard-auth-profile" className="dark:block mb-1">
                Git Authentication Profile
              </FieldLabel>
              <Select
                value={authProfileId}
                onValueChange={(v) => {
                  setAuthProfileId(v as string);
                  setSelectedTargetUrls([]);
                }}
                items={(selectableAuthProfiles ?? []).map((prof) => ({
                  value: prof.id.toString(),
                  label: `${prof.name} (${prof.authMethod})`,
                }))}
              >
                <SelectTrigger id="wizard-auth-profile">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {(selectableAuthProfiles ?? []).map((prof) => (
                      <SelectItem key={prof.id.toString()} value={prof.id.toString()}>
                        {prof.name} ({prof.authMethod})
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
              {isEdit && (
                <p className="mt-1 text-[11px] text-slate-500">
                  Profile family is locked to the pool's {deducedProvider} provider; recreate the
                  pool to change provider (docs/22 §5.3).
                </p>
              )}
              <div className="mt-2 flex items-center gap-2">
                <span className="text-[11px] text-slate-500">Deduced Provider:</span>
                <span className="inline-flex items-center rounded-md bg-blue-50 px-2 py-0.5 text-[11px] font-semibold text-blue-700 dark:bg-blue-950/50 dark:text-blue-400 capitalize">
                  {deducedProvider}
                </span>
              </div>
            </div>

            <div className="flex justify-end gap-2 pt-4 border-t border-slate-100 dark:border-slate-800">
              <Button variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <Button
                onClick={handleNextFromStep1}
                disabled={!poolName.trim() || !isNameSlugValid || !selectedAuthProfile}
              >
                <span>Continue to Scope & Targets</span>
                <ChevronRight data-icon="inline-end" />
              </Button>
            </div>
          </div>
        )}

        {/* Step 2: Scope & Target Discovery */}
        {currentStep === 2 && (
          <div className="mt-5 space-y-4">
            {isEdit && selectedTargetUrls.length > 0 && (
              <div className="rounded-xl border border-slate-200 bg-slate-50/50 p-3 dark:border-slate-800 dark:bg-slate-950/40">
                <span className="font-semibold text-slate-700 dark:text-slate-300 block mb-1.5">
                  Selected Targets ({selectedTargetUrls.length})
                </span>
                <div className="flex flex-wrap gap-1.5">
                  {selectedTargetUrls.map((url) => (
                    <Badge key={url} variant="outline" className="gap-1 py-0.5 pl-2 pr-1 font-mono">
                      <span className="max-w-48 truncate">{url}</span>
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-label={`Remove target ${url}`}
                        onClick={() => handleToggleTarget(url)}
                      >
                        <X />
                      </Button>
                    </Badge>
                  ))}
                </div>
              </div>
            )}
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 items-center">
              <div>
                <FieldLabel htmlFor="wizard-scope" className="dark:block mb-1">
                  Pool Scope
                </FieldLabel>
                <Select
                  value={scope}
                  onValueChange={(v) => setScope(v as "repo" | "org")}
                  items={[
                    { value: "repo", label: "Repositories (Multi-Repo)" },
                    { value: "org", label: "Organizations (Multi-Org)" },
                  ]}
                >
                  <SelectTrigger id="wizard-scope">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="repo">Repositories (Multi-Repo)</SelectItem>
                      <SelectItem value="org">Organizations (Multi-Org)</SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>

              <div className="flex items-center gap-2 justify-end pt-5">
                <Badge className="h-auto border-primary/30 bg-primary/10 px-2.5 py-1 text-primary font-semibold">
                  <Layers />
                  <span>
                    {selectedTargetUrls.length}{" "}
                    {scope === "repo" ? "Repositories" : "Organizations"} Selected
                  </span>
                </Badge>
              </div>
            </div>

            {/* Target Discovery Search & Action Bar */}
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between pt-2">
              <div className="relative flex-1">
                <Search className="absolute left-3 top-2.5 h-3.5 w-3.5 text-slate-400" />
                <Input
                  type="text"
                  placeholder={`Search discovered ${scope === "repo" ? "repositories" : "organizations"}...`}
                  value={targetSearch}
                  onChange={(e) => setTargetSearch(e.target.value)}
                  className="pl-8"
                />
              </div>
              <div className="flex items-center gap-2">
                {manageAccessUrl && (
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <a
                          href={manageAccessUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="inline-flex items-center gap-1 rounded-lg border border-slate-200 bg-white px-2.5 py-1 text-[11px] font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700 shadow-xs"
                        />
                      }
                    >
                      <ExternalLink className="h-3 w-3 text-slate-400" />
                      <span>Manage Access in GitHub</span>
                    </TooltipTrigger>
                    <TooltipContent>Manage repository access in GitHub</TooltipContent>
                  </Tooltip>
                )}
                <Button
                  variant="outline"
                  size="xs"
                  onClick={handleSelectAllFiltered}
                  disabled={filteredDiscoveredTargets.length === 0}
                >
                  Select All Filtered
                </Button>
                <Button
                  variant="outline"
                  size="xs"
                  onClick={handleClearSelection}
                  disabled={selectedTargetUrls.length === 0}
                >
                  Clear
                </Button>
              </div>
            </div>

            {/* Discovered Items Container */}
            <div className="max-h-64 overflow-y-auto rounded-xl border border-slate-200 bg-slate-50/50 p-2 dark:border-slate-800 dark:bg-slate-950/40 space-y-1.5">
              {isDiscovering && (
                <div className="flex flex-col items-center justify-center py-10 text-slate-400 gap-2">
                  <Loader2 className="h-6 w-6 animate-spin text-blue-600" />
                  <span>
                    Discovering accessible {scope === "repo" ? "repositories" : "organizations"}...
                  </span>
                </div>
              )}

              {!isDiscovering && discoveryError && (
                <div className="flex flex-col items-center justify-center py-8 text-center px-4">
                  <AlertCircle className="h-6 w-6 text-rose-500 mb-1" />
                  <p className="text-rose-600 dark:text-rose-400 font-semibold">
                    Failed to discover targets
                  </p>
                  <p className="text-[11px] text-slate-500 mt-1 max-w-sm">
                    {discoveryError instanceof Error
                      ? discoveryError.message
                      : "Upstream API error"}
                  </p>
                  <Button size="xs" onClick={() => refetchDiscovery()} className="mt-3">
                    Retry Discovery
                  </Button>
                </div>
              )}

              {!isDiscovering && !discoveryError && discoveredTargets.length === 0 && (
                <div className="py-8 px-4 text-center">
                  <div className="mx-auto flex h-10 w-10 items-center justify-center rounded-full bg-blue-50 dark:bg-blue-950/60 mb-2.5">
                    <FolderGit2 className="h-5 w-5 text-blue-600 dark:text-blue-400" />
                  </div>
                  <h4 className="text-xs font-semibold text-slate-900 dark:text-white">
                    {installUrl
                      ? "GitHub App Not Installed Yet"
                      : `No ${scope === "repo" ? "repositories" : "organizations"} found`}
                  </h4>
                  <p className="text-[11px] text-slate-500 dark:text-slate-400 mt-1 max-w-sm mx-auto">
                    {installUrl
                      ? "This GitHub App has not been installed on any account or organization. Install the app to grant access to repositories."
                      : `No accessible ${scope === "repo" ? "repositories" : "organizations"} were found for this auth profile.`}
                  </p>
                  {installUrl && (
                    <div className="mt-3.5">
                      <a
                        href={installUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-flex items-center gap-1.5 rounded-xl bg-blue-600 px-3.5 py-1.5 text-xs font-semibold text-white hover:bg-blue-500 shadow-xs transition-colors"
                      >
                        <ExternalLink className="h-3.5 w-3.5" />
                        <span>Install GitHub App on Your Account</span>
                      </a>
                      <p className="text-[10px] text-slate-400 dark:text-slate-500 mt-2">
                        After completing installation in GitHub, return here — your repositories
                        will appear automatically.
                      </p>
                    </div>
                  )}
                </div>
              )}

              {!isDiscovering &&
                !discoveryError &&
                discoveredTargets.length > 0 &&
                filteredDiscoveredTargets.length === 0 && (
                  <div className="py-8 text-center text-slate-400">
                    <FolderGit2 className="h-6 w-6 mx-auto mb-1 opacity-50" />
                    <span>
                      No matching {scope === "repo" ? "repositories" : "organizations"} found
                    </span>
                  </div>
                )}

              {!isDiscovering &&
                !discoveryError &&
                filteredDiscoveredTargets.map((target) => {
                  const isSelected = selectedTargetUrls.includes(target.htmlUrl);
                  return (
                    <div
                      key={target.htmlUrl}
                      onClick={() => handleToggleTarget(target.htmlUrl)}
                      className={`flex items-start gap-3 rounded-xl border p-2.5 transition-colors cursor-pointer ${
                        isSelected
                          ? "border-blue-500 bg-blue-50/50 dark:border-blue-700 dark:bg-blue-950/30"
                          : "border-slate-200 bg-white hover:border-slate-300 dark:border-slate-800 dark:bg-slate-900 dark:hover:border-slate-700"
                      }`}
                    >
                      <div className="pt-0.5 text-blue-600 dark:text-blue-400 shrink-0">
                        {isSelected ? (
                          <CheckSquare className="h-4 w-4" />
                        ) : (
                          <Square className="h-4 w-4 text-slate-400" />
                        )}
                      </div>

                      {scope === "org" ? (
                        <Building className="h-5 w-5 text-slate-400 shrink-0 mt-0.5" />
                      ) : (
                        <FolderGit2 className="h-5 w-5 text-slate-400 shrink-0 mt-0.5" />
                      )}

                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="font-semibold text-slate-900 dark:text-white truncate">
                            {target.fullName || target.name}
                          </span>
                          <span
                            className={`inline-flex items-center gap-0.5 rounded px-1.5 py-0.2 text-[10px] font-medium border ${
                              target.isPrivate
                                ? "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950/40 dark:text-amber-400 dark:border-amber-900"
                                : "bg-slate-100 text-slate-600 border-slate-200 dark:bg-slate-800 dark:text-slate-400 dark:border-slate-700"
                            }`}
                          >
                            {target.isPrivate ? (
                              <>
                                <Lock className="h-2.5 w-2.5" />
                                Private
                              </>
                            ) : (
                              <>
                                <Globe className="h-2.5 w-2.5" />
                                Public
                              </>
                            )}
                          </span>
                        </div>
                        {target.description && (
                          <p className="text-[11px] text-slate-500 dark:text-slate-400 truncate mt-0.5">
                            {target.description}
                          </p>
                        )}
                      </div>

                      <Tooltip>
                        <TooltipTrigger
                          render={
                            <a
                              href={target.htmlUrl}
                              target="_blank"
                              rel="noreferrer"
                              onClick={(e) => e.stopPropagation()}
                              aria-label="Open in upstream git provider"
                              className="text-slate-400 hover:text-blue-600 p-1 shrink-0"
                            />
                          }
                        >
                          <ExternalLink className="h-3.5 w-3.5" />
                        </TooltipTrigger>
                        <TooltipContent>Open in upstream git provider</TooltipContent>
                      </Tooltip>
                    </div>
                  );
                })}
            </div>

            <div className="flex justify-between items-center pt-4 border-t border-slate-100 dark:border-slate-800">
              <Button variant="outline" onClick={() => setCurrentStep(1)}>
                <ChevronLeft data-icon="inline-start" />
                <span>Back</span>
              </Button>
              <Button onClick={handleNextFromStep2} disabled={selectedTargetUrls.length === 0}>
                <span>Continue to Specifications</span>
                <ChevronRight data-icon="inline-end" />
              </Button>
            </div>
          </div>
        )}

        {/* Step 3: Specs & Quotas */}
        {currentStep === 3 && (
          <div className="mt-5 space-y-4">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div>
                <FieldLabel htmlFor="wizard-min-idle" className="dark:block mb-1">
                  Min Idle Warm Runners
                </FieldLabel>
                <Input
                  id="wizard-min-idle"
                  type="number"
                  min={0}
                  max={20}
                  value={minIdleRunners}
                  onChange={(e) => setMinIdleRunners(Number(e.target.value))}
                />
                <p className="mt-1 text-[11px] text-slate-500">
                  Set to 0 for scale-to-zero mode (ephemeral on-demand only).
                </p>
              </div>

              <div>
                <FieldLabel htmlFor="wizard-max-concurrency" className="dark:block mb-1">
                  Max Concurrency
                </FieldLabel>
                <Input
                  id="wizard-max-concurrency"
                  type="number"
                  min={1}
                  max={50}
                  value={maxConcurrency}
                  onChange={(e) => setMaxConcurrency(Number(e.target.value))}
                />
                <p className="mt-1 text-[11px] text-slate-500">
                  Total maximum simultaneous runner containers allowed across all targets.
                </p>
              </div>

              <div className="sm:col-span-2">
                <FieldLabel htmlFor="wizard-labels" className="dark:block mb-1">
                  Runner Labels
                </FieldLabel>
                <Input
                  id="wizard-labels"
                  type="text"
                  value={labels}
                  onChange={(e) => setCustomLabels(e.target.value)}
                />
                <div className="mt-1 flex items-center justify-between text-[11px] text-slate-500">
                  <span>Comma-separated list matched in workflow runs.</span>
                  {customLabels !== null && (
                    <Button variant="link" size="xs" onClick={() => setCustomLabels(null)}>
                      Reset to suggested ({suggestedLabels})
                    </Button>
                  )}
                </div>
              </div>

              <div>
                <FieldLabel htmlFor="wizard-runner-image" className="dark:block mb-1">
                  Runner Image
                </FieldLabel>
                <Input
                  id="wizard-runner-image"
                  type="text"
                  value={runnerImage}
                  onChange={(e) => setRunnerImage(e.target.value)}
                />
              </div>

              <div>
                <FieldLabel htmlFor="wizard-cpu" className="dark:block mb-1">
                  CPU Limit
                </FieldLabel>
                <Input
                  id="wizard-cpu"
                  type="text"
                  value={cpuLimit}
                  onChange={(e) => setCpuLimit(e.target.value)}
                />
              </div>

              <div>
                <FieldLabel htmlFor="wizard-mem" className="dark:block mb-1">
                  Memory Limit
                </FieldLabel>
                <Input
                  id="wizard-mem"
                  type="text"
                  value={memoryLimit}
                  onChange={(e) => setMemoryLimit(e.target.value)}
                />
              </div>
            </div>

            {/* Docker Socket Privilege */}
            <div className="pt-2 border-t border-slate-100 dark:border-slate-800">
              <FieldLabel className="flex items-center gap-2">
                <Checkbox
                  checked={isDockerLocked ? true : allowDocker}
                  disabled={isDockerLocked}
                  onCheckedChange={(v) => setAllowDocker(v === true)}
                />
                <span>Enable Docker-in-Docker socket access</span>
              </FieldLabel>
              {isDockerLocked && (
                <p className="mt-1 text-[11px] text-amber-600 dark:text-amber-400">
                  Mandatory for {deducedProvider} pools (runner daemon communicates via Docker
                  daemon).
                </p>
              )}
            </div>

            {/* Demand Polling Fallback (docs/24 §5.9) */}
            <div className="pt-2 border-t border-slate-100 dark:border-slate-800">
              <FieldLabel className="flex items-center gap-2">
                <Checkbox
                  checked={
                    deducedProvider === "github" ? pollFallback : deducedProvider === "forgejo"
                  }
                  disabled={deducedProvider !== "github"}
                  onCheckedChange={(v) => setPollFallback(v === true)}
                />
                <span>Scale without webhooks (poll for queued jobs)</span>
              </FieldLabel>
              <p className="mt-1 text-[11px] text-slate-500 dark:text-slate-400">
                {deducedProvider === "gitea" &&
                  "Not available for Gitea pools: Gitea has no repo-scoped queued-jobs API."}
                {deducedProvider === "forgejo" &&
                  "Always on for Forgejo pools: Forgejo has no workflow_job webhooks, so the supervisor polls for waiting tasks."}
                {deducedProvider === "github" &&
                  "Polls connected repositories for queued jobs so runners scale on hosts without inbound webhooks. Webhooks remain the fast path when available."}
              </p>
            </div>

            {/* Renovate Bot Section */}
            <div className="rounded-xl border border-slate-200 bg-slate-50/50 p-3 dark:border-slate-800 dark:bg-slate-950/40 space-y-3">
              <FieldLabel className="flex items-center gap-2 dark:text-white">
                <Checkbox
                  checked={renovateEnabled}
                  onCheckedChange={(v) => setRenovateEnabled(v === true)}
                />
                <span className="flex items-center gap-1.5">
                  <Bot className="h-4 w-4 text-blue-600 dark:text-blue-400" />
                  Enable Automated Renovate Dependency Scans
                </span>
              </FieldLabel>

              {renovateEnabled && (
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 pt-2 border-t border-slate-200 dark:border-slate-800">
                  <div>
                    <FieldLabel className="dark:block mb-1">Cron Schedule</FieldLabel>
                    <Input
                      type="text"
                      value={renovateCron}
                      onChange={(e) => setRenovateCron(e.target.value)}
                      placeholder="0 2 * * *"
                    />
                  </div>
                  <div>
                    <FieldLabel className="dark:block mb-1">Renovate Image</FieldLabel>
                    <Input
                      type="text"
                      value={renovateImage}
                      onChange={(e) => setRenovateImage(e.target.value)}
                      placeholder="renovate/renovate:latest"
                    />
                  </div>
                </div>
              )}
            </div>

            <div className="flex justify-between items-center pt-4 border-t border-slate-100 dark:border-slate-800">
              <Button variant="outline" onClick={() => setCurrentStep(2)}>
                <ChevronLeft data-icon="inline-start" />
                <span>Back</span>
              </Button>
              <Button onClick={handleNextFromStep3}>
                <span>Review & Confirm</span>
                <ChevronRight data-icon="inline-end" />
              </Button>
            </div>
          </div>
        )}

        {/* Step 4: Review & Confirmation */}
        {currentStep === 4 && (
          <form onSubmit={handleSubmitPool} className="mt-5 space-y-4">
            {isEdit && changes.length > 0 && (
              <div className="rounded-xl border border-blue-200 bg-blue-50/60 p-4 dark:border-blue-900/50 dark:bg-blue-950/30 space-y-2">
                <span className="text-sm font-bold text-slate-900 dark:text-white">
                  Changed Fields ({changes.length})
                </span>
                <div className="max-h-44 overflow-y-auto space-y-1">
                  {changes.map((c) => (
                    <div
                      key={c.field}
                      className="flex flex-wrap items-baseline gap-x-2 rounded-lg bg-white px-2.5 py-1.5 text-[11px] dark:bg-slate-900 border border-slate-100 dark:border-slate-800"
                    >
                      <span className="font-semibold text-slate-700 dark:text-slate-300 min-w-28">
                        {c.field}:
                      </span>
                      <span className="font-mono text-rose-600 dark:text-rose-400 break-all line-through">
                        {c.before || "—"}
                      </span>
                      <ChevronRight className="h-3 w-3 shrink-0 self-center text-slate-400" />
                      <span className="font-mono text-emerald-700 dark:text-emerald-400 break-all">
                        {c.after || "—"}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}
            {isEdit && changes.length === 0 && (
              <div className="rounded-xl border border-slate-200 bg-slate-50/60 p-4 text-[11px] text-slate-500 dark:border-slate-800 dark:bg-slate-950/40">
                No changes yet — modify any field to see the diff before saving.
              </div>
            )}
            {isEdit && identityChanged && !renamed && (
              <div className="flex items-center gap-2 rounded-xl border border-amber-200 bg-amber-50/80 p-3 text-amber-700 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-400">
                <AlertCircle className="h-4 w-4 shrink-0" />
                <span>
                  {pool?.idleRunners ?? 0} idle runner{pool?.idleRunners === 1 ? "" : "s"} will be
                  recycled to apply the new configuration; running jobs are not affected.
                </span>
              </div>
            )}
            {isEdit && renamed && (
              <div className="flex items-center gap-2 rounded-xl border border-blue-200 bg-blue-50/80 p-3 text-blue-700 dark:border-blue-900/50 dark:bg-blue-950/30 dark:text-blue-400">
                <Info className="h-4 w-4 shrink-0" />
                <span>
                  Renaming only changes how the pool is displayed — runners are unaffected and keep
                  their original pool label until they recycle naturally.
                </span>
              </div>
            )}
            <div className="rounded-xl border border-slate-200 bg-slate-50/50 p-4 dark:border-slate-800 dark:bg-slate-950/40 space-y-4">
              <div className="flex items-center justify-between border-b border-slate-200 pb-3 dark:border-slate-800">
                <div>
                  <h4 className="text-sm font-bold text-slate-900 dark:text-white">{poolName}</h4>
                  <p className="text-[11px] text-slate-500">
                    Provider Profile: {selectedAuthProfile?.name} ({selectedAuthProfile?.authMethod}
                    )
                  </p>
                </div>
                <span className="inline-flex items-center rounded-md bg-blue-50 px-2.5 py-1 text-xs font-semibold text-blue-700 dark:bg-blue-950/60 dark:text-blue-300 capitalize">
                  {deducedProvider} ({scope})
                </span>
              </div>

              {/* Targets Summary */}
              <div>
                <span className="font-semibold text-slate-700 dark:text-slate-300 block mb-1.5">
                  Associated Targets ({selectedTargetUrls.length}):
                </span>
                <div className="max-h-32 overflow-y-auto space-y-1 rounded-lg border border-slate-200 bg-white p-2 dark:border-slate-800 dark:bg-slate-900">
                  {selectedTargetUrls.map((url) => (
                    <div key={url} className="flex items-center justify-between text-[11px]">
                      <span className="font-mono text-slate-800 dark:text-slate-200 truncate">
                        {url}
                      </span>
                      <a
                        href={url}
                        target="_blank"
                        rel="noreferrer"
                        className="text-slate-400 hover:text-blue-500 ml-2 shrink-0"
                      >
                        <ExternalLink className="h-3 w-3" />
                      </a>
                    </div>
                  ))}
                </div>
              </div>

              {/* Specs Grid */}
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4 border-t border-slate-200 pt-3 dark:border-slate-800">
                <div>
                  <span className="text-slate-400 block">Idle Warm</span>
                  <span className="font-bold text-slate-900 dark:text-white">{minIdleRunners}</span>
                </div>
                <div>
                  <span className="text-slate-400 block">Max Limit</span>
                  <span className="font-bold text-slate-900 dark:text-white">{maxConcurrency}</span>
                </div>
                <div>
                  <span className="text-slate-400 block">CPU / RAM</span>
                  <span className="font-bold text-slate-900 dark:text-white">
                    {cpuLimit} / {memoryLimit}
                  </span>
                </div>
                <div>
                  <span className="text-slate-400 block">Docker Access</span>
                  <span className="font-bold text-slate-900 dark:text-white">
                    {isDockerLocked || allowDocker ? "Enabled" : "Disabled"}
                  </span>
                </div>
              </div>

              {/* Labels & Renovate */}
              <div className="border-t border-slate-200 pt-3 dark:border-slate-800 flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-1.5">
                  <span className="text-slate-400">Labels:</span>
                  <span className="font-mono text-slate-700 dark:text-slate-300">{labels}</span>
                </div>
                {renovateEnabled && (
                  <span className="inline-flex items-center gap-1 text-[11px] text-blue-600 dark:text-blue-400 font-medium">
                    <Bot className="h-3.5 w-3.5" />
                    Renovate Scheduled ({renovateCron})
                  </span>
                )}
              </div>
            </div>

            <div className="flex justify-between items-center pt-4 border-t border-slate-100 dark:border-slate-800">
              <Button variant="outline" onClick={() => setCurrentStep(3)}>
                <ChevronLeft data-icon="inline-start" />
                <span>Back</span>
              </Button>
              <Button
                type="submit"
                disabled={createPoolMutation.isPending || updatePoolMutation.isPending}
              >
                {isEdit ? (
                  updatePoolMutation.isPending ? (
                    <>
                      <Loader2 data-icon="inline-start" className="animate-spin" />
                      <span>Saving Changes...</span>
                    </>
                  ) : (
                    <>
                      <Pencil data-icon="inline-start" />
                      <span>Save Changes</span>
                    </>
                  )
                ) : createPoolMutation.isPending ? (
                  <>
                    <Loader2 data-icon="inline-start" className="animate-spin" />
                    <span>Creating Runner Pool...</span>
                  </>
                ) : (
                  <span>Create Runner Pool</span>
                )}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
