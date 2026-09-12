import { useState, useMemo } from "react";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { cn } from "cn";
import { Button } from "@/components/ui/button";
import { usePools, useAuthProfiles, useSession } from "../lib/api/query-hooks";
import { useWatchPools } from "../lib/api/streaming-hooks";
import { PoolWizardModal } from "../components/pools/pool-wizard-modal";
import { Link } from "@tanstack/react-router";
import {
  Server,
  Search,
  Cpu,
  HardDrive,
  Shield,
  Activity,
  ArrowUpRight,
  Info,
  Plus,
  AlertTriangle,
  Pencil,
} from "lucide-react";
import { PoolHealthBadge } from "../components/pools/pool-health-badge";
import { poolTargetList, TargetCountBadge } from "../components/pools/pool-targets";
import { PoolHealthStatus, type Pool } from "../gen/api_pb";

export function PoolsPage() {
  const { data: pools, isLoading } = usePools();
  const { data: authProfiles, isLoading: authProfilesLoading } = useAuthProfiles();
  const { data: session } = useSession();
  const { isConnected } = useWatchPools();
  const hasAuthProfiles = Boolean(authProfiles && authProfiles.length > 0);

  const [search, setSearch] = useState("");
  const [providerFilter, setProviderFilter] = useState("all");
  const [scopeFilter, setScopeFilter] = useState("all");
  const [healthFilter, setHealthFilter] = useState("all");

  // Create Pool Modal State
  const [isModalOpen, setIsModalOpen] = useState(false);

  // Edit Pool Modal State (docs/22 §7.1)
  const [editingPool, setEditingPool] = useState<Pool | null>(null);

  const filteredPools = useMemo(() => {
    if (!pools) return [];
    return pools.filter((p) => {
      const matchesSearch =
        search === "" ||
        p.name.toLowerCase().includes(search.toLowerCase()) ||
        p.repositoryUrl.toLowerCase().includes(search.toLowerCase()) ||
        p.targetUrls?.some((t) => t.toLowerCase().includes(search.toLowerCase()));

      const matchesProvider =
        providerFilter === "all" || p.provider.toLowerCase() === providerFilter.toLowerCase();

      const matchesScope =
        scopeFilter === "all" || (p.scope || "repo").toLowerCase() === scopeFilter.toLowerCase();

      const matchesHealth =
        healthFilter === "all" ||
        (healthFilter === "healthy" && p.healthStatus === PoolHealthStatus.HEALTHY) ||
        (healthFilter === "provisioning" && p.healthStatus === PoolHealthStatus.PROVISIONING) ||
        (healthFilter === "degraded" && p.healthStatus === PoolHealthStatus.DEGRADED) ||
        (healthFilter === "paused" && p.healthStatus === PoolHealthStatus.PAUSED);

      return matchesSearch && matchesProvider && matchesScope && matchesHealth;
    });
  }, [pools, search, providerFilter, scopeFilter, healthFilter]);

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <div className="flex items-center gap-2.5">
            <h1 className="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
              Runner Pools
            </h1>
            <Badge
              className={cn(
                "border",
                isConnected
                  ? "border-success/30 bg-success/10 text-success"
                  : "border-warning/30 bg-warning/10 text-warning",
              )}
            >
              <span
                className={`h-1.5 w-1.5 rounded-full ${
                  isConnected ? "bg-emerald-500 animate-pulse" : "bg-amber-500"
                }`}
              />
              <span className="font-mono text-[10px]">
                {isConnected ? "Live Stream" : "Connecting"}
              </span>
            </Badge>
          </div>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            Manage ephemeral worker pools, runtime scaling targets, and provider bindings.
          </p>
        </div>

        {hasAuthProfiles ? (
          <Button onClick={() => setIsModalOpen(true)}>
            <span>+ Add Runner Pool</span>
          </Button>
        ) : (
          <Link
            to="/profiles"
            className="inline-flex items-center justify-center gap-1.5 rounded-xl bg-blue-600 px-4 py-2 text-sm font-semibold text-white shadow-xs hover:bg-blue-500 transition-colors"
          >
            <span>+ Add Runner Pool</span>
          </Link>
        )}
      </div>

      {/* Missing Auth Profile Warning Banner */}
      {!hasAuthProfiles && !authProfilesLoading && (
        <div className="flex items-start gap-3 rounded-2xl border border-amber-200 bg-amber-50/70 p-4 text-xs text-amber-800 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-300">
          <Info className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <div className="flex-1 space-y-1">
            <p className="font-semibold text-slate-900 dark:text-white">
              Git Authentication Profile Required
            </p>
            <p className="text-[11px] text-amber-700 dark:text-amber-400">
              Runner pools require upstream credentials to register ephemeral runners with GitHub,
              Gitea, or Forgejo. Connect an auth profile first or run through the setup wizard.
            </p>
          </div>
          <Link
            to="/profiles"
            className="shrink-0 rounded-xl bg-amber-600 px-3 py-1.5 text-xs font-semibold text-white shadow-xs transition-colors hover:bg-amber-700 dark:bg-amber-500 dark:hover:bg-amber-600"
          >
            Configure Profile &rarr;
          </Link>
        </div>
      )}

      {/* Filters Toolbar */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between rounded-2xl border border-slate-200 bg-white p-3 shadow-xs dark:border-slate-800 dark:bg-slate-900">
        <div className="relative flex-1">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400" />
          <Input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search pools by name or target URL..."
            className="pl-9"
          />
        </div>

        <div className="flex items-center gap-2">
          <Select
            value={providerFilter}
            onValueChange={(v) => setProviderFilter(v as string)}
            items={[
              { value: "all", label: "All Providers" },
              { value: "github", label: "GitHub" },
              { value: "gitea", label: "Gitea" },
              { value: "forgejo", label: "Forgejo" },
            ]}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">All Providers</SelectItem>
                <SelectItem value="github">GitHub</SelectItem>
                <SelectItem value="gitea">Gitea</SelectItem>
                <SelectItem value="forgejo">Forgejo</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>

          <Select
            value={scopeFilter}
            onValueChange={(v) => setScopeFilter(v as string)}
            items={[
              { value: "all", label: "All Scopes" },
              { value: "repo", label: "Repository" },
              { value: "org", label: "Organization" },
              { value: "global", label: "Global" },
            ]}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">All Scopes</SelectItem>
                <SelectItem value="repo">Repository</SelectItem>
                <SelectItem value="org">Organization</SelectItem>
                <SelectItem value="global">Global</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>

          <Select
            value={healthFilter}
            onValueChange={(v) => setHealthFilter(v as string)}
            items={[
              { value: "all", label: "All Health States" },
              { value: "healthy", label: "Healthy" },
              { value: "provisioning", label: "Provisioning" },
              { value: "degraded", label: "Degraded" },
              { value: "paused", label: "Paused" },
            ]}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">All Health States</SelectItem>
                <SelectItem value="healthy">Healthy</SelectItem>
                <SelectItem value="provisioning">Provisioning</SelectItem>
                <SelectItem value="degraded">Degraded</SelectItem>
                <SelectItem value="paused">Paused</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      </div>

      {/* Pools Grid */}
      {isLoading ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-44 w-full" />
          ))}
        </div>
      ) : filteredPools.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-slate-300 p-12 text-center text-slate-500 dark:border-slate-800 dark:text-slate-400">
          <Server className="mx-auto mb-2 h-8 w-8 text-slate-400" />
          <p className="text-base font-semibold text-slate-800 dark:text-slate-200">
            {pools?.length === 0 ? "No runner pools configured" : "No pools match your filters"}
          </p>
          <p className="mx-auto mt-1 max-w-md text-xs text-slate-500 dark:text-slate-400">
            {pools?.length === 0
              ? hasAuthProfiles
                ? "Git authentication profile is ready. Create your first runner pool to start processing CI workflows."
                : "No Git authentication profiles are configured yet. Connect a Git profile before creating your first pool."
              : "Try adjusting your search terms or filter criteria."}
          </p>
          {pools?.length === 0 && (
            <div className="mt-4 flex items-center justify-center gap-3">
              {hasAuthProfiles ? (
                <Button size="xs" onClick={() => setIsModalOpen(true)}>
                  <span>+ Add Runner Pool</span>
                </Button>
              ) : (
                <Link
                  to="/profiles"
                  className="inline-flex items-center gap-1.5 rounded-xl bg-blue-600 px-4 py-2 text-xs font-semibold text-white shadow-xs transition-colors hover:bg-blue-500"
                >
                  <Plus className="h-3.5 w-3.5" />
                  <span>Configure Git Profile</span>
                </Link>
              )}
            </div>
          )}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
          {filteredPools.map((p) => {
            const maxConcurrency = p.maxConcurrency > 0 ? p.maxConcurrency : 1;
            const utilization = Math.min(100, Math.round((p.activeRunners / maxConcurrency) * 100));

            return (
              <Card key={p.id.toString()} className="group relative">
                <CardContent>
                  {/* Pool Header */}
                  <div className="flex items-start justify-between gap-2">
                    <div>
                      <h3 className="text-lg font-bold text-foreground group-hover:text-primary transition-colors">
                        {p.name}
                      </h3>
                      {poolTargetList(p).length > 1 && (
                        <div className="mt-0.5">
                          <TargetCountBadge pool={p} />
                        </div>
                      )}
                      {p.currentIntent && (
                        <p className="mt-1.5 text-xs text-muted-foreground italic flex items-center gap-1.5">
                          <span className="h-1.5 w-1.5 rounded-full bg-primary inline-block shrink-0" />
                          <span className="truncate">{p.currentIntent}</span>
                        </p>
                      )}
                    </div>

                    <div className="flex items-center gap-1.5 flex-wrap">
                      <PoolHealthBadge status={p.healthStatus} size="sm" />
                      <span className="rounded-md bg-primary/10 px-2 py-0.5 text-[11px] font-semibold text-primary uppercase tracking-wider">
                        {p.provider}
                      </span>
                      <span className="rounded-md bg-muted px-2 py-0.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
                        {p.scope || "repo"}
                      </span>
                    </div>
                  </div>

                  {p.healthStatus === PoolHealthStatus.DEGRADED && (
                    <div className="mt-3 rounded-xl border border-destructive/20 bg-destructive/5 p-3 text-xs text-foreground/90">
                      <div className="flex items-start justify-between gap-2">
                        <div className="flex items-start gap-2 min-w-0">
                          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center gap-2">
                              <span className="font-semibold text-destructive">
                                Reconciliation Error
                              </span>
                              {p.lastErrorCode && (
                                <span className="font-mono text-[10px] bg-destructive/10 px-1.5 py-0.5 rounded border border-destructive/20">
                                  {p.lastErrorCode}
                                </span>
                              )}
                            </div>
                            {p.lastError && (
                              <p className="mt-1 font-mono text-[11px] break-words line-clamp-2 text-destructive/90">
                                {p.lastError}
                              </p>
                            )}
                          </div>
                        </div>
                        {p.lastErrorCode?.includes("AUTH") && (
                          <Link
                            to="/profiles"
                            className="shrink-0 text-[11px] font-semibold text-destructive hover:text-destructive/80 underline decoration-destructive/40"
                          >
                            Fix Auth &rarr;
                          </Link>
                        )}
                      </div>
                    </div>
                  )}

                  {/* Utilization Progress Bar */}
                  <div className="mt-5">
                    <div className="flex items-center justify-between text-xs text-muted-foreground">
                      <span className="flex items-center gap-1">
                        <Activity className="h-3.5 w-3.5 text-success" />
                        <span>Capacity Utilization</span>
                      </span>
                      <span className="font-semibold text-foreground">
                        {p.activeRunners} / {p.maxConcurrency} ({utilization}%)
                      </span>
                    </div>
                    <div className="mt-1.5 h-2 w-full overflow-hidden rounded-full bg-muted">
                      <div
                        className={`h-full transition-all duration-500 rounded-full ${
                          utilization > 85
                            ? "bg-destructive"
                            : utilization > 60
                              ? "bg-warning"
                              : "bg-success"
                        }`}
                        style={{ width: `${utilization}%` }}
                      />
                    </div>
                  </div>

                  {/* Metrics Grid */}
                  <div className="mt-5 grid grid-cols-3 gap-2 rounded-xl bg-muted/50 p-3 text-center text-xs border border-border/60">
                    <div>
                      <span className="text-muted-foreground">Active</span>
                      <div className="mt-0.5 text-base font-bold text-foreground">
                        {p.activeRunners}
                      </div>
                    </div>
                    <div>
                      <span className="text-muted-foreground">Idle Warm Target</span>
                      <div className="mt-0.5 text-base font-bold text-foreground">
                        {p.minIdleRunners}
                        {p.healthStatus === PoolHealthStatus.DEGRADED && p.minIdleRunners > 0 && (
                          <span className="ml-1 text-[10px] font-normal text-rose-600 dark:text-rose-400">
                            (Failed)
                          </span>
                        )}
                        {p.healthStatus === PoolHealthStatus.PROVISIONING && (
                          <span className="ml-1 text-[10px] font-normal text-amber-600 dark:text-amber-400">
                            (Warming)
                          </span>
                        )}
                      </div>
                    </div>
                    <div>
                      <span className="text-muted-foreground">Max Limit</span>
                      <div className="mt-0.5 text-base font-bold text-foreground">
                        {p.maxConcurrency}
                      </div>
                    </div>
                  </div>

                  {/* Badges / Specs Strip */}
                  <div className="mt-4 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <span className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 font-medium text-foreground/80">
                      <Cpu className="h-3 w-3 text-muted-foreground" />
                      {p.cpuLimit || "2"} CPU
                    </span>
                    <span className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 font-medium text-foreground/80">
                      <HardDrive className="h-3 w-3 text-muted-foreground" />
                      {p.memoryLimit || "4G"} Mem
                    </span>
                    {p.allowDocker && (
                      <span className="inline-flex items-center gap-1 rounded-md border border-success/30 bg-success/10 px-2 py-0.5 font-medium text-success">
                        <Shield className="h-3 w-3" />
                        Docker Enabled
                      </span>
                    )}
                  </div>
                </CardContent>

                {/* Footer Link */}
                <CardFooter className="border-t justify-between">
                  <span className="text-xs text-muted-foreground">
                    Image:{" "}
                    <span className="font-mono text-foreground/80">
                      {p.runnerImage ? p.runnerImage.split("/").pop() : "runnero:latest"}
                    </span>
                  </span>

                  <div className="flex items-center gap-3">
                    <Button
                      variant="ghost"
                      size="xs"
                      aria-label={`Edit pool ${p.name}`}
                      onClick={() => setEditingPool(p)}
                    >
                      <Pencil data-icon="inline-start" />
                      <span>Edit</span>
                    </Button>
                    <Link
                      to="/pools/$poolId"
                      params={{ poolId: p.id.toString() }}
                      className="inline-flex items-center gap-1 text-xs font-semibold text-primary hover:text-primary/90 transition-colors"
                    >
                      <span>View Pool Details</span>
                      <ArrowUpRight className="h-3.5 w-3.5" />
                    </Link>
                  </div>
                </CardFooter>
              </Card>
            );
          })}
        </div>
      )}

      {/* Create Pool Wizard Modal */}
      <PoolWizardModal
        isOpen={isModalOpen}
        onClose={() => setIsModalOpen(false)}
        authProfiles={authProfiles}
        hostOs={session?.hostOs}
        hostArch={session?.hostArch}
      />

      {/* Edit Pool Modal (docs/22 §7.1) — conditionally mounted so edit-mode
          prefill state initializes from the selected pool on every open */}
      {editingPool !== null && (
        <PoolWizardModal
          isOpen
          mode="edit"
          pool={editingPool}
          onClose={() => setEditingPool(null)}
          authProfiles={authProfiles}
          hostOs={session?.hostOs}
          hostArch={session?.hostArch}
        />
      )}
    </div>
  );
}
