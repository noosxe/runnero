import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import {
  authClient,
  poolClient,
  authProfileClient,
  onboardingClient,
  analyticsClient,
  logClient,
  renovateClient,
  imageClient,
} from "./transport";

export const queryKeys = {
  onboardingStatus: ["onboarding", "status"] as const,
  appSettings: ["onboarding", "settings"] as const,
  session: ["auth", "session"] as const,
  pools: ["pools"] as const,
  pool: (id: bigint) => ["pools", id.toString()] as const,
  authProfiles: ["authProfiles"] as const,
  systemStats: ["analytics", "systemStats"] as const,
  jobHistory: (params?: {
    poolId?: bigint;
    limit?: number;
    offset?: number;
    search?: string;
    status?: string;
  }) =>
    [
      "analytics",
      "jobHistory",
      {
        poolId: params?.poolId?.toString(),
        limit: params?.limit,
        offset: params?.offset,
        search: params?.search,
        status: params?.status,
      },
    ] as const,
  runners: (poolId: bigint) => ["pools", poolId.toString(), "runners"] as const,
  runnerLogs: (runnerId: string) => ["logs", runnerId] as const,
  jobRecord: (jobId: bigint) => ["analytics", "jobRecord", jobId.toString()] as const,
  imageUpdates: (poolId?: bigint) => ["imageUpdates", poolId?.toString() ?? "all"] as const,
  renovateStatus: (poolId: bigint) => ["renovate", "status", poolId.toString()] as const,
  renovateHistory: (poolId: bigint, limit?: number, offset?: number) =>
    ["renovate", "history", poolId.toString(), { limit, offset }] as const,
  discoverTargets: (authProfileId: bigint, scope: string) =>
    ["pools", "discoverTargets", authProfileId.toString(), scope] as const,
};

// Onboarding Service Hooks
export function useOnboardingStatus() {
  return useQuery({
    queryKey: queryKeys.onboardingStatus,
    queryFn: async () => {
      const res = await onboardingClient.getOnboardingStatus({});
      return res;
    },
    staleTime: 30_000,
  });
}

export function useAppSettings() {
  return useQuery({
    queryKey: queryKeys.appSettings,
    queryFn: async () => {
      const res = await onboardingClient.getAppSettings({});
      return res.settings;
    },
  });
}

export function useSetAppSetting() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (req: Parameters<typeof onboardingClient.setAppSetting>[0]) => {
      return await onboardingClient.setAppSetting(req);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.appSettings });
    },
  });
}

export function useCompleteOnboarding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      return await onboardingClient.completeOnboarding({});
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.onboardingStatus });
      queryClient.invalidateQueries({ queryKey: queryKeys.session });
    },
  });
}

// Direct Query Helpers (used for route guards and preloading)
export async function fetchOnboardingStatus(qc: QueryClient) {
  try {
    return await qc.ensureQueryData({
      queryKey: queryKeys.onboardingStatus,
      queryFn: async () => {
        const res = await onboardingClient.getOnboardingStatus({});
        return res;
      },
      staleTime: 30_000,
    });
  } catch {
    return {
      setupComplete: false,
      adminCreated: false,
      authProfileExists: false,
      poolExists: false,
      onboardingCompleted: false,
    };
  }
}

export async function fetchSession(qc: QueryClient) {
  try {
    return await qc.ensureQueryData({
      queryKey: queryKeys.session,
      queryFn: async () => {
        const res = await authClient.getSession({});
        return res;
      },
      staleTime: 60_000,
    });
  } catch {
    return null;
  }
}

// Auth Service Hooks
export function useSession() {
  return useQuery({
    queryKey: queryKeys.session,
    queryFn: async () => {
      const res = await authClient.getSession({});
      return res;
    },
    retry: false,
    staleTime: 60_000,
  });
}

export function useLogin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (req: Parameters<typeof authClient.login>[0]) => {
      return await authClient.login(req);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.session });
    },
  });
}

