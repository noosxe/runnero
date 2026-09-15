import { useState, useMemo, type FormEvent } from "react";
import { cn } from "cn";
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
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
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Badge } from "@/components/ui/badge";
import { create } from "@bufbuild/protobuf";
import {
  CreatePoolRequestSchema,
  PoolSchema,
  UpdatePoolRequestSchema,
  type Pool,
} from "../../gen/api_pb";
import { useCreatePool, useUpdatePool, useDiscoverTargets } from "../../lib/api/query-hooks";
import { useStore } from "@tanstack/react-form";
import {
  FormError,
  TextField,
  groupByField,
  useAppForm,
  validateMessage,
  violationsFromConnectError,
} from "../../lib/forms";
import { getSuggestedRunnerLabels } from "../../lib/utils/labels";
import { authMethodLabel } from "../../lib/utils/auth-methods";
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
  Lock,
  Globe,
  Building,
  FolderGit2,
  Layers,
  Bot,
  Pencil,
} from "lucide-react";

interface WizardFormValues {
  name: string;
  minIdleRunners: string;
  maxConcurrency: string;
  labels: string;
  runnerImage: string;
  cpuLimit: string;
  memoryLimit: string;
  swapCustom: string;
  pidsCustom: string;
  renovateCron: string;
  renovateImage: string;
}

/** Parse a numeric text input; "" or garbage → 0 (annotations decide validity). */
function toIntOrZero(raw: string): number {
  const parsed = Number.parseInt(raw.trim(), 10);
  return Number.isFinite(parsed) ? parsed : 0;
}

// Free-form form key → wizard step (gating buckets, docs/30 §5.4).
const FIELD_STEP: Record<keyof WizardFormValues, 1 | 2 | 3 | 4> = {
  name: 1,
  minIdleRunners: 3,
  maxConcurrency: 3,
  labels: 3,
  runnerImage: 3,
  cpuLimit: 3,
  memoryLimit: 3,
  swapCustom: 3,
  pidsCustom: 3,
  renovateCron: 4,
  renovateImage: 4,
};

// Form key → DOM id (focus-first-invalid).
const FIELD_DOM_ID: Record<keyof WizardFormValues, string> = {
  name: "wizard-pool-name",
  minIdleRunners: "wizard-min-idle",
  maxConcurrency: "wizard-max-concurrency",
  labels: "wizard-labels",
  runnerImage: "wizard-runner-image",
  cpuLimit: "wizard-cpu",
  memoryLimit: "wizard-mem",
  swapCustom: "wizard-swap-custom",
  pidsCustom: "wizard-pids-custom",
  renovateCron: "wizard-renovate-cron",
  renovateImage: "wizard-renovate-image",
};

// Proto field name (last path element of a violation) → form key. Fields with
// no wizard input (selections, targets) fall into their step bucket instead.
const PROTO_FIELD_TO_FORM: Record<string, keyof WizardFormValues> = {
  name: "name",
  min_idle_runners: "minIdleRunners",
  max_concurrency: "maxConcurrency",
  labels: "labels",
  runner_image: "runnerImage",
  cpu_limit: "cpuLimit",
  memory_limit: "memoryLimit",
  memory_swap_limit: "swapCustom",
  pids_limit: "pidsCustom",
  cron_schedule: "renovateCron",
  image: "renovateImage",
};

const PROTO_FIELD_STEP: Record<string, 1 | 2 | 3 | 4> = {
  // message-level CEL path element
  pool: 3,
  repository_url: 2,
  target_urls: 2,
  auth_profile_id: 1,
  allow_docker: 3,
  poll_fallback: 3,
};

