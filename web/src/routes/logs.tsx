import { useMemo, useState } from "react";
import { usePageTitle } from "../hooks/use-page-title";
import { Link, useNavigate } from "@tanstack/react-router";
import { useIsAdmin } from "@/lib/api/query-hooks";
import {
  usePools,
  useRemovalRecords,
  useRunnerLogs,
  useSupervisorBoots,
} from "@/lib/api/query-hooks";
import { useStreamSupervisorLogFollow, useSupervisorBootReplay } from "@/lib/api/streaming-hooks";
import { LogTerminal } from "@/components/terminal/log-terminal";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "cn";
import { ScrollText, Terminal, Radio } from "lucide-react";

import { removalsColumns } from "./logs-removals-columns";
import { bootColumns, shortBootId } from "./logs-boot-columns";
import { DataTable, useAppTable } from "../lib/tables";

/** Log view tabs — each one is its own route path under /logs (RUN-283). */
export type LogsTab = "supervisor" | "removals" | "runners";

const TABS: readonly { id: LogsTab; label: string }[] = [
  { id: "supervisor", label: "Supervisor" },
  { id: "removals", label: "Removals" },
  { id: "runners", label: "Runners" },
];

const REMOVAL_REASONS = [
  "reap",
  "task-exit",
  "lifetime-limit",
  "idle-drain",
  "shutdown",
  "pool-drain",
  "recycle",
  "manual",
  "create-failure",
] as const;

function SupervisorTab({ deepLinkBoot }: { deepLinkBoot?: string }) {
  const navigate = useNavigate();
  const boots = useSupervisorBoots();
  // Selected boot is local state seeded from the URL: row clicks update it
  // directly (and mirror into the URL), while /logs/supervisor?boot=…
  // deep links seed it on mount.
  const [selectedBoot, setSelectedBoot] = useState<string | undefined>(deepLinkBoot);
  const [follow, setFollow] = useState(false);

  // Boots table (docs/31 §5 phase 3): hook lives at the component top
  // level; row click/selection affordances flow through getRowProps.
  const bootsTable = useAppTable({
    columns: bootColumns(),
    data: boots.data ?? [],
    getRowId: (b) => b.file,
  });

  // Resolve the selected boot by file name first, then by boot id, so
  // cross-tab "View boot" deep links work with either identifier.
  const boot = useMemo(() => {
    if (!selectedBoot || !boots.data) {
      return undefined;
    }
    return boots.data.find((b) => b.file === selectedBoot || b.bootId === selectedBoot);
  }, [selectedBoot, boots.data]);

  const replay = useSupervisorBootReplay(boot?.file ?? "", Boolean(boot) && !follow);
  const live = useStreamSupervisorLogFollow(boot?.file ?? "", Boolean(boot) && follow);

  // Selecting another boot drops follow mode with it: follow is only valid
  // while the same current-boot file stays open.
  const selectBoot = (file: string) => {
    if (file !== selectedBoot) {
      setFollow(false);
    }
    setSelectedBoot(file);
    void navigate({
      to: "/logs/supervisor",
      search: { boot: file },
    });
  };

  const logs = follow ? live.logs : replay.logs;

  return (
    <div className="flex flex-col gap-4" data-testid="logs-tab-panel-supervisor">
      <div className="overflow-hidden rounded-xl border">
        <DataTable
          table={bootsTable}
          getRowProps={(row) => ({
            "data-testid": "logs-boot-row",
            "data-boot-file": row.original.file,
            className: cn("cursor-pointer", boot?.file === row.original.file && "bg-muted/50"),
            onClick: () => selectBoot(row.original.file),
          })}
          empty={
            <div className="text-muted-foreground">
              {boots.isLoading ? "Loading boot files..." : "No supervisor boot files retained."}
            </div>
          }
        />
      </div>

      {boot ? (
        <div className="min-h-0 flex-1">
          <div className="mb-2 flex items-center justify-between gap-2">
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Terminal className="size-4" />
              <span className="font-mono text-xs">{boot.file}</span>
            </div>
            {boot.isCurrent && (
              <Button
                size="sm"
                variant="outline"
                aria-pressed={follow}
                data-testid="logs-follow-toggle"
                onClick={() => setFollow((prev) => !prev)}
                className={cn(
                  follow && "border-success/60 bg-success/10 text-success hover:bg-success/20",
                )}
              >
                <Radio data-icon="inline-start" />
                <span>{follow ? "Following" : "Follow"}</span>
              </Button>
            )}
          </div>
          <div className="h-[60vh]">
            <LogTerminal
              logs={logs}
              mode={follow ? "live" : "historical"}
              runnerName={`supervisor-boot-${shortBootId(boot.bootId)}`}
              title="Supervisor Boot Log"
              isConnected={follow && live.isConnected}
              isConnecting={follow && live.isConnecting}
              isLoading={!follow && replay.isLoading}
            />
          </div>
        </div>
      ) : (
        selectedBoot && (
          <p className="text-sm text-muted-foreground" data-testid="logs-boot-missing">
            Boot file not found — it may have been removed by retention sweeping.
          </p>
        )
      )}
    </div>
  );
}

