import { ChevronDown, ChevronUp, ChevronsUpDown } from "lucide-react";
import type { Column, RowData } from "@tanstack/react-table";
import type { AppTableFeatures } from "./use-app-table";

type SortableHeaderProps<TData extends RowData> = {
  column: Column<AppTableFeatures, TData, any>;
  children: React.ReactNode;
};

/**
 * Sortable column header affordance (docs/31 §4.4): a button toggling
 * asc → desc, with a direction indicator. Rendered from a column's
 * `header` renderer; only columns that opt in show it — every other
 * column keeps its plain header and stays unsortable.
 */
export function SortableHeader<TData extends RowData>({
  column,
  children,
}: SortableHeaderProps<TData>) {
  const sorted = column.getIsSorted();
  return (
    <button
      type="button"
      className="group inline-flex items-center gap-1 hover:text-foreground"
      onClick={column.getToggleSortingHandler()}
      aria-label={`Sort by ${typeof children === "string" ? children : column.id}`}
    >
      {children}
      {sorted === "asc" ? (
        <ChevronUp className="size-3" />
      ) : sorted === "desc" ? (
        <ChevronDown className="size-3" />
      ) : (
        <ChevronsUpDown className="size-3 opacity-40 transition-opacity group-hover:opacity-100" />
      )}
    </button>
  );
}
