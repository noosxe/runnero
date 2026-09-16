import { coreFeatures, createTableHook, tableFeatures } from "@tanstack/react-table";

/**
 * App-wide feature set (docs/31 §3): deliberately minimal — core only.
 * Optional features (row sorting, pagination) get composed here in later
 * phases so every table shares one typed feature set instead of each
 * surface picking its own.
 *
 * `columnMeta` is the phantom slot giving columns typed `meta` fields;
 * the DataTable shell reads these to apply per-column cell/header classes.
 */
export const appTableFeatures = tableFeatures({
  ...coreFeatures,
  columnMeta: {} as {
    /** Extra class for the rendered <TableHead>. */
    headerClassName?: string;
    /** Extra class for every rendered <TableCell> in this column. */
    cellClassName?: string;
  },
});

/**
 * App-scoped table hook (docs/31 §4.1): createTableHook bakes the shared
 * feature set in, so route tables only pass columns/data/state. This is
 * the scoped-composable pattern from the TanStack shadcn/base example —
 * the hook owns state and semantics; markup stays in our ui/table
 * primitives via the DataTable shell.
 */
export const { useAppTable } = createTableHook({
  features: appTableFeatures,
});

export type AppTableFeatures = typeof appTableFeatures;
