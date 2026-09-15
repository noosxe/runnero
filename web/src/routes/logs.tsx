import { useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import {
  usePools,
  useRemovalRecords,
  useRunnerLogs,
  useSupervisorBoots,
} from "@/lib/api/query-hooks";
import { useStreamSupervisorLogFollow, useSupervisorBootReplay } from "@/lib/api/streaming-hooks";
import { LogTerminal } from "@/components/terminal/log-terminal";
import { Badge } from "@/components/ui/badge";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "cn";
import { ScrollText, Terminal, Radio } from "lucide-react";

function formatBytes(bytes: bigint | number): string {
  const n = Number(bytes);
  if (n < 1024) {
    return `${n} B`;
  }
  const units = ["KiB", "MiB", "GiB"];
  let v = n / 1024;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return `${v.toFixed(1)} ${units[u]}`;
}

export interface LogsPageSearch {
  tab?: string;
  runner?: string;
  boot?: string;
}

const TABS = [
  { id: "supervisor", label: "Supervisor" },
  { id: "removals", label: "Removals" },
  { id: "runners", label: "Runners" },
] as const;

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

// Reason badge palette mirrors the pool diagnostics status colors:
// green = healthy exit, blue = informational, amber = capacity/lifecycle,
// red = failure, neutral = operator/system actions.
const REASON_BADGE_CLASS: Record<string, string> = {
  reap: "border-success/30 bg-success/10 text-success",
  "task-exit": "border-primary/30 bg-primary/10 text-primary",
  "lifetime-limit": "border-warning/30 bg-warning/10 text-warning",
  "idle-drain": "border-warning/30 bg-warning/10 text-warning",
  shutdown: "border-border bg-muted text-muted-foreground",
  "pool-drain": "border-border bg-muted text-muted-foreground",
  recycle: "border-border bg-muted text-muted-foreground",
  manual: "border-border bg-muted text-muted-foreground",
  "create-failure": "border-destructive/30 bg-destructive/10 text-destructive",
};

function shortBootId(bootId: string): string {
  return bootId.length > 8 ? bootId.substring(0, 8) : bootId;
}

function formatTimestamp(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) {
    return ts;
  }
  return d.toLocaleString();
}

