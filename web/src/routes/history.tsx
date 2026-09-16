import { useState } from "react";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
  EmptyDescription,
} from "@/components/ui/empty";
import { useJobHistory, usePools } from "../lib/api/query-hooks";
import { History, Search, ChevronLeft, ChevronRight, Download } from "lucide-react";

import { jobHistoryColumns } from "./history-columns";
import { DataTable, useAppTable } from "../lib/tables";

export function HistoryPage() {
  const [search, setSearch] = useState("");
  const [selectedPool, setSelectedPool] = useState<string>("0");
  const [statusFilter, setStatusFilter] = useState<string>("all");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);

  const { data: pools } = usePools();

  const poolId = selectedPool === "0" ? 0n : BigInt(selectedPool);
  const offset = (page - 1) * pageSize;

  const {
    data: history,
    isLoading,
    isFetching,
  } = useJobHistory({
    poolId,
    limit: pageSize,
    offset,
    search,
    status: statusFilter,
  });

  const totalCount = history?.totalCount ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalCount / pageSize));

  // Job history table (docs/31 §5 phase 2): server-side offset paging —
  // the table only ever displays the current page. The hook lives at the
  // component top level; the bespoke footer keeps driving `page` state.
  const jobHistoryTable = useAppTable({
    columns: jobHistoryColumns(),
    data: history?.jobs ?? [],
    getRowId: (job) => job.id.toString(),
    manualPagination: true,
    pageCount: totalPages,
    state: {
      pagination: { pageIndex: page - 1, pageSize },
    },
    onPaginationChange: (updater) => {
      const next =
        typeof updater === "function" ? updater({ pageIndex: page - 1, pageSize }) : updater;
      setPage(next.pageIndex + 1);
    },
  });

  // Reset to page 1 when search or filters change
  const handleSearchChange = (val: string) => {
    setSearch(val);
    setPage(1);
  };

  const handlePoolChange = (val: string) => {
    setSelectedPool(val);
    setPage(1);
  };

  const handleStatusChange = (val: string) => {
    setStatusFilter(val);
    setPage(1);
  };

  // CSV export driven by the table model (docs/31 §4.1): the current
  // page's rows as the table sees them.
  const handleExportCSV = () => {
    const rows = jobHistoryTable.getRowModel().rows;
    if (rows.length === 0) return;
    const headers = [
      "ID",
      "Status",
      "Runner Name",
      "Pool ID",
      "Pool Name",
      "Duration (s)",
      "Queue Time (s)",
      "Queued At",
      "Started At",
      "Completed At",
    ];
    const cells = rows.map(({ original: j }) => [
      j.id.toString(),
      j.status,
      `"${j.runnerName.replace(/"/g, '""')}"`,
      j.poolId.toString(),
      `"${(j.poolName || "").replace(/"/g, '""')}"`,
      j.durationSeconds.toFixed(1),
      j.queueTimeSeconds.toFixed(1),
      j.queuedAt || "",
      j.startedAt || "",
      j.completedAt || "",
    ]);
    const csvContent = [headers.join(","), ...cells.map((r) => r.join(","))].join("\n");
    const blob = new Blob([csvContent], { type: "text/csv;charset=utf-8;" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.setAttribute("href", url);
    link.setAttribute("download", `job-history-${new Date().toISOString().slice(0, 10)}.csv`);
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
  };

  return (
    <div className="flex flex-col gap-6">
      {/* Page Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground ">
            Job Execution History
          </h1>
          <p className="mt-1 text-sm text-muted-foreground ">
            Historical execution records, queue latencies, duration metrics, and runner logs.
          </p>
        </div>

        <Button
          variant="outline"
          size="sm"
          onClick={handleExportCSV}
          disabled={!history?.jobs || history.jobs.length === 0}
        >
          <Download data-icon="inline-start" />
          <span>Export CSV</span>
        </Button>
      </div>

      {/* Filters Toolbar */}
      <Card size="sm" className="gap-3 p-3">
        <InputGroup className="flex-1">
          <InputGroupAddon align="inline-start">
            <Search />
          </InputGroupAddon>
          <InputGroupInput
            type="text"
            value={search}
            onChange={(e) => handleSearchChange(e.target.value)}
            placeholder="Search by runner name..."
          />
        </InputGroup>

        <div className="flex flex-wrap items-center gap-2">
          {/* Pool Filter */}
          <Select
            value={selectedPool}
            onValueChange={(v) => handlePoolChange(v as string)}
            items={[
              { value: "0", label: "All Pools" },
              ...(pools ?? []).map((p) => ({ value: p.id.toString(), label: p.name })),
            ]}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="0">All Pools</SelectItem>
                {(pools ?? []).map((p) => (
                  <SelectItem key={p.id.toString()} value={p.id.toString()}>
                    {p.name}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>

          {/* Status Filter */}
          <Select
            value={statusFilter}
            onValueChange={(v) => handleStatusChange(v as string)}
            items={[
              { value: "all", label: "All Statuses" },
              { value: "success", label: "Success" },
              { value: "failure", label: "Failed" },
              { value: "running", label: "Running" },
            ]}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">All Statuses</SelectItem>
                <SelectItem value="success">Success</SelectItem>
                <SelectItem value="failure">Failed</SelectItem>
                <SelectItem value="running">Running</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>

          {/* Page Size Select */}
          <Select
            value={pageSize}
            onValueChange={(v) => {
              setPageSize(Number(v));
              setPage(1);
            }}
            items={[10, 25, 50].map((n) => ({ value: n, label: `${n} / page` }))}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {[10, 25, 50].map((n) => (
                  <SelectItem key={n} value={n}>
                    {n} / page
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      </Card>

      {/* History Table */}
      {isLoading ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : !history?.jobs || history.jobs.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <History />
            </EmptyMedia>
            <EmptyTitle>No execution records found</EmptyTitle>
            <EmptyDescription>
              {search || statusFilter !== "all" || selectedPool !== "0"
                ? "No job history matched your current filters. Try resetting search or status filters."
                : "Completed runner workflow jobs will appear here automatically."}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <Card className="py-0">
          <div className="overflow-x-auto">
            <DataTable table={jobHistoryTable} />
          </div>

          {/* Pagination Footer */}
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between border-t border-border/60 px-(--card-spacing) py-3.5 text-xs text-muted-foreground">
            <div>
              Showing{" "}
              <span className="font-semibold text-foreground">
                {totalCount === 0 ? 0 : offset + 1}
              </span>{" "}
              to{" "}
              <span className="font-semibold text-foreground">
                {Math.min(offset + pageSize, totalCount)}
              </span>{" "}
              of <span className="font-semibold text-foreground">{totalCount}</span> runs
            </div>

            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="xs"
                onClick={() => setPage((p) => Math.max(1, p - 1))}
                disabled={page <= 1 || isFetching}
              >
                <ChevronLeft data-icon="inline-start" /> Prev
              </Button>

              <span className="px-2 font-mono text-muted-foreground ">
                {page} / {totalPages}
              </span>

              <Button
                variant="outline"
                size="xs"
                onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                disabled={page >= totalPages || isFetching}
              >
                Next <ChevronRight data-icon="inline-end" />
              </Button>
            </div>
          </div>
        </Card>
      )}
    </div>
  );
}
