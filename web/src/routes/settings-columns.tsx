import { createColumnHelper } from "@tanstack/react-table";
import { Button } from "@/components/ui/button";
import { Empty, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Server } from "lucide-react";
import type { Pool } from "../gen/api_pb";
import type { AppTableFeatures } from "../lib/tables/use-app-table";

const columnHelper = createColumnHelper<AppTableFeatures, Pool>();

/**
 * Column defs for the "Configured Pool Images" table on the settings page
 * (docs/31 §5 phase 0). Rendered by the lib/tables DataTable shell, so the
 * visual contract (classes, empty state) mirrors the original hand-rolled
 * markup exactly.
 */
export const poolImagesColumns = (onCheckUpdate: (poolId: bigint) => void) =>
  columnHelper.columns([
    columnHelper.accessor("name", {
      header: "Pool Name",
      cell: (info) => <span className="font-semibold">{info.getValue()}</span>,
    }),
    columnHelper.accessor("runnerImage", {
      header: "Configured Image",
      cell: (info) => <span className="font-mono">{info.getValue()}</span>,
    }),
    columnHelper.accessor("provider", {
      header: "Provider",
      cell: (info) => (
        <span className="font-mono uppercase text-muted-foreground">{info.getValue()}</span>
      ),
    }),
    columnHelper.display({
      id: "actions",
      header: "Actions",
      meta: { headerClassName: "text-right", cellClassName: "text-right" },
      cell: ({ row }) => (
        <Button variant="outline" size="xs" onClick={() => onCheckUpdate(row.original.id)}>
          Check Update
        </Button>
      ),
    }),
  ]);

/** Empty state rendered when no pools are configured (original markup). */
export const poolImagesEmpty = (
  <Empty className="py-6">
    <EmptyHeader>
      <EmptyMedia variant="icon">
        <Server />
      </EmptyMedia>
      <EmptyTitle>No pools configured.</EmptyTitle>
    </EmptyHeader>
  </Empty>
);