function SupervisorTab({ deepLinkBoot }: { deepLinkBoot?: string }) {
  const navigate = useNavigate();
  const boots = useSupervisorBoots();
  // Selected boot is local state seeded from the URL: row clicks update it
  // directly (and mirror into the URL), while /logs?boot=… deep links seed it
  // on mount.
  const [selectedBoot, setSelectedBoot] = useState<string | undefined>(deepLinkBoot);
  const [follow, setFollow] = useState(false);

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
      to: "/logs",
      search: { tab: "supervisor", boot: file },
    });
  };

  const logs = follow ? live.logs : replay.logs;

  return (
    <div className="flex flex-col gap-4" data-testid="logs-tab-panel-supervisor">
      <div className="overflow-hidden rounded-xl border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Boot time</TableHead>
              <TableHead>Boot ID</TableHead>
              <TableHead>Size</TableHead>
              <TableHead>Rotation</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {boots.isLoading ? (
              <TableRow>
                <TableCell colSpan={5} className="text-muted-foreground">
                  Loading boot files...
                </TableCell>
              </TableRow>
            ) : (boots.data?.length ?? 0) === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="text-muted-foreground">
                  No supervisor boot files retained.
                </TableCell>
              </TableRow>
            ) : (
              boots.data?.map((b) => (
                <TableRow
                  key={b.file}
                  data-testid="logs-boot-row"
                  data-boot-file={b.file}
                  className={cn("cursor-pointer", boot?.file === b.file && "bg-muted/50")}
                  onClick={() => selectBoot(b.file)}
                >
                  <TableCell>{formatTimestamp(b.startedAt)}</TableCell>
                  <TableCell className="font-mono text-xs">{shortBootId(b.bootId)}</TableCell>
                  <TableCell>{formatBytes(b.sizeBytes)}</TableCell>
                  <TableCell>{b.rotationSeq > 0 ? `#${b.rotationSeq}` : "—"}</TableCell>
                  <TableCell>
                    {b.isCurrent ? (
                      <Badge
                        data-testid="logs-boot-current-badge"
                        className="border border-success/30 bg-success/10 text-success"
                      >
                        current
                      </Badge>
                    ) : undefined}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
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
  const navigate = useNavigate();
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
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Time</TableHead>
              <TableHead>Pool</TableHead>
              <TableHead>Runner</TableHead>
              <TableHead>Reason</TableHead>
              <TableHead>Busy</TableHead>
              <TableHead>Exit</TableHead>
              <TableHead>Capture</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {records.isLoading ? (
              <TableRow>
                <TableCell colSpan={8} className="text-muted-foreground">
                  Loading removal records...
                </TableCell>
              </TableRow>
            ) : records.data?.pages.every((p) => p.records.length === 0) ? (
              <TableRow>
                <TableCell colSpan={8} className="text-muted-foreground">
                  No removal records match the current filters.
                </TableCell>
              </TableRow>
            ) : (
              records.data?.pages.flatMap((page) =>
                page.records.map((rec) => (
                  <TableRow key={`${rec.ts}-${rec.runnerId}`} data-testid="logs-removal-row">
                    <TableCell className="whitespace-nowrap text-xs">
                      {formatTimestamp(rec.ts)}
                    </TableCell>
                    <TableCell className="text-xs">{rec.poolName || "—"}</TableCell>
                    <TableCell className="font-mono text-xs">
                      {rec.runnerName || rec.runnerId}
                    </TableCell>
                    <TableCell>
                      <Badge
                        data-testid="logs-reason-badge"
                        className={cn("border", REASON_BADGE_CLASS[rec.reason])}
                      >
                        {rec.reason}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      {rec.providerBusy ? (
                        <Badge className="border border-warning/30 bg-warning/10 text-warning">
                          busy
                        </Badge>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {rec.exitCode >= 0 ? rec.exitCode : "—"}
                    </TableCell>
                    <TableCell>
                      {rec.captureOk ? (
                        <Badge className="border border-success/30 bg-success/10 text-success">
                          {formatBytes(rec.captureBytes)}
                        </Badge>
                      ) : (
                        <Badge variant="secondary">none</Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button
                          size="xs"
                          variant="outline"
                          data-testid="logs-removal-view-capture"
                          onClick={() =>
                            navigate({
                              to: "/logs",
                              search: { tab: "runners", runner: rec.runnerId },
                            })
                          }
                        >
                          View capture
                        </Button>
                        <Button
                          size="xs"
                          variant="outline"
                          data-testid="logs-removal-view-boot"
                          disabled={!rec.bootId}
                          onClick={() =>
                            navigate({
                              to: "/logs",
                              search: { tab: "supervisor", boot: rec.bootId },
                            })
                          }
                        >
                          View boot
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )),
              )
            )}
          </TableBody>
        </Table>
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

export function LogsPage({ search }: { search: LogsPageSearch }) {
  const navigate = useNavigate();

  const tab = TABS.some((t) => t.id === search.tab) ? (search.tab as string) : "supervisor";

  const selectTab = (id: string) => {
    void navigate({ to: "/logs", search: { tab: id } });
  };

  return (
    <div className="flex h-full flex-col gap-6 p-6">
      <header className="flex items-center gap-3">
        <div className="flex size-10 items-center justify-center rounded-xl bg-primary/10">
          <ScrollText className="size-5 text-primary" />
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
        {TABS.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            data-testid={`logs-tab-${t.id}`}
            onClick={() => selectTab(t.id)}
            className={cn(
              "rounded-lg px-4 py-1.5 text-sm font-medium transition-colors",
              tab === t.id
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "supervisor" && <SupervisorTab deepLinkBoot={search.boot} />}
      {tab === "removals" && <RemovalsTab />}
      {tab === "runners" && <RunnersTab initialRunner={search.runner} />}
    </div>
  );
}
