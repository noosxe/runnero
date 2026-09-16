import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  useOnboardingStatus,
  usePools,
  useSession,
  useLogout,
  useSessions,
  useRevokeSession,
  useRevokeOtherSessions,
} from "./query-hooks";
import { onboardingClient, poolClient, authClient } from "./transport";

describe("TanStack Query hooks with ConnectRPC", () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    queryClient = new QueryClient({
      defaultOptions: {
        queries: {
          retry: false,
        },
      },
    });
    vi.restoreAllMocks();
  });

  const wrapper = ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );

  it("useOnboardingStatus queries onboarding status", async () => {
    vi.spyOn(onboardingClient, "getOnboardingStatus").mockResolvedValue({
      adminCreated: true,
      authProfileExists: false,
      poolExists: false,
      setupComplete: false,
    } as any);

    const { result } = renderHook(() => useOnboardingStatus(), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data?.adminCreated).toBe(true);
    expect(result.current.data?.setupComplete).toBe(false);
  });

  it("usePools queries runner pools list", async () => {
    vi.spyOn(poolClient, "listPools").mockResolvedValue({
      pools: [
        {
          id: 1n,
          name: "pool-arm64",
          provider: "github",
          repositoryUrl: "https://github.com/owner/repo",
          activeRunners: 2,
          minIdleRunners: 1,
          maxConcurrency: 5,
        },
      ],
    } as any);

    const { result } = renderHook(() => usePools(), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toHaveLength(1);
    expect(result.current.data?.[0].name).toBe("pool-arm64");
  });

  it("useSession queries current user session", async () => {
    vi.spyOn(authClient, "getSession").mockResolvedValue({
      username: "admin",
      isAdmin: true,
    } as any);

    const { result } = renderHook(() => useSession(), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data?.username).toBe("admin");
    expect(result.current.data?.isAdmin).toBe(true);
  });

  it("useRenovateStatus queries status and useTriggerRenovateRun triggers run", async () => {
    const { renovateClient } = await import("./transport");
    vi.spyOn(renovateClient, "getRenovateStatus").mockResolvedValue({
      lastRun: {
        id: 42n,
        poolId: 1n,
        status: "success",
        startedAt: "2026-09-04T00:00:00Z",
        completedAt: "2026-09-04T00:02:00Z",
        summary: "1 PR created",
      },
      nextScheduledRun: "2026-09-05T03:00:00Z",
    } as any);

    vi.spyOn(renovateClient, "triggerRenovateRun").mockResolvedValue({
      success: true,
      runId: 43n,
    } as any);

    const { useRenovateStatus, useTriggerRenovateRun } = await import("./query-hooks");

    const { result: statusResult } = renderHook(() => useRenovateStatus(1n), { wrapper });
    await waitFor(() => expect(statusResult.current.isSuccess).toBe(true));
    expect(statusResult.current.data?.lastRun?.id).toBe(42n);

    const { result: triggerResult } = renderHook(() => useTriggerRenovateRun(), { wrapper });
    const triggerRes = await triggerResult.current.mutateAsync(1n);
    expect(triggerRes.success).toBe(true);
    expect(triggerRes.runId).toBe(43n);
  });

  it("useCompleteOnboarding invokes completeOnboarding and invalidates queries", async () => {
    vi.spyOn(onboardingClient, "completeOnboarding").mockResolvedValue({
      success: true,
    } as any);

    const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");
    const { useCompleteOnboarding } = await import("./query-hooks");

    const { result } = renderHook(() => useCompleteOnboarding(), { wrapper });
    const res = await result.current.mutateAsync();

    expect(res.success).toBe(true);
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["onboarding", "status"] });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["auth", "session"] });
  });

  it("useLogout calls the Logout RPC and clears the cached session", async () => {
    const logoutSpy = vi.spyOn(authClient, "logout").mockResolvedValue({
      success: true,
    } as any);
    queryClient.setQueryData(["auth", "session"], { username: "admin" });

    const { result } = renderHook(() => useLogout(), { wrapper });

    result.current.mutate();
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(logoutSpy).toHaveBeenCalledOnce();
    expect(queryClient.getQueryData(["auth", "session"])).toBeNull();
  });

  it("useLogout clears the cached session even when the RPC fails", async () => {
    vi.spyOn(authClient, "logout").mockRejectedValue(new Error("server unreachable"));

    const { result } = renderHook(() => useLogout(), { wrapper });
    queryClient.setQueryData(["auth", "session"], { username: "admin" });

    result.current.mutate();
    await waitFor(() => expect(result.current.isError).toBe(true));

    // onSettled semantics: a dead session must never linger in the cache.
    expect(queryClient.getQueryData(["auth", "session"])).toBeNull();
  });

  it("useSessions queries the caller's session list", async () => {
    vi.spyOn(authClient, "listSessions").mockResolvedValue({
      sessions: [
        {
          id: 1n,
          deviceLabel: "curl 8.5.0",
          isCurrent: true,
        },
      ],
    } as any);

    const { result } = renderHook(() => useSessions(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toHaveLength(1);
    expect(result.current.data?.[0].isCurrent).toBe(true);
  });

  it("useRevokeSession revokes by row id and refreshes the list", async () => {
    const revokeSpy = vi
      .spyOn(authClient, "revokeSession")
      .mockResolvedValue({ success: true } as any);

    const { result } = renderHook(() => useRevokeSession(), { wrapper });

    result.current.mutate(7n);
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(revokeSpy).toHaveBeenCalledWith({ sessionId: 7n });
  });

  it("useRevokeOtherSessions revokes every other row", async () => {
    const revokeSpy = vi
      .spyOn(authClient, "revokeOtherSessions")
      .mockResolvedValue({ revoked: 2n } as any);

    const { result } = renderHook(() => useRevokeOtherSessions(), { wrapper });

    result.current.mutate();
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(revokeSpy).toHaveBeenCalledOnce();
    expect(result.current.data?.revoked).toBe(2n);
  });
});