export function useLogout() {
  const queryClient = useQueryClient();
  return () => {
    queryClient.setQueryData(queryKeys.session, null);
    queryClient.invalidateQueries({ queryKey: queryKeys.session });
  };
}

export function useSetupAdmin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (req: Parameters<typeof authClient.setupAdmin>[0]) => {
      return await authClient.setupAdmin(req);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.session });
      queryClient.invalidateQueries({ queryKey: queryKeys.onboardingStatus });
    },
  });
}

// Pool Service Hooks
export function usePools() {
  return useQuery({
    queryKey: queryKeys.pools,
    queryFn: async () => {
      const res = await poolClient.listPools({});
      return res.pools;
    },
    staleTime: 5_000,
  });
}

export function useCreatePool() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (req: Parameters<typeof poolClient.createPool>[0]) => {
      return await poolClient.createPool(req);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.pools });
      queryClient.invalidateQueries({ queryKey: queryKeys.onboardingStatus });
      queryClient.invalidateQueries({ queryKey: queryKeys.systemStats });
    },
  });
}

export function useUpdatePool() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (req: Parameters<typeof poolClient.updatePool>[0]) => {
      return await poolClient.updatePool(req);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.pools });
      queryClient.invalidateQueries({ queryKey: queryKeys.systemStats });
    },
  });
}

export function useDeletePool() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: bigint) => {
      return await poolClient.deletePool({ id });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.pools });
      queryClient.invalidateQueries({ queryKey: queryKeys.systemStats });
    },
  });
}

export function useRunners(poolId: bigint) {
  return useQuery({
    queryKey: queryKeys.runners(poolId),
    queryFn: async () => {
      const res = await poolClient.listRunners({ poolId });
      return res.runners;
    },
    enabled: poolId > 0n,
    staleTime: 3_000,
  });
}

export function useTerminateRunner() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ poolId, containerId }: { poolId: bigint; containerId: string }) => {
      return await poolClient.terminateRunner({ poolId, containerId });
    },
    onSuccess: (_, vars) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.runners(vars.poolId) });
      queryClient.invalidateQueries({ queryKey: queryKeys.pools });
      queryClient.invalidateQueries({ queryKey: queryKeys.systemStats });
    },
  });
}

export function useDiscoverTargets(authProfileId: bigint, scope: string) {
  return useQuery({
    queryKey: queryKeys.discoverTargets(authProfileId, scope),
    queryFn: async () => {
      const res = await poolClient.discoverTargets({
        authProfileId,
        scope,
      });
      return {
        targets: res.targets,
        installUrl: res.installUrl,
        installations: res.installations,
      };
    },
    enabled: authProfileId > 0n && !!scope,
    staleTime: 60_000,
    refetchOnWindowFocus: true,
  });
}

// Auth Profile Service Hooks
export function useAuthProfiles() {
  return useQuery({
    queryKey: queryKeys.authProfiles,
    queryFn: async () => {
      const res = await authProfileClient.listAuthProfiles({});
      return res.profiles;
    },
  });
}

export function useCreateAuthProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (req: Parameters<typeof authProfileClient.createAuthProfile>[0]) => {
      return await authProfileClient.createAuthProfile(req);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.authProfiles });
      queryClient.invalidateQueries({ queryKey: queryKeys.onboardingStatus });
    },
  });
}

// Rotates secrets / renames an existing auth profile (docs/17). Blank secret
// fields are intentionally transmitted as empty: the server treats them as
// "keep the existing encrypted secret" (write-only model, no read-back).
export function useUpdateAuthProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (req: Parameters<typeof authProfileClient.updateAuthProfile>[0]) => {
      return await authProfileClient.updateAuthProfile(req);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.authProfiles });
    },
  });
}

export function useDeleteAuthProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: bigint) => {
      return await authProfileClient.deleteAuthProfile({ id });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.authProfiles });
    },
  });
}

