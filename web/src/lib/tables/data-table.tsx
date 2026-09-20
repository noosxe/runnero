import { cn } from "cn";
import { ariaSortValue } from "./sortable-header";
import { flexRender } from "@tanstack/react-table";
import { useEffect, useRef, useState } from "react";
import type { ReactTable, RowData } from "@tanstack/react-table";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { AppTableFeatures } from "./use-app-table";

type DataTableProps<TData extends RowData> = {
  /** Instance from `useAppTable` (lib/tables). */
  table: ReactTable<AppTableFeatures, TData>;
  /** Rendered inside the single empty-row cell when there are no rows. */
  empty?: React.ReactNode;
  /**
   * EXPERIMENTAL: pin the first column (sticky left) while the table
   * scrolls horizontally, so the row identity stays visible. Pinned cells
   * use `bg-card`, which blends inside Cards but not on bare page
   * backgrounds in dark mode — revisit before promoting this out of
   * experimental.
   */
  pinFirst?: boolean;
  /**
   * Per-row attributes (e.g. `data-testid`) applied to the rendered
   * <tr>. Return value is spread onto the TableRow for each row.
   */
  getRowProps?: (
    row: import("@tanstack/react-table").Row<AppTableFeatures, TData>,
  ) => React.HTMLAttributes<HTMLTableRowElement> & { "data-testid"?: string };
};

/**
 * Presentational table shell (docs/31 §4.1): renders header/cell content
 * through `flexRender`, so column definitions own everything visible
 * (badges, menus, testids). Per-column classes come from typed
 * `columnDef.meta.headerClassName` / `cellClassName`.
 */
// Separator on the pinned column's right edge: the inset box-shadow recipe
// from TanStack's kitchen-sink shadcn example (getCommonPinningStyles) — a
// soft border-colored shadow hugging the edge, painted over the full cell
// box (no pseudo-element positioning). Applied only while scrolled.
const PIN_EDGE = "shadow-[inset_-4px_0_4px_-4px_var(--border)]";
// Sticky + opaque background apply whenever pinning is on; the edge shadow
// only while the table is actually scrolled (scroll state tracked below).
const PIN_CELL = "sticky left-0 z-10 bg-card group-hover/row:bg-muted/50";
const PIN_HEAD = "sticky left-0 z-20 bg-card";

export function DataTable<TData extends RowData>({
  table,
  empty,
  getRowProps,
  pinFirst = false,
}: DataTableProps<TData>) {
  const rows = table.getRowModel().rows;
  // The pin edge gradient is only meaningful while the table is actually
  // scrolled; hide it otherwise so an unscrolled table carries no decoration.
  const scrollWrapRef = useRef<HTMLDivElement>(null);
  const [scrolled, setScrolled] = useState(false);
  useEffect(() => {
    const container = scrollWrapRef.current?.querySelector("[data-slot=table-container]");
    if (!container) return;
    const update = () => setScrolled(container.scrollLeft > 0);
    update();
    container.addEventListener("scroll", update, { passive: true });
    return () => container.removeEventListener("scroll", update);
  }, []);
  return (
    <div ref={scrollWrapRef}>
      <Table>
        <TableHeader>
          {table.getHeaderGroups().map((headerGroup) => (
            <TableRow key={headerGroup.id}>
              {headerGroup.headers.map((header, headerIndex) => (
                <TableHead
                  key={header.id}
                  aria-sort={ariaSortValue(header.column.getIsSorted())}
                  className={cn(
                    header.column.columnDef.meta?.headerClassName,
                    headerIndex === 0 && pinFirst && PIN_HEAD,
                    headerIndex === 0 && pinFirst && scrolled && PIN_EDGE,
                  )}
                >
                  {header.isPlaceholder
                    ? null
                    : flexRender(header.column.columnDef.header, header.getContext())}
                </TableHead>
              ))}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {rows.length === 0 ? (
            <TableRow>
              <TableCell colSpan={table.getAllColumns().length}>
                {empty ?? (
                  <div className="py-6 text-center text-sm text-muted-foreground">No results.</div>
                )}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row) => {
              const rowProps = getRowProps?.(row);
              return (
                <TableRow
                  key={row.id}
                  {...rowProps}
                  className={cn("group/row", rowProps?.className)}
                >
                  {row.getAllCells().map((cell, cellIndex) => (
                    <TableCell
                      key={cell.id}
                      className={cn(
                        cell.column.columnDef.meta?.cellClassName,
                        cellIndex === 0 && pinFirst && PIN_CELL,
                        cellIndex === 0 && pinFirst && scrolled && PIN_EDGE,
                      )}
                    >
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </TableCell>
                  ))}
                </TableRow>
              );
            })
          )}
        </TableBody>
      </Table>
    </div>
  );
}
