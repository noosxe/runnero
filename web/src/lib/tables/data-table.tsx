import { flexRender } from "@tanstack/react-table";
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
export function DataTable<TData extends RowData>({
  table,
  empty,
  getRowProps,
}: DataTableProps<TData>) {
  const rows = table.getRowModel().rows;
  return (
    <Table>
      <TableHeader>
        {table.getHeaderGroups().map((headerGroup) => (
          <TableRow key={headerGroup.id}>
            {headerGroup.headers.map((header) => (
              <TableHead key={header.id} className={header.column.columnDef.meta?.headerClassName}>
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
          rows.map((row) => (
            <TableRow key={row.id} {...getRowProps?.(row)}>
              {row.getAllCells().map((cell) => (
                <TableCell key={cell.id} className={cell.column.columnDef.meta?.cellClassName}>
                  {flexRender(cell.column.columnDef.cell, cell.getContext())}
                </TableCell>
              ))}
            </TableRow>
          ))
        )}
      </TableBody>
    </Table>
  );
}