// Analytics Service Hooks
export function useSystemStats(timeframeHours = 24) {
  return useQuery({
    queryKey: [...queryKeys.systemStats, timeframeHours] as const,
    queryFn: async () => {
      const res = await analyticsClient.getSystemStats({ timeframeHours });
      return res;
    },
    staleTime: 5_000,
  });
}

export function useJobHistory(params?: {
  poolId?: bigint;
  limit?: number;
  offset?: number;
  search?: string;
  status?: string;
}) {
  const poolId = params?.poolId ?? 0n;
  const limit = params?.limit ?? 25;
  const offset = params?.offset ?? 0;
  const search = params?.search ?? "";
  const status = params?.status ?? "";

  return useQuery({
    queryKey: queryKeys.jobHistory({ poolId, limit, offset, search, status }),
    queryFn: async () => {
      const res = await analyticsClient.getJobHistory({
        poolId,
        limit,
        offset,
        search,
        status,
      });
      return res;
    },
    staleTime: 5_000,
  });
}

export function useJobRecord(jobId?: bigint) {
  return useQuery({
    queryKey: queryKeys.jobRecord(jobId ?? 0n),
    queryFn: async () => {
      const res = await analyticsClient.getJobRecord({ jobId: jobId ?? 0n });
      return res.job;
    },
    enabled: Boolean(jobId && jobId > 0n),
  });
}

// Historical Logs Hook
export function useRunnerLogs(runnerId: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.runnerLogs(runnerId),
    queryFn: async () => {
      const res = await logClient.getRunnerLogs({ runnerId });
      return res.lines;
    },
    enabled: enabled && runnerId.length > 0,
  });
}

// Image Update Service Hooks (RUN-62)
export function useImageUpdates(poolId?: bigint) {
  return useQuery({
    queryKey: queryKeys.imageUpdates(poolId),
    queryFn: async () => {
      const res = await imageClient.listImageUpdates({ poolId: poolId ?? 0n });
      return res.updates;
    },
  });
}

export function useCheckImageUpdate() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (poolId: bigint) => {
      return await imageClient.checkImageUpdate({ poolId });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["imageUpdates"] });
      queryClient.invalidateQueries({ queryKey: queryKeys.pools });
    },
  });
}

export function usePullImage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (poolId: bigint) => {
      return await imageClient.pullImage({ poolId });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["imageUpdates"] });
      queryClient.invalidateQueries({ queryKey: queryKeys.pools });
    },
  });
}

export function useDismissImageUpdate() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: bigint) => {
      return await imageClient.dismissImageUpdate({ id });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["imageUpdates"] });
      queryClient.invalidateQueries({ queryKey: queryKeys.pools });
    },
  });
}

// Renovate Service Hooks
export function useRenovateStatus(
  poolId: bigint,
  options?: { enabled?: boolean; refetchInterval?: number | false },
) {
  return useQuery({
    queryKey: queryKeys.renovateStatus(poolId),
    queryFn: async () => {
      return await renovateClient.getRenovateStatus({ poolId });
    },
    enabled: options?.enabled ?? poolId > 0n,
    refetchInterval: options?.refetchInterval,
  });
}

export function useRenovateHistory(
  poolId: bigint,
  limit = 10,
  offset = 0,
  options?: { enabled?: boolean },
) {
  return useQuery({
    queryKey: queryKeys.renovateHistory(poolId, limit, offset),
    queryFn: async () => {
      return await renovateClient.listRenovateHistory({ poolId, limit, offset });
    },
    enabled: options?.enabled ?? poolId > 0n,
  });
}

export function useTriggerRenovateRun() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (poolId: bigint) => {
      return await renovateClient.triggerRenovateRun({ poolId });
    },
    onSuccess: (_, poolId) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.renovateStatus(poolId) });
      queryClient.invalidateQueries({ queryKey: ["renovate", "history", poolId.toString()] });
      queryClient.invalidateQueries({ queryKey: queryKeys.pools });
    },
  });
}