function RemovalsTab() {
  const pools = usePools();

  const [reasonFilter, setReasonFilter] = useState("all");
  const [poolFilter, setPoolFilter] = useState("all");
  const [runnerFilter, setRunnerFilter] = useState("");
  const [sinceFilter, setSinceFilter] = useState("");
  const [untilFilter, setUntilFilter] = useState("");
  const [applied, setApplied] = useState({
    reason: "",
    poolId: undefined as bigint | undefined,
    runnerId: "",
    since: "",
    until: "",
  });

  const records = useRemovalRecords(applied);

  // Removal records table (docs/31 §5 phase 2): display-only over the
  // accumulated infinite-query pages; "Load more" keeps driving paging.
  const removalsTable = useAppTable({
    columns: removalsColumns(),
    data: records.data?.pages.flatMap((p) => p.records) ?? [],
    getRowId: (rec) => `${rec.ts}-${rec.runnerId}`,
  });

  const applyFilters = () => {
    setApplied({
      reason: reasonFilter === "all" ? "" : reasonFilter,
      poolId: poolFilter === "all" ? undefined : BigInt(poolFilter),
      runnerId: runnerFilter.trim(),
      since: sinceFilter ? `${sinceFilter}T00:00:00Z` : "",
      until: untilFilter ? `${untilFilter}T23:59:59Z` : "",
    });
  };

  return (
    <div className="flex flex-col gap-4" data-testid="logs-tab-panel-removals">
      <form
        className="grid grid-cols-2 gap-3 md:grid-cols-6"
        data-testid="logs-removal-filters"
        onSubmit={(e) => {
          e.preventDefault();
          applyFilters();
        }}
      >
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="logs-filter-pool">Pool</Label>
          <Select
            value={poolFilter}
            onValueChange={(v) => {
              setPoolFilter(v ?? "all");
            }}
          >
            <SelectTrigger id="logs-filter-pool" data-testid="logs-filter-pool">
              <SelectValue placeholder="All pools" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All pools</SelectItem>
              {pools.data?.map((p) => (
                <SelectItem key={p.id.toString()} value={p.id.toString()}>
                  {p.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="logs-filter-runner">Runner ID</Label>
          <Input
            id="logs-filter-runner"
            data-testid="logs-filter-runner"
            value={runnerFilter}
            onChange={(e) => setRunnerFilter(e.target.value)}
            placeholder="runner id"
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="logs-filter-reason">Reason</Label>
          <Select
            value={reasonFilter}
            onValueChange={(v) => {
              setReasonFilter(v ?? "all");
            }}
          >
            <SelectTrigger id="logs-filter-reason" data-testid="logs-filter-reason">
              <SelectValue placeholder="All reasons" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All reasons</SelectItem>
              {REMOVAL_REASONS.map((r) => (
                <SelectItem key={r} value={r}>
                  {r}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="logs-filter-since">Since</Label>
          <Input
            id="logs-filter-since"
            data-testid="logs-filter-since"
            type="date"
            value={sinceFilter}
            onChange={(e) => setSinceFilter(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="logs-filter-until">Until</Label>
          <Input
            id="logs-filter-until"
            data-testid="logs-filter-until"
            type="date"
            value={untilFilter}
            onChange={(e) => setUntilFilter(e.target.value)}
          />
        </div>
        <div className="flex items-end">
          <Button type="submit" data-testid="logs-filter-apply" className="w-full">
            Apply
          </Button>
        </div>
      </form>

      <div className="overflow-hidden rounded-xl border">
        <DataTable
          table={removalsTable}
          getRowProps={() => ({ "data-testid": "logs-removal-row" })}
          empty={
            <div className="text-muted-foreground">
              {records.isLoading
                ? "Loading removal records..."
                : "No removal records match the current filters."}
            </div>
          }
        />
      </div>

      {records.hasNextPage && (
        <Button
          variant="outline"
          data-testid="logs-removals-load-more"
          onClick={() => void records.fetchNextPage()}
          disabled={records.isFetchingNextPage}
        >
          {records.isFetchingNextPage ? "Loading..." : "Load more"}
        </Button>
      )}
    </div>
  );
}

function RunnersTab({ initialRunner }: { initialRunner?: string }) {
  const [runnerId, setRunnerId] = useState(initialRunner ?? "");
  const [query, setQuery] = useState(initialRunner ?? "");

  const logs = useRunnerLogs(query, query.length > 0);

  return (
    <div className="flex flex-col gap-4" data-testid="logs-tab-panel-runners">
      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setQuery(runnerId.trim());
        }}
      >
        <div className="flex flex-1 flex-col gap-1.5">
          <Label htmlFor="logs-runner-input">Runner ID</Label>
          <Input
            id="logs-runner-input"
            data-testid="logs-runner-input"
            value={runnerId}
            onChange={(e) => setRunnerId(e.target.value)}
            placeholder="e.g. 9f2c11ab-3d4e-4f5a-8b6c-7d8e9f0a1b2c"
            className="font-mono"
          />
        </div>
        <Button type="submit" data-testid="logs-runner-load">
          Load capture
        </Button>
      </form>

      {query ? (
        <div className="h-[60vh]">
          <LogTerminal
            logs={logs.data ?? []}
            mode="historical"
            runnerName={query}
            title={`Runner Capture — ${query}`}
            isLoading={logs.isLoading}
          />
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">
          Enter a runner ID to view its captured output, or follow a removal record's "View capture"
          action.
        </p>
      )}
    </div>
  );
}

export function LogsPage({ tab, runner, boot }: { tab: LogsTab; runner?: string; boot?: string }) {
  usePageTitle("Logs");
  // Supervisor boot logs and removal records are admin surfaces
  // (docs/35 section 2.2, OQ-1); viewers get the runner logs tab. Tabs are
  // path segments (RUN-283): each tab is its own route, the router owns
  // the role defaults, and this strip only renders the visible ones.
  const isAdmin = useIsAdmin();
  const visibleTabs = TABS.filter((t) => isAdmin || t.id === "runners");

  return (
    <div className="flex h-full flex-col gap-6 p-6">
      <header className="flex items-center gap-3">
        <div className="flex size-10 items-center justify-center rounded-xl bg-primary/10">
          <ScrollText className="size-5 text-link" />
        </div>
        <div>
          <h1 className="text-2xl font-bold">Logs</h1>
          <p className="text-sm text-muted-foreground">
            Supervisor boot logs, runner removal records, and captured runner output.
          </p>
        </div>
      </header>

      <div
        className="flex w-fit items-center gap-1 rounded-xl border bg-muted/40 p-1"
        role="tablist"
        aria-label="Log views"
      >
        {visibleTabs.map((t) => (
          <Link
            key={t.id}
            to={`/logs/${t.id}`}
            role="tab"
            aria-selected={tab === t.id}
            aria-current={tab === t.id ? "page" : undefined}
            data-testid={`logs-tab-${t.id}`}
            className={cn(
              "rounded-lg px-4 py-1.5 text-sm font-medium transition-colors",
              tab === t.id
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {t.label}
          </Link>
        ))}
      </div>

      {tab === "supervisor" && <SupervisorTab deepLinkBoot={boot} />}
      {tab === "removals" && <RemovalsTab />}
      {tab === "runners" && <RunnersTab initialRunner={runner} />}
    </div>
  );
}
