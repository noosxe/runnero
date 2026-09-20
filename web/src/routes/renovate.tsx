import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { DataTable, useAppTable } from "../lib/tables";
import { poolRenovateColumns } from "./renovate-columns";
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
  EmptyDescription,
  EmptyContent,
} from "@/components/ui/empty";
import { LinkButton } from "../lib/link-button";
import { usePools } from "../lib/api/query-hooks";
import { usePageTitle } from "../hooks/use-page-title";
import { Skeleton } from "@/components/ui/skeleton";
import { Bot, Layers, Calendar } from "lucide-react";

export function RenovatePage() {
  usePageTitle("Renovate Bot");
  const { data: pools, isLoading } = usePools();

  // Pool renovate table (docs/31 §5 phase 1): hook lives at the component
  // top level; row-scoped queries moved into the columns' cell components.
  const poolRenovateTable = useAppTable({
    columns: poolRenovateColumns(),
    data: pools ?? [],
    getRowId: (pool) => pool.id.toString(),
  });

  const totalPools = pools?.length ?? 0;
  const enabledPools = pools?.filter((p) => p.renovate?.enabled).length ?? 0;

  return (
    <div className="flex flex-col gap-6">
      {/* Header */}
      <div>
        <div className="flex items-center gap-2.5">
          <h1 className="text-2xl font-bold tracking-tight text-foreground ">
            Renovate Bot Dashboard
          </h1>
          <Badge className="border-primary/30 bg-primary/10 text-primary font-medium">
            <Bot />
            <span className="font-mono text-[10px]">Managed Automation</span>
          </Badge>
        </div>
        <p className="mt-1 text-sm text-muted-foreground ">
          Automated dependency updates, scheduled scans, and on-demand maintenance runs across
          runner pools.
        </p>
      </div>

      {/* Overview Stats */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Card size="sm" className="p-4">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-muted-foreground">Configured Pools</span>
            <Layers className="size-4 text-muted-foreground" />
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-foreground">{totalPools}</span>
            <span className="text-xs text-muted-foreground">total pools</span>
          </div>
        </Card>

        <Card size="sm" className="p-4">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-muted-foreground">Renovate Active</span>
            <Bot className="size-4 text-success" />
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-success">{enabledPools}</span>
            <span className="text-xs text-muted-foreground">of {totalPools} pools scheduled</span>
          </div>
        </Card>

        <Card size="sm" className="p-4">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-muted-foreground">Automation Coverage</span>
            <Calendar className="size-4 text-primary" />
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-foreground">
              {totalPools > 0 ? Math.round((enabledPools / totalPools) * 100) : 0}%
            </span>
            <span className="text-xs text-muted-foreground">pools covered</span>
          </div>
        </Card>
      </div>

      {/* Pools Renovate List */}
      <div className="flex flex-col gap-4">
        <h2 className="text-base font-bold text-foreground ">Runner Pool Schedules & Status</h2>

        {isLoading ? (
          <div className="flex flex-col gap-3">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : !pools || pools.length === 0 ? (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Bot />
              </EmptyMedia>
              <EmptyTitle>No runner pools found</EmptyTitle>
              <EmptyDescription>
                Create your first runner pool to enable automated Renovate dependency updates.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <LinkButton to="/onboarding" size="sm">
                Create Runner Pool
              </LinkButton>
            </EmptyContent>
          </Empty>
        ) : (
          <Card className="py-0">
            <div className="overflow-x-auto">
              <DataTable table={poolRenovateTable} />
            </div>
          </Card>
        )}
      </div>
    </div>
  );
}