const MESSAGE_LEVEL_STEP: Record<string, 1 | 2 | 3 | 4> = {
  "pool.min_idle.max_concurrency": 3,
  "pool.update.id_required": 4,
};
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

  // Free-form inputs live on the app form (docs/30 §5.4). Rules are NOT
  // re-implemented here: blur/step/submit handlers build the exact request
  // message the wizard submits and run protovalidate on it (class A — the
  // same engine and annotations as the server), plus the two UI-state rules
  // (class C, docs/30 §5.1) for the custom swap/pids modes. Selections and
  // toggles stay local state: constrained controls whose wire fields carry
  // no format rules.
  const form = useAppForm({
    defaultValues: {
      name: pool?.name ?? "",
      minIdleRunners: String(pool?.minIdleRunners ?? 1),
      maxConcurrency: String(pool?.maxConcurrency ?? 5),
      labels: pool ? pool.labels.join(",") : suggestedLabels,
      runnerImage: pool?.runnerImage || "ghcr.io/noosxe/runnero:latest",
      cpuLimit: pool?.cpuLimit || "2.0",
      memoryLimit: pool?.memoryLimit || "4GB",
      swapCustom:
        pool?.memorySwapLimit && pool.memorySwapLimit !== "-1" ? pool.memorySwapLimit : "",
      pidsCustom:
        pool?.pidsLimit && ![4096, 1024].includes(pool.pidsLimit) ? String(pool.pidsLimit) : "",
      renovateCron: pool?.renovate?.cronSchedule || "0 2 * * *",
      renovateImage: pool?.renovate?.image || "renovate/renovate:latest",
    },
  });
  // Reactive subscription: the wizard re-renders on value changes (controlled
  // shims + gates read fresh values), matching the useState behavior it
  // replaced.
  const formValues = useStore(form.store, (state) => state.values);
  // Re-render on meta writes too: blur-time evaluations set field errors via
  // setFieldMeta without touching values.
  useStore(form.store, (state) => state.fieldMeta);

  // Read-shims: pre-existing JSX keeps compiling while state moved to the form.
  const poolName = formValues.name;
  const minIdleRunners = toIntOrZero(formValues.minIdleRunners);
  const maxConcurrency = toIntOrZero(formValues.maxConcurrency);
  const runnerImage = formValues.runnerImage;
  const cpuLimit = formValues.cpuLimit;
  const memoryLimit = formValues.memoryLimit;
  const swapCustom = formValues.swapCustom;
  const pidsCustom = formValues.pidsCustom;
  const renovateCron = formValues.renovateCron;
  const renovateImage = formValues.renovateImage;

  // Step 1: Identity & Credentials (edit mode prefills from the pool, docs/22 §7.2)
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
  const labels = formValues.labels;
  const [allowDocker, setAllowDocker] = useState(pool?.allowDocker ?? true);
  // Demand polling fallback (docs/24 §5.9): GitHub pools only; Forgejo polls
  // natively and Gitea has no repo-scoped queued-jobs API.
  const [pollFallback, setPollFallback] = useState(pool?.pollFallback ?? false);
  // Memory swap mode (RUN-147): "match" hardens to swap = memory (no extra
  // swap — the shipped default), "default2x" keeps the Docker daemon default,
  // "unlimited" passes -1, "custom" carries an explicit total allowance.
  const [swapMode, setSwapMode] = useState<"match" | "default2x" | "unlimited" | "custom">(
    pool
      ? pool.memorySwapLimit === "-1"
        ? "unlimited"
        : pool.memorySwapLimit
          ? "custom"
          : "default2x"
      : "match",
  );
  // PIDs mode (RUN-148): "default" = 4096 (shipped default), "strict" =
  // 1024, "unlimited" = 0 (explicit opt-out), "custom" carries an explicit
  // process ceiling.
  const [pidsMode, setPidsMode] = useState<"default" | "strict" | "unlimited" | "custom">(() => {
    if (!pool) return "default";
    if (pool.pidsLimit == null) return "default"; // unset wire value ≠ a picked mode
    if (pool.pidsLimit === 0) return "unlimited";
    if (pool.pidsLimit === 4096) return "default";
    if (pool.pidsLimit === 1024) return "strict";
    return "custom";
  });
  // Lifetime is not wizard-editable; edit mode preserves the stored value
  // instead of silently resetting it to the create-mode default (docs/22 §7.2).
  const [maxRunnerLifetimeSeconds] = useState(pool?.maxRunnerLifetimeSeconds ?? 7200);

  // Renovate Config
  const [renovateEnabled, setRenovateEnabled] = useState(pool?.renovate?.enabled ?? false);

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

  // Slug validation lives in proto now (string.pattern on Pool.name, class A):
  // the violation map carries it; no client-side re-implementation (docs/30).

  // Edit-mode change detection (docs/22 §5.2, §7.2): drives the review-step
  // changed-fields diff and the runner-impact banners.
  const effectiveRenovateImage = renovateImage.trim() || "renovate/renovate:latest";
  const effectiveRenovateCron = renovateCron.trim() || "0 2 * * *";
  const normalizeSet = (values: string[]) =>
    Array.from(new Set(values.map((v) => v.trim()).filter(Boolean))).sort();

  const describeSwap = (mode: typeof swapMode, custom: string, mem: string) => {
    const m = mem.trim() || "4GB";
    switch (mode) {
      case "match":
        return `${m} + no swap`;
      case "default2x":
        return `${m} + ${m} swap (2x)`;
      case "unlimited":
        return `${m} + unlimited swap`;
      case "custom":
        return `${m} + ${custom.trim() || "?"} swap`;
    }
  };

  const describePids = (mode: typeof pidsMode, custom: string) => {
    switch (mode) {
      case "default":
        return "4096 (default)";
      case "strict":
        return "1024 (strict)";
      case "unlimited":
        return "unlimited";
      case "custom":
        return custom.trim() || "?";
    }
  };

  const memorySwapLimit = (() => {
    switch (swapMode) {
      case "match":
        return memoryLimit.trim() || "4GB";
      case "unlimited":
        return "-1";
      case "custom":
        return swapCustom.trim();
      case "default2x":
        return "";
    }
  })();

  const pidsLimit = (() => {
    switch (pidsMode) {
      case "default":
        return 4096;
      case "strict":
        return 1024;
      case "unlimited":
        return 0;
      case "custom": {
        const n = Number.parseInt(pidsCustom, 10);
        return Number.isFinite(n) && n > 0 ? n : 0;
      }
    }
  })();

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
    add(
      "Memory Swap",
      describeSwap(
        pool.memorySwapLimit === "-1" ? "unlimited" : pool.memorySwapLimit ? "custom" : "default2x",
        pool.memorySwapLimit && pool.memorySwapLimit !== "-1" ? pool.memorySwapLimit : "",
        pool.memoryLimit || "4GB",
      ),
      describeSwap(swapMode, swapCustom, memoryLimit),
      true,
    );
    add(
      "PIDs Limit",
      describePids(
        !pool || pool.pidsLimit == null
          ? "default"
          : pool.pidsLimit === 0
            ? "unlimited"
            : pool.pidsLimit === 4096
              ? "default"
              : pool.pidsLimit === 1024
                ? "strict"
                : "custom",
        pool?.pidsLimit && ![4096, 1024].includes(pool.pidsLimit) ? String(pool.pidsLimit) : "",
      ),
      describePids(pidsMode, pidsCustom),
      true,
    );
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
    memorySwapLimit,
    swapMode,
    swapCustom,
    pidsMode,
    pidsCustom,
    pidsLimit,
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

  // Build the exact wire message the wizard submits (docs/30 §5.4): blur,
  // step-change, and submit evaluation all run protovalidate on THIS message,
  // so the preview can never drift from what goes over the wire.
  const buildPoolPayload = (): Pool | null => {
    const editingPool = isEdit ? pool : undefined;
    if (isEdit && !editingPool) return null;
    if (!selectedAuthProfile) return null;
    const effectiveLabels = labels.trim() || suggestedLabels;
    const parsedLabels = effectiveLabels
      .split(",")
      .map((l) => l.trim())
      .filter(Boolean);

    return create(PoolSchema, {
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
      memorySwapLimit,
      pidsLimit,
      maxRunnerLifetimeSeconds,
      targetUrls: selectedTargetUrls,
    });
  };

  /**
   * The shared violation map (docs/30 §5.4): one evaluation produces BOTH the
   * inline per-field messages and the per-step gating buckets — gate and
   * message read the same map and can never disagree. Class A comes from
   * protovalidate on the wire message; class C (custom mode ⇒ value) is the
   * UI-state rule layer; selection guards stay step-local.
   */
  const runEvaluation = (): {
    fieldErrors: Partial<Record<keyof WizardFormValues, string[]>>;
    stepIssues: Record<1 | 2 | 3 | 4, string[]>;
  } => {
    const fieldErrors: Partial<Record<keyof WizardFormValues, string[]>> = {};
    const stepIssues: Record<1 | 2 | 3 | 4, string[]> = { 1: [], 2: [], 3: [], 4: [] };

    // Class C (docs/30 §5.1): swap/pids mode is client-only state — the wire
    // meaning of "" stays valid (RUN-147), so the custom-mode pairing is a
    // form rule, never a data-format rule.
    if (swapMode === "custom" && !formValues.swapCustom.trim()) {
      (fieldErrors.swapCustom ??= []).push("Custom swap mode requires a memory string (e.g. 8GB).");
    }
    if (pidsMode === "custom" && !formValues.pidsCustom.trim()) {
      (fieldErrors.pidsCustom ??= []).push(
        "Custom PIDs mode requires a process ceiling (e.g. 512).",
      );
    }

    const poolPayload = buildPoolPayload();
    if (poolPayload) {
      const schema = isEdit ? UpdatePoolRequestSchema : CreatePoolRequestSchema;
      const violations = validateMessage(schema, create(schema, { pool: poolPayload }));
      const { byField, messageLevel } = groupByField(violations);
      for (const [protoField, messages] of byField) {
        const formKey = PROTO_FIELD_TO_FORM[protoField];
        if (formKey) {
          (fieldErrors[formKey] ??= []).push(...messages);
          continue;
        }
        const step = PROTO_FIELD_STEP[protoField] ?? 4;
        stepIssues[step].push(...messages);
      }
      for (const violation of messageLevel) {
        const step = MESSAGE_LEVEL_STEP[violation.ruleId] ?? 4;
        stepIssues[step].push(violation.message);
      }
    }

    // Selection guards (not wire rules — no annotations on these flows).
    if (!selectedAuthProfile) {
      stepIssues[1].push("Please select a Git Auth Profile");
    }
    if (selectedTargetUrls.length === 0) {
      stepIssues[2].push(
        `Please select at least one ${scope === "repo" ? "repository" : "organization"}`,
      );
    }

    // Project field errors into their step buckets so gating uses one map.
    for (const [formKey, messages] of Object.entries(fieldErrors)) {
      if (messages && messages.length > 0) {
        stepIssues[FIELD_STEP[formKey as keyof WizardFormValues]].push(...messages);
      }
    }
    applyFieldErrors(fieldErrors);
    return { fieldErrors, stepIssues };
  };

  const applyFieldErrors = (fieldErrors: Partial<Record<keyof WizardFormValues, string[]>>) => {
    for (const key of Object.keys(FIELD_DOM_ID)) {
      const formKey = key as keyof WizardFormValues;
      const messages = fieldErrors[formKey] ?? [];
      form.setFieldMeta(formKey, (prev) => ({
        ...prev,
        // v1.33 derives meta.errors from errorMap entries — write there.
        errorMap: { ...(prev?.errorMap ?? {}), onBlur: messages[0] },
      }));
    }
  };

  const focusFirstInvalid = (fieldErrors: Partial<Record<keyof WizardFormValues, string[]>>) => {
    for (const key of Object.keys(FIELD_DOM_ID)) {
      const formKey = key as keyof WizardFormValues;
      if ((fieldErrors[formKey] ?? []).length > 0) {
        document.getElementById(FIELD_DOM_ID[formKey])?.focus();
        return;
      }
    }
  };

  // Step Navigation Handlers: advance only when the shared violation map is
  // clean for this step; otherwise surface the first issue and focus the
  // offending input — gate and messages come from the same evaluation.
  const handleAdvanceFrom = (step: 1 | 2 | 3) => {
    setError(null);
    const { fieldErrors, stepIssues } = runEvaluation();
    if (stepIssues[step].length > 0) {
      setError(stepIssues[step][0]);
      focusFirstInvalid(fieldErrors);
      return;
    }
    setCurrentStep((step + 1) as 1 | 2 | 3 | 4);
  };

  const handleNextFromStep1 = () => handleAdvanceFrom(1);
  const handleNextFromStep2 = () => handleAdvanceFrom(2);
  const handleNextFromStep3 = () => handleAdvanceFrom(3);

  const handleSubmitPool = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    // Final gate: the same shared violation map the fields and steps read.
    const { fieldErrors, stepIssues } = runEvaluation();
    applyFieldErrors(fieldErrors);
    const allIssues = ([1, 2, 3, 4] as const).flatMap((step) => stepIssues[step]);
    if (allIssues.length > 0) {
      setError(allIssues[0]);
      focusFirstInvalid(fieldErrors);
      return;
    }

    const poolPayload = buildPoolPayload();
    if (!poolPayload) return;

    try {
      if (isEdit && pool) {
        await updatePoolMutation.mutateAsync({ pool: poolPayload });
      } else {
        await createPoolMutation.mutateAsync({ pool: poolPayload });
      }
      onClose();
    } catch (err: unknown) {
      // Server is the authority (docs/30 §5.4): typed buf.validate.Violations
      // details re-enter the SAME inline mapping; anything unmapped keeps the
      // banner fallback.
      const violations = violationsFromConnectError(err);
      if (!violations) {
        setError(
          err instanceof Error
            ? err.message
            : isEdit
              ? "Failed to update runner pool"
              : "Failed to create runner pool",
        );
        return;
      }
      const { byField, messageLevel } = groupByField(violations);
      const serverFieldErrors: Partial<Record<keyof WizardFormValues, string[]>> = {};
      const banner: string[] = messageLevel.map((violation) => violation.message);
      for (const [protoField, messages] of byField) {
        const formKey = PROTO_FIELD_TO_FORM[protoField];
        if (formKey) {
          serverFieldErrors[formKey] = messages;
        } else {
          banner.push(...messages);
        }
      }
      applyFieldErrors(serverFieldErrors);
      // Mapped messages also show in the banner: their field may live on a
      // step the user is not on (e.g. duplicate name → step 1) — the banner
      // guarantees visibility.
      setError(
        banner.length > 0 || Object.keys(serverFieldErrors).length > 0
          ? [banner, ...Object.values(serverFieldErrors)].flat().join("; ")
          : isEdit
            ? "Failed to update runner pool"
            : "Failed to create runner pool",
      );
      focusFirstInvalid(serverFieldErrors);
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
        <div className="mt-4 flex items-center justify-between border-b border-border/60 pb-4">
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
                  className={cn(
                    "flex size-6 items-center justify-center rounded-full font-bold transition-colors",
                    isCompleted
                      ? "bg-success text-success-foreground"
                      : isActive
                        ? "bg-primary text-primary-foreground"
                        : "bg-muted text-muted-foreground",
                  )}
                >
                  {isCompleted ? <Check className="size-3.5" /> : s.step}
                </div>
                <span
                  className={cn(
                    "font-semibold hidden sm:inline",
                    isActive
                      ? "text-primary"
                      : isCompleted
                        ? "text-foreground"
                        : "text-muted-foreground",
                  )}
                >
                  {s.label}
                </span>
              </div>
            );
          })}
        </div>

        {/* Error Notification */}
        {error && (
          <div className="mt-4 flex items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 p-3 text-destructive">
            <AlertCircle className="size-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        {/* Step 1: Identity & Credentials */}
        {currentStep === 1 && (
          <div className="mt-5 flex flex-col gap-4">
            <FieldGroup className="gap-4">
              <form.AppField name="name">
                {() => (
                  <TextField
                    label="Pool Name (Slug)"
                    id="wizard-pool-name"
                    placeholder="e.g. arm64-ci-pool"
                    description="Lowercase letters, digits, and hyphens only. Used as container identifier prefix."
                    onBlurExtra={runEvaluation}
                  />
                )}
              </form.AppField>

              <Field>
                <FieldLabel htmlFor="wizard-auth-profile">Git Authentication Profile</FieldLabel>
                <Select
                  value={authProfileId}
                  onValueChange={(v) => {
                    setAuthProfileId(v as string);
                    setSelectedTargetUrls([]);
                  }}
                  items={(selectableAuthProfiles ?? []).map((prof) => ({
                    value: prof.id.toString(),
                    label: `${prof.name} (${authMethodLabel(prof.authMethod)})`,
                  }))}
                >
                  <SelectTrigger id="wizard-auth-profile">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      {(selectableAuthProfiles ?? []).map((prof) => (
                        <SelectItem key={prof.id.toString()} value={prof.id.toString()}>
                          {prof.name} ({authMethodLabel(prof.authMethod)})
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                {isEdit && (
                  <FieldDescription className="text-[11px]">
                    Profile family is locked to the pool's {deducedProvider} provider; recreate the
                    pool to change provider (docs/22 §5.3).
                  </FieldDescription>
                )}
                <div className="mt-2 flex items-center gap-2">
                  <span className="text-[11px] text-muted-foreground">Deduced Provider:</span>
                  <span className="inline-flex items-center rounded-md bg-primary/10 px-2 py-0.5 text-[11px] font-semibold text-primary capitalize">
                    {deducedProvider}
                  </span>
                </div>
              </Field>
            </FieldGroup>
            <div className="flex justify-end gap-2 pt-4 border-t border-border/60 ">
              <Button variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <Button
                onClick={handleNextFromStep1}
                disabled={!poolName.trim() || !selectedAuthProfile}
              >
                <span>Continue to Scope & Targets</span>
                <ChevronRight data-icon="inline-end" />
              </Button>
            </div>
          </div>
        )}

        {/* Step 2: Scope & Target Discovery */}
        {currentStep === 2 && (
          <div className="mt-5 flex flex-col gap-4">
            <FieldGroup className="gap-4">
              {isEdit && selectedTargetUrls.length > 0 && (
                <div className="rounded-xl border border-border bg-muted/50 p-3 ">
                  <span className="font-semibold text-foreground block mb-1.5">
                    Selected Targets ({selectedTargetUrls.length})
                  </span>
                  <div className="flex flex-wrap gap-1.5">
                    {selectedTargetUrls.map((url) => (
                      <Badge
                        key={url}
                        variant="outline"
                        className="gap-1 py-0.5 pl-2 pr-1 font-mono"
                      >
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
                <Field>
                  <FieldLabel htmlFor="wizard-scope">Pool Scope</FieldLabel>
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
                </Field>

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
                <InputGroup className="flex-1">
                  <InputGroupAddon align="inline-start">
                    <Search />
                  </InputGroupAddon>
                  <InputGroupInput
                    type="text"
                    placeholder={`Search discovered ${scope === "repo" ? "repositories" : "organizations"}...`}
                    value={targetSearch}
                    onChange={(e) => setTargetSearch(e.target.value)}
                  />
                </InputGroup>
                <div className="flex items-center gap-2">
                  {manageAccessUrl && (
                    <Tooltip>
                      <TooltipTrigger
                        render={
                          <a
                            href={manageAccessUrl}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-semibold text-foreground hover:bg-muted/50 shadow-xs"
                          />
                        }
                      >
                        <ExternalLink className="h-3 w-3 text-muted-foreground" />
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
              <div className="max-h-64 overflow-y-auto rounded-xl border border-border bg-muted/50 p-2 flex flex-col gap-1.5">
                {isDiscovering && (
                  <div className="flex flex-col gap-1.5 p-2">
                    {Array.from({ length: 4 }).map((_, i) => (
                      <Skeleton key={i} className="h-8 w-full" />
                    ))}
                  </div>
                )}

                {!isDiscovering && discoveryError && (
                  <div className="flex flex-col items-center justify-center py-8 text-center px-4">
                    <AlertCircle className="size-6 text-destructive mb-1" />
                    <p className="text-destructive font-semibold">Failed to discover targets</p>
                    <p className="text-[11px] text-muted-foreground mt-1 max-w-sm">
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
                  <Empty className="py-8 px-4">
                    <EmptyHeader>
                      <EmptyMedia variant="icon">
                        <FolderGit2 />
                      </EmptyMedia>
                      <EmptyTitle>
                        {installUrl
                          ? "GitHub App Not Installed Yet"
                          : `No ${scope === "repo" ? "repositories" : "organizations"} found`}
                      </EmptyTitle>
                      <EmptyDescription>
                        {installUrl
                          ? "This GitHub App has not been installed on any account or organization. Install the app to grant access to repositories."
                          : `No accessible ${scope === "repo" ? "repositories" : "organizations"} were found for this auth profile.`}
                      </EmptyDescription>
                    </EmptyHeader>
                    {installUrl && (
                      <EmptyContent>
                        <a
                          href={installUrl}
                          className="inline-flex items-center gap-1.5 rounded-xl bg-primary px-3.5 py-1.5 text-xs font-semibold text-primary-foreground shadow-xs transition-colors hover:bg-primary/90"
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          <ExternalLink className="size-3.5" />
                          <span>Install GitHub App on Your Account</span>
                        </a>
                        <p className="text-[10px] text-muted-foreground">
                          After completing installation in GitHub, return here — your repositories
                          will appear automatically.
                        </p>
                      </EmptyContent>
                    )}
                  </Empty>
                )}

                {!isDiscovering &&
                  !discoveryError &&
                  discoveredTargets.length > 0 &&
                  filteredDiscoveredTargets.length === 0 && (
                    <Empty className="py-6">
                      <EmptyHeader>
                        <EmptyMedia variant="icon" className="opacity-50">
                          <FolderGit2 className="size-6" />
                        </EmptyMedia>
                        <EmptyTitle className="text-sm text-muted-foreground">
                          No matching {scope === "repo" ? "repositories" : "organizations"} found
                        </EmptyTitle>
                      </EmptyHeader>
                    </Empty>
                  )}

                {!isDiscovering &&
                  !discoveryError &&
                  filteredDiscoveredTargets.map((target) => {
                    const isSelected = selectedTargetUrls.includes(target.htmlUrl);
                    return (
                      <div
                        key={target.htmlUrl}
                        onClick={() => handleToggleTarget(target.htmlUrl)}
                        className={cn(
                          "flex items-start gap-3 rounded-xl border p-2.5 transition-colors cursor-pointer",
                          isSelected
                            ? "border-primary/50 bg-primary/10"
                            : "border-border bg-card hover:border-border bg-muted",
                        )}
                      >
                        <div className="pt-0.5 text-primary shrink-0">
                          {isSelected ? (
                            <CheckSquare className="size-4" />
                          ) : (
                            <Square className="size-4 text-muted-foreground" />
                          )}
                        </div>

                        {scope === "org" ? (
                          <Building className="size-5 text-muted-foreground shrink-0 mt-0.5" />
                        ) : (
                          <FolderGit2 className="size-5 text-muted-foreground shrink-0 mt-0.5" />
                        )}

                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-2">
                            <span className="font-semibold text-foreground truncate">
                              {target.fullName || target.name}
                            </span>
                            <span
                              className={cn(
                                "inline-flex items-center gap-0.5 rounded px-1.5 py-0.2 text-[10px] font-medium border",
                                target.isPrivate
                                  ? "bg-warning/10 text-warning border-warning/30"
                                  : "bg-muted text-muted-foreground border-border",
                              )}
                            >
                              {target.isPrivate ? (
                                <>
                                  <Lock className="size-2.5" />
                                  Private
                                </>
                              ) : (
                                <>
                                  <Globe className="size-2.5" />
                                  Public
                                </>
                              )}
                            </span>
                          </div>
                          {target.description && (
                            <p className="text-[11px] text-muted-foreground truncate mt-0.5">
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
                                className="text-muted-foreground hover:text-primary p-1 shrink-0"
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
            </FieldGroup>
            <div className="flex justify-between items-center pt-4 border-t border-border/60 ">
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
          <div className="mt-5 flex flex-col gap-4">
            <FieldGroup className="gap-4">
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <form.AppField name="minIdleRunners">
                  {() => (
                    <TextField
                      label="Min Idle Warm Runners"
                      id="wizard-min-idle"
                      type="number"
                      min={0}
                      max={20}
                      description="Set to 0 for scale-to-zero mode (ephemeral on-demand only)."
                      onBlurExtra={runEvaluation}
                    />
                  )}
                </form.AppField>

                <form.AppField name="maxConcurrency">
                  {() => (
                    <TextField
                      label="Max Concurrency"
                      id="wizard-max-concurrency"
                      type="number"
                      min={1}
                      max={50}
                      description="Total maximum simultaneous runner containers allowed across all targets."
                      onBlurExtra={runEvaluation}
                    />
                  )}
                </form.AppField>

                <form.AppField name="labels">
                  {() => (
                    <div className="sm:col-span-2 flex flex-col gap-1">
                      <TextField
                        label="Runner Labels"
                        id="wizard-labels"
                        description="Comma-separated list matched in workflow runs."
                        onBlurExtra={runEvaluation}
                      />
                      {labels !== suggestedLabels && (
                        <Button
                          variant="link"
                          size="xs"
                          className="self-start"
                          onClick={() => form.setFieldValue("labels", suggestedLabels)}
                        >
                          Reset to suggested ({suggestedLabels})
                        </Button>
                      )}
                    </div>
                  )}
                </form.AppField>

                <form.AppField name="runnerImage">
                  {() => (
                    <TextField
                      label="Runner Image"
                      id="wizard-runner-image"
                      onBlurExtra={runEvaluation}
                    />
                  )}
                </form.AppField>

                <form.AppField name="cpuLimit">
                  {() => (
                    <TextField label="CPU Limit" id="wizard-cpu" onBlurExtra={runEvaluation} />
                  )}
                </form.AppField>
                <form.AppField name="memoryLimit">
                  {() => (
                    <TextField label="Memory Limit" id="wizard-mem" onBlurExtra={runEvaluation} />
                  )}
                </form.AppField>
                <Field>
                  <FieldLabel htmlFor="wizard-swap">Memory Swap</FieldLabel>
                  <Select value={swapMode} onValueChange={(v) => setSwapMode(v as typeof swapMode)}>
                    <SelectTrigger id="wizard-swap" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        <SelectItem value="match">None — match memory (hardened)</SelectItem>
                        <SelectItem value="default2x">2x memory (Docker default)</SelectItem>
                        <SelectItem value="unlimited">Unlimited</SelectItem>
                        <SelectItem value="custom">Custom…</SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  {swapMode === "custom" && (
                    <form.AppField name="swapCustom">
                      {(field) => {
                        const errors = (field.state.meta.errors as unknown as string[]).filter(
                          Boolean,
                        );
                        return (
                          <div className="mt-2">
                            <Input
                              id="wizard-swap-custom"
                              type="text"
                              placeholder="total memory+swap, e.g. 12GB"
                              value={field.state.value}
                              onBlur={() => {
                                field.handleBlur();
                                runEvaluation();
                              }}
                              onChange={(e) => field.handleChange(e.target.value)}
                              aria-invalid={errors.length > 0}
                              aria-describedby={
                                errors.length > 0 ? "wizard-swap-custom-error" : undefined
                              }
                            />
                            <FormError id="wizard-swap-custom-error" messages={errors} />
                          </div>
                        );
                      }}
                    </form.AppField>
                  )}
                  <p className="text-muted-foreground text-xs">
                    Total memory+swap allowance per runner. Matching memory means a memory-starved
                    runner is killed instead of thrashing host swap.
                  </p>
                </Field>
                <Field>
                  <FieldLabel htmlFor="wizard-pids">PIDs Limit</FieldLabel>
                  <Select value={pidsMode} onValueChange={(v) => setPidsMode(v as typeof pidsMode)}>
                    <SelectTrigger id="wizard-pids" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        <SelectItem value="default">4096 · default</SelectItem>
                        <SelectItem value="strict">1024 · strict</SelectItem>
                        <SelectItem value="unlimited">Unlimited</SelectItem>
                        <SelectItem value="custom">Custom…</SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  {pidsMode === "custom" && (
                    <form.AppField name="pidsCustom">
                      {(field) => {
                        const errors = (field.state.meta.errors as unknown as string[]).filter(
                          Boolean,
                        );
                        return (
                          <div className="mt-2">
                            <Input
                              id="wizard-pids-custom"
                              type="text"
                              inputMode="numeric"
                              placeholder="max processes, e.g. 8192"
                              value={field.state.value}
                              onBlur={() => {
                                field.handleBlur();
                                runEvaluation();
                              }}
                              onChange={(e) => field.handleChange(e.target.value)}
                              aria-invalid={errors.length > 0}
                              aria-describedby={
                                errors.length > 0 ? "wizard-pids-custom-error" : undefined
                              }
                            />
                            <FormError id="wizard-pids-custom-error" messages={errors} />
                          </div>
                        );
                      }}
                    </form.AppField>
                  )}
                  <p className="text-muted-foreground text-xs">
                    Maximum processes per runner container. Bounds the blast radius of runaway
                    builds (fork bombs, recursive scripts) on the host.
                  </p>
                </Field>
              </div>

              {/* Docker Socket Privilege */}
              <Field orientation="horizontal" className="pt-2 border-t border-border/60">
                <Checkbox
                  id="wizard-allow-docker"
                  checked={isDockerLocked ? true : allowDocker}
                  disabled={isDockerLocked}
                  onCheckedChange={(v) => setAllowDocker(v === true)}
                />
                <FieldContent>
                  <FieldLabel htmlFor="wizard-allow-docker">
                    Enable Docker-in-Docker socket access
                  </FieldLabel>
                  {isDockerLocked && (
                    <FieldDescription className="text-[11px] text-warning">
                      Mandatory for {deducedProvider} pools (runner daemon communicates via Docker
                      daemon).
                    </FieldDescription>
                  )}
                </FieldContent>
              </Field>

              {/* Demand Polling Fallback (docs/24 §5.9) */}
              <Field orientation="horizontal" className="pt-2 border-t border-border/60">
                <Checkbox
                  id="wizard-poll-fallback"
                  checked={
                    deducedProvider === "github" ? pollFallback : deducedProvider === "forgejo"
                  }
                  disabled={deducedProvider !== "github"}
                  onCheckedChange={(v) => setPollFallback(v === true)}
                />
                <FieldContent>
                  <FieldLabel htmlFor="wizard-poll-fallback">
                    Scale without webhooks (poll for queued jobs)
                  </FieldLabel>
                  <FieldDescription className="text-[11px]">
                    {deducedProvider === "gitea" &&
                      "Not available for Gitea pools: Gitea has no repo-scoped queued-jobs API."}
                    {deducedProvider === "forgejo" &&
                      "Always on for Forgejo pools: Forgejo has no workflow_job webhooks, so the supervisor polls for waiting tasks."}
                    {deducedProvider === "github" &&
                      "Polls connected repositories for queued jobs so runners scale on hosts without inbound webhooks. Webhooks remain the fast path when available."}
                  </FieldDescription>
                </FieldContent>
              </Field>

              {/* Renovate Bot Section */}
              <div className="rounded-xl border border-border bg-muted/50 p-3 flex flex-col gap-3">
                <Field orientation="horizontal">
                  <Checkbox
                    id="wizard-renovate-enabled"
                    checked={renovateEnabled}
                    onCheckedChange={(v) => setRenovateEnabled(v === true)}
                  />
                  <FieldLabel htmlFor="wizard-renovate-enabled">
                    <Bot className="size-4 text-primary " />
                    Enable Automated Renovate Dependency Scans
                  </FieldLabel>
                </Field>

                {renovateEnabled && (
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 pt-2 border-t border-border ">
                    <form.AppField name="renovateCron">
                      {() => (
                        <TextField
                          label="Cron Schedule"
                          id="wizard-renovate-cron"
                          placeholder="0 2 * * *"
                          onBlurExtra={runEvaluation}
                        />
                      )}
                    </form.AppField>
                    <form.AppField name="renovateImage">
                      {() => (
                        <TextField
                          label="Renovate Image"
                          id="wizard-renovate-image"
                          placeholder="renovate/renovate:latest"
                          onBlurExtra={runEvaluation}
                        />
                      )}
                    </form.AppField>
                  </div>
                )}
              </div>
            </FieldGroup>
            <div className="flex justify-between items-center pt-4 border-t border-border/60 ">
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
          <form onSubmit={handleSubmitPool} noValidate className="mt-5 flex flex-col gap-4">
            {isEdit && changes.length > 0 && (
              <div className="rounded-xl border border-primary/30 bg-primary/10 p-4 flex flex-col gap-2">
                <span className="text-sm font-bold text-foreground ">
                  Changed Fields ({changes.length})
                </span>
                <div className="max-h-44 overflow-y-auto flex flex-col gap-1">
                  {changes.map((c) => (
                    <div
                      key={c.field}
                      className="flex flex-wrap items-baseline gap-x-2 rounded-lg bg-card px-2.5 py-1.5 text-[11px] bg-muted border border-border/60 "
                    >
                      <span className="font-semibold text-foreground min-w-28">{c.field}:</span>
                      <span className="font-mono text-destructive break-all line-through">
                        {c.before || "—"}
                      </span>
                      <ChevronRight className="size-3 shrink-0 self-center text-muted-foreground" />
                      <span className="font-mono text-success break-all">{c.after || "—"}</span>
                    </div>
                  ))}
                </div>
              </div>
            )}
            {isEdit && changes.length === 0 && (
              <div className="rounded-xl border border-border bg-muted/50 p-4 text-[11px] text-muted-foreground ">
                No changes yet — modify any field to see the diff before saving.
              </div>
            )}
            {isEdit && identityChanged && !renamed && (
              <div className="flex items-center gap-2 rounded-xl border border-warning/30 bg-warning/10 p-3 text-warning ">
                <AlertCircle className="size-4 shrink-0" />
                <span>
                  {pool?.idleRunners ?? 0} idle runner{pool?.idleRunners === 1 ? "" : "s"} will be
                  recycled to apply the new configuration; running jobs are not affected.
                </span>
              </div>
            )}
            {isEdit && renamed && (
              <div className="flex items-center gap-2 rounded-xl border border-primary/30 bg-primary/10 p-3 text-primary ">
                <Info className="size-4 shrink-0" />
                <span>
                  Renaming only changes how the pool is displayed — runners are unaffected and keep
                  their original pool label until they recycle naturally.
                </span>
              </div>
            )}
            <div className="rounded-xl border border-border bg-muted/50 p-4 flex flex-col gap-4">
              <div className="flex items-center justify-between border-b border-border pb-3 ">
                <div>
                  <h4 className="text-sm font-bold text-foreground ">{poolName}</h4>
                  <p className="text-[11px] text-muted-foreground">
                    Provider Profile: {selectedAuthProfile?.name} (
                    {authMethodLabel(selectedAuthProfile?.authMethod ?? "")})
                  </p>
                </div>
                <span className="inline-flex items-center rounded-md bg-primary/10 px-2.5 py-1 text-xs font-semibold text-primary capitalize">
                  {deducedProvider} ({scope})
                </span>
              </div>

              {/* Targets Summary */}
              <div>
                <span className="font-semibold text-foreground block mb-1.5">
                  Associated Targets ({selectedTargetUrls.length}):
                </span>
                <div className="max-h-32 overflow-y-auto flex flex-col gap-1 rounded-lg border border-border bg-card p-2 bg-muted">
                  {selectedTargetUrls.map((url) => (
                    <div key={url} className="flex items-center justify-between text-[11px]">
                      <span className="font-mono text-foreground truncate">{url}</span>
                      <a
                        href={url}
                        target="_blank"
                        rel="noreferrer"
                        className="text-muted-foreground hover:text-primary ml-2 shrink-0"
                      >
                        <ExternalLink className="size-3" />
                      </a>
                    </div>
                  ))}
                </div>
              </div>

              {/* Specs Grid */}
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4 border-t border-border pt-3 ">
                <div>
                  <span className="text-muted-foreground block">Idle Warm</span>
                  <span className="font-bold text-foreground ">{minIdleRunners}</span>
                </div>
                <div>
                  <span className="text-muted-foreground block">Max Limit</span>
                  <span className="font-bold text-foreground ">{maxConcurrency}</span>
                </div>
                <div>
                  <span className="text-muted-foreground block">CPU / RAM</span>
                  <span className="font-bold text-foreground ">
                    {cpuLimit} / {describeSwap(swapMode, swapCustom, memoryLimit)}
                  </span>
                </div>
                <div>
                  <span className="text-muted-foreground block">PIDs Limit</span>
                  <span className="font-bold text-foreground ">
                    {describePids(pidsMode, pidsCustom)}
                  </span>
                </div>
                <div>
                  <span className="text-muted-foreground block">Docker Access</span>
                  <span className="font-bold text-foreground ">
                    {isDockerLocked || allowDocker ? "Enabled" : "Disabled"}
                  </span>
                </div>
              </div>

              {/* Labels & Renovate */}
              <div className="border-t border-border pt-3 flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-1.5">
                  <span className="text-muted-foreground">Labels:</span>
                  <span className="font-mono text-foreground ">{labels}</span>
                </div>
                {renovateEnabled && (
                  <span className="inline-flex items-center gap-1 text-[11px] text-primary font-medium">
                    <Bot className="size-3.5" />
                    Renovate Scheduled ({renovateCron})
                  </span>
                )}
              </div>
            </div>

            <div className="flex justify-between items-center pt-4 border-t border-border/60 ">
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
                      <Spinner data-icon="inline-start" />
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
                    <Spinner data-icon="inline-start" />
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
