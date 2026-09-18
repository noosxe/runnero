# TanStack Table Migration — Headless Table Core for All Web Tables

Status: **Design Phase** (docs-only PR; implementation tracked on Linear after merge).

Every table surface in the web UI is currently hand-wired: shadcn `Table`
primitives (`ui/table.tsx`) plus per-page ad-hoc logic for columns, empty
states, filters, and pagination controls. This doc proposes migrating all
surfaces to **TanStack Table v9** (`@tanstack/react-table`) as the headless
core, using the integration blueprint from the official
[kitchen-sink shadcn/base example](https://tanstack.com/table/latest/docs/framework/react/examples/kitchen-sink-shadcn-base)
— while preserving markup, `data-testid` contracts, and all existing
interaction behavior unless a change is explicitly called out.

## 1. Problem

Six route files render tables today (≈44 column headers total):

| Surface | File | Columns | Data behavior |
|---|---|---|---|
| Recent jobs | `routes/dashboard.tsx` | 6 | client snapshot from `GetJobHistory(limit)` |
| Job history | `routes/history.tsx` | 9 | server-paged (`limit`/`offset` + `search` + `status`), CSV export |
| Boot logs | `routes/logs.tsx` (boots tab) | 5 | server list, client filters |
| Removal records | `routes/logs.tsx` (removals tab) | 8 | server **cursor** paging (`page_size`/`cursor` + reason/pool/runner/since/until filters), newest-first |
| Runners | `routes/pool-detail.tsx` | 7 | live-updating (streaming/polling) |
| Renovate runs | `routes/pool-detail.tsx` | 5 | server list |
| Renovate pools | `routes/renovate.tsx` | 5 | server list |
| Constraint overrides | `routes/settings.tsx` | 4 | server list |

Consequences of the current approach:

- **Duplicated plumbing** — every surface re-implements header rows, empty
  states, row action cells, and (where present) paging/filter controls with
  slightly different idioms.
- **No shared sorting story** — zero tables are sortable today; adding
  sorting per-surface means re-solving it six times.
- **Server paging has no table-level contract** — `history.tsx` and
  `logs.tsx` each hand-manage offsets/cursors against bespoke UI.
- **Consistency risk** — new tables (there will be more: audit logs, auth
  profiles) inherit whatever the author copies, not a tested pattern.

## 2. Goals and non-goals

### Goals

1. One headless table core (`@tanstack/react-table` v9) behind a thin shared
   toolkit in `web/src/lib/tables/`, in the same spirit as the RUN-221 forms
   toolkit (`web/src/lib/forms/`).
2. All six surfaces migrated; markup keeps coming from our existing
   `ui/table.tsx` shadcn primitives (`flexRender` for header/cell content).
3. Server-authoritative paging preserved: history (offset) and removals
   (cursor) keep their wire semantics; the table is configured
   `manualPagination: true` and never slices client data behind the server's
   back.
4. Explicit, flagged capability changes: client-side column sorting where it
   adds value (called out per phase, not slipped in).
5. Every phase independently shippable and revertable (one PR per phase).

### Non-goals

- Row selection, column visibility/pinning/resizing, column ordering, global
  (fuzzy) search, virtualization, devtools plugin — the kitchen-sink example
  demonstrates these but no surface needs them today. Follow-ups, not scope.
- Any proto/RPC change: pagination and filtering stay exactly where they are
  on the wire.
- Backend changes of any kind.

## 3. Library decision

`@tanstack/react-table` **v9** (`^9.2.4`, `latest` dist-tag, first stable
v9 released 2026-08; zero runtime dependencies, MIT, React-19 compatible).

Two deliberate version/API choices:

- **v9, not v8.** The linked shadcn-base example and the current docs are
  written against the v9 composable API (`tableFeatures({ ... })` +
  `useTable` + `createTableHook`). v8's `useReactTable` API is legacy in the
  docs; starting on v8 would sign us up for a second migration.
- **Scoped composable adoption.** We adopt the composable *hook* pattern — a
  single app-wide `createTableHook`-built `useAppTable` (mirroring the
  example's `useAppTable`) — but we do **not** adopt its full component
  sub-context layer (`table.AppTable`, `table.FilterList`, `table.SortList`,
  devtools, stress-test extras). Our tables are small (4–9 columns) and their
  chrome is bespoke (pool action menus, reason badges, capture links); a
  shared presentational `DataTable` over our existing primitives keeps that
  customization simple while the hook owns state/semantics.

## 4. Architecture

### 4.1 Shared toolkit — `web/src/lib/tables/`

```
web/src/lib/tables/
├── index.ts              # public surface
├── use-app-table.ts      # createTableHook-wrapped hook: core row model +
│                         #   optional sortedRowModel; defaults all tables share
├── data-table.tsx        # <DataTable table={t}> presentational shell:
│                         #   shadcn Table + flexRender headers/cells + empty state
├── pagination-footer.tsx # offset-style footer (history); cursor-style (removals)
├── sortable-header.tsx   # clickable header with sort indicator (opt-in per column)
└── csv-export.ts         # history's CSV builder, moved + table-model-driven
```

Conventions (matching the forms toolkit):

- Column defs are declared with `createColumnHelper<TRow>()`, colocated in
  the route file or a sibling `columns.tsx`, `accessorKey`/`accessorFn` per
  column, `cell` renderers returning the existing JSX (badges, menus, links).
- All `data-testid`s are preserved on the rendered rows/cells (cell
  renderers own them), so the E2E suite's selectors keep working.
- `DataTable` takes the `useAppTable` instance; routes keep owning data
  fetching (TanStack Query hooks) and pass the row array in.
- **`pinFirst` (opt-in):** pins the first column (`position: sticky; left: 0`,
  opaque `bg-card`, so row content slides beneath it) for dense tables that
  overflow horizontally — table cells are globally `whitespace-nowrap`, so a
  wide table always scrolls inside its card; the pin keeps the row identity
  visible while it does. A separator on the pin's right edge uses the inset
  box-shadow recipe from TanStack's kitchen-sink shadcn example
  (`getCommonPinningStyles`) and renders only while the table is actually
  scrolled. History's job table opts in; the shell fix that stops wide pages
  from overflowing the document lives on `SidebarInset` (`min-w-0`,
  `components/ui/sidebar.tsx`).

### 4.2 Server-side paging mapping

The v9 pagination guide's manual patterns, applied per wire shape:

- **History — offset paging** (`GetJobHistoryRequest.limit/offset`, total
  count derivable): controlled `pagination` state `{ pageIndex, pageSize }`,
  `manualPagination: true`, `rowCount` from the response; `manualFiltering:
  true` — the existing search/status inputs write through to the query key
  exactly as today. `placeholderData: keepPreviousData` already in use stays.
- **Removal records — accumulated cursor pages** (`ListRemovalRecordsRequest.cursor`,
  no total): the page already pages via `useInfiniteQuery` with a
  "Load more" button and never shows page numbers, so there is no table
  pagination state to mirror. The table is display-only over the
  accumulated pages (`pages.flatMap(...)`); paging stays hook-driven and
  the filter UI (reason/pool/runner/since) unchanged, still server-side
  via query key. (Revision from the original design, which sketched
  `pageCount: -1` table pagination — unnecessary for a Load-more UX.)
- **Everything else:** no pagination state at all — `manualPagination` not
  set, full row array rendered (sizes are 10s of rows, capped server-side).

### 4.3 Live tables (runners)

The runners table on pool detail updates live. TanStack Table is stateless
over the `data` prop, so updates are just re-renders. To keep per-tick cost
flat: column defs module-level constants (never rebuilt per render), data
identity managed upstream by the existing streaming hook, and memoized row
models from the core (v9 memoizes internally). No virtualization — runner
counts per pool are small.

### 4.4 Sorting enablement (the one behavior change)

New capability, explicitly flagged and added **last**:

| Table | Sortable columns | Notes |
|---|---|---|
| Job history | Started At, Completed At, Duration, Queue Wait | client-side (`getSortedRowModel`) over the current page only — server ordering unchanged; sortable-header affordance per shadcn pattern |
| Runners | State, Uptime | same, per-page |
| All others | none | keep static headers |

Anything that must sort across the whole dataset is a server concern and
stays out of scope (would need RPC changes; follow-up if ever needed).

## 5. Migration plan (phased, each independently shippable/revertable)

Parity-first: every phase keeps visuals, testids, and interactions identical
except where a phase explicitly lists a flagged change. Each phase is one PR
with vitest + E2E updates included.

| Phase | Scope | Surfaces | Flagged changes |
|---|---|---|---|
| **0 — Foundation + pilot** | dep `@tanstack/react-table@^9.2.4`; `lib/tables/` toolkit; unit-test harness; migrate the smallest table | settings constraint overrides (4 cols) | none |
| **1 — Static client tables** | exercise the toolkit on plain lists | dashboard recent jobs, renovate pools, pool-detail renovate runs | none |
| **2 — Server-paged tables** | manual pagination (offset + cursor recipes), filter write-through, CSV export from table model | job history, logs removals | none (paging behavior identical) |
| **3 — Live tables + sorting** | streaming data, `sortable-header`; last PRs | logs boots, pool-detail runners, then sorting on history + runners | **column sorting appears** (per §4.4) |

Phase 3 deliberately splits "make it live" (parity) from "add sorting"
(feature) into separate commits so a sorting regression is trivially
revertable.

## 6. Testing strategy

- **Unit (vitest):** per-surface column tests — render the `DataTable` via
  the toolkit with fixture rows; assert header labels, cell content
  (badges/menus/links), testid preservation, empty state. Sorting tests for
  phase 3 (asc/desc toggle, indicator). CSV export golden tests.
- **E2E:** no new specs required; existing flows 03 (dashboard), 06
  (renovate), 08 (pool edit), 09 (logs) already traverse the migrated
  surfaces and assert via testids — they double as regression coverage. UI
  work is exercised through the E2E suite before the PR body's human-check
  list, per repo policy.
- **Gates:** standard gate line per phase.

## 7. Security implications

- **No new wire surface:** pure client-side refactor; no proto/RPC changes,
  no new endpoints, no credential handling.
- **Server remains authoritative** for paging/filtering on history and
  removals (`manual*` flags prevent client-side data fabrication; the client
  never widens a server filter locally).
- **Dependency supply chain:** `@tanstack/react-table` v9 is zero-runtime-
  dependency, MIT, pinned `^9.2.4`; pnpm lockfile update reviewed in the
  phase-0 PR.

## 8. Acceptance criteria

1. No route file contains hand-rolled `TableHeader`/`TableBody` markup or
   bespoke paging/filter state machines; all render via `lib/tables/`.
2. All existing `data-testid`s and E2E flows pass unchanged (except where
   phase 3 adds sorting affordances).
3. History and removals paging behave identically on the wire (verified by
   E2E + manual page-size boundary checks).
4. Sorting works only where §4.4 grants it, client-side over the current
   page, with no change to server query semantics.
5. `docs/09` frontend design doc updated with the table toolkit pattern;
   README roadmap entry moved to Features at final-phase merge.

## 9. Out of scope / follow-ups

- Row selection, column visibility, pinning, resizing, virtualization,
  global search, devtools — adopt per-surface later if a need appears.
- Server-side sorting RPCs (would need proto changes).
- Audit-log / auth-profile tables (future features) get the toolkit for
  free; not part of this migration.
