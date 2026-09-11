# 27. shadcn/ui Component Migration

| | |
| :--- | :--- |
| Status | Design Phase (docs-only PR) |
| Linear | [shadcn/ui Migration](https://linear.app/runnero/project/aio-supervisor-5f33f8096608) milestone — RUN-167 … RUN-178 |
| Related | docs/09 (frontend design spec — updated by each phase) · `.pi/skills/shadcn` (bundled skill — source of the CLI workflow and conventions in §3) |
| Touches | `web/` (`components.json`, `src/index.css`, font wiring, `src/lib/utils.ts`, `tsconfig.app.json`, `vite.config.ts`, oxlint/oxfmt config, all `routes/` + `components/`), `README.md`, docs/09 |

## 1. Problem

Every UI primitive in the web control plane is hand-rolled inline in the file
that needs it. A discovery pass over `web/src` (Sept 2026) found:

- **103 `<button>` instances with 25 distinct class signatures** — primary,
  danger, outline, ghost and icon variants are re-implemented per file with
  drifting spacing, radii and colors (`rounded-lg` vs `rounded-xl`,
  `py-2` vs `py-2.5`, `px-4` vs `px-5` for the same intent).
- **5 copy-pasted modal shells** (identical
  `fixed inset-0 z-50 … bg-slate-950/60 backdrop-blur-xs` markup) with **no
  Escape handling, no focus trap, no `role="dialog"`**. The entire codebase
  contains 14 `aria-`/`role` attributes.
- **45 `rounded-full` pills/badges**, **18 native `<table>` blocks** across 8
  files, **54 inputs / 9 native selects / 2 textareas** with no shared field
  wrapper, and 13 native `title=""` tooltips as the only tooltip mechanism.
- `clsx` + `tailwind-merge` are declared in `package.json` but never used — a
  `cn()` helper was planned and never built.

The symptom that triggered this design: the collapsed sidebar's brand header
(32px padding + 36px logo + 12px gap + 28px toggle = 108px) overflows the 80px
`w-20` rail, so the overflow-hidden logo wrapper is squeezed and the expand
button collides with the clipped logo — layout-critical primitives rebuilt per
feature with no shared contract.

## 2. Goals

- Adopt **shadcn/ui** as the single source of UI primitives, with the
  owner-selected preset `b7QqImqdoe` (§4.1) as the design language.
- Prefer ready-made registry components over custom UI wherever an equivalent
  exists; custom UI survives only where nothing fits (the log viewer, see
  Non-goals).
- Get accessibility for free: focus management, Escape handling, ARIA wiring
  on dialogs, menus, tooltips and form controls.
- Kill the duplication: one Button, one Dialog, one Select, one Table, one
  Tooltip — themed once via CSS variables.
- Stay on the existing stack: React 19, Vite, **Tailwind CSS v4**, lucide-react.

### Non-goals

- No redesign beyond the owner-selected preset (`b7QqImqdoe`, §4.3) — the
  preset defines the new visual language.
- No form library (react-hook-form / zod) — forms keep their controlled state.
- No backend, RPC (docs/08) or routing changes — this is strictly the
  presentation layer.
- The log viewer's core stays bespoke (`components/terminal/log-terminal.tsx`:
  streaming, autoscroll, search over `LogChunk` streams) — no registry
  equivalent exists. Only its toolbar chrome adopts shadcn primitives in the
  normal sweeps (§5, RUN-178).

## 3. Workflow rules (binding)

1. **CLI-only spawning.** Components enter the repository exclusively via
   `pnpm dlx shadcn@latest add <component>` run inside `web/` — the project's
   package runner, matching the bundled shadcn skill. No manual copy-paste
   from docs, GitHub, or other projects; the skill is explicit that raw
   upstream files are never fetched by hand.
2. **Generated code is read-only.** Files under `web/src/components/ui/` are
   never edited. Customization happens through:
   - **composition wrappers** in `web/src/components/` (e.g. preset `variant`
     props on top of the generated Button);
   - **CSS variables** in the global CSS file (the intended theming surface);
   - **config** (`components.json`, Tailwind theme).
   This is deliberately stricter than the shadcn skill, which treats "add a
   variant via `cva` in the component source" as a customization option — we
   do not adopt that option. Any exception requires a design decision
   recorded on a Linear issue and the product owner's approval — never an
   opportunistic edit.
3. Generated files are **excluded from oxlint/oxfmt** so the `make lint-web`
   gate stays green without ever touching them (§4.5).
4. **CLI workflow, every time** (per the shadcn skill):
   - `pnpm dlx shadcn@latest info` for project context; check installed
     components before adding — never re-add, never import un-added;
   - `pnpm dlx shadcn@latest docs <component>` for docs/examples before
     using a component's API — never design against remembered APIs;
   - `add --dry-run` / `--diff` to preview exactly what lands before writing;
   - the registry is explicit: `@shadcn` unless the owner names another;
   - upstream updates go through the skill's smart-merge flow (`--dry-run`
     → per-file `--diff` → merge); **`--overwrite` only with the owner's
     explicit approval**.

### 3.1 Adoption conventions (binding in migrated code)

The shadcn skill's critical rules apply to everything we write on top of the
primitives:

- **Semantic tokens only** — `bg-primary`, `text-muted-foreground`; no raw
  palette values (`bg-blue-600`) and no manual `dark:` overrides in migrated
  surfaces — light/dark comes from the CSS variables.
- Status colors become Badge variants or dedicated theme variables
  (`--success`, `--warning`) — never raw `emerald-*`/`amber-*` classes.
- `gap-*` instead of `space-y-*`; `size-*` when width equals height;
  `truncate` shorthand.
- `cn()` for conditional classes — no template-literal ternaries.
- Icons in Buttons carry `data-icon="inline-start|inline-end"` and no size
  classes; pending actions compose `Spinner` + `disabled` (Button has no
  `isPending`).
- Custom triggers use Base UI's `render` prop (the skill's base-vs-radix
  rule) — no Radix-style `asChild` in this codebase.
- Overlays own their stacking — no manual `z-*` on Dialog/Sheet/
  DropdownMenu/Tooltip; Dialog and Sheet always render a `DialogTitle`
  (`sr-only` when visually hidden).
- Forms use `FieldGroup`/`Field` (+ `FieldSet`/`FieldLegend` for groups,
  `InputGroup` for buttons inside inputs, `ToggleGroup` for 2–7 option
  sets); validation via `data-invalid` on the Field + `aria-invalid` on the
  control.
- `Avatar` always renders an `AvatarFallback`; callouts use `Alert`; empty
  states use `Empty`; separators use `Separator`, not `border-t` divs.

## 4. Architecture & integration

### 4.1 Initialization (RUN-167)

`pnpm dlx shadcn@latest init --preset b7QqImqdoe` in `web/` writes
`components.json` and the preset's theme variables into the global CSS file
(`src/index.css`), wires the preset fonts, and creates `src/lib/utils.ts`
(`cn()` — finally activating `clsx` + `tailwind-merge`).

The shadcn CLI requires the `@/` path alias, which the project does not have
yet: `tsconfig.app.json` gets `"@/*": ["./src/*"]` and `vite.config.ts` a
matching `resolve.alias`. Repo config files are not generated code; touching
them is allowed (and required) by rule (2).

**Preset decision (made).** The owner generated preset `b7QqImqdoe` at
[ui.shadcn.com/create](https://ui.shadcn.com/create?preset=b7QqImqdoe); the
code is opaque and passed to the CLI verbatim. Decoded via `preset decode`
for the record (never manually):

| Field | Value |
| :--- | :--- |
| style | `maia` |
| base color | `neutral` |
| theme | `blue` |
| chart color | `amber` |
| icons | `lucide` (matches the project) |
| fonts | Inter (body) · Source Sans 3 (headings) |
| radius | default |
| menu | subtle accent, default color |

Preset codes do **not** encode the primitive base — that is chosen once at
init (interactive prompt / `--base`). **Decision: Base UI** (the `base`
library) — shadcn's current default direction. Its components expose
`render`-prop composition (instead of Radix's `asChild`) and the `toast`
component replaces sonner (§4.2, RUN-176). Switching bases later means
reinstalling every component. Re-application of the preset (e.g. after
style drift) uses `apply --preset b7QqImqdoe`, which overwrites
preset-driven config, fonts, CSS variables and detected components.

### 4.2 Dependencies

`init` adds `class-variance-authority` (plus `clsx`/`tailwind-merge`, already
present); each component adds its Base UI primitives
(`@base-ui-components/react`) via pnpm with lockfile pinning. lucide-react
is already the icon set shadcn uses. All of it is tree-shakeable ESM —
unused variants do not ship.

### 4.3 Tokens (preset-owned)

The preset owns the design tokens: `init` emits the full OKLCH variable set
(`@theme inline` in `src/index.css`) — neutral surfaces, blue primary, amber
chart ramp, default radius — plus the font wiring (Inter / Source Sans 3).
We do **not** override token values with a hand-rolled palette; the preset is
the single source of visual truth, and the semantic utilities (`bg-primary`,
`text-muted-foreground`, …) pick it up automatically.

Dark mode needs no JS change: shadcn's `.dark` class strategy is already the
project's mechanism (`@custom-variant dark (&:where(.dark, .dark *))` in
`src/index.css`, toggled by `hooks/use-theme.ts`).

Two additions per the shadcn skill's conventions:

- Custom app colors that the preset does not define are added as theme
  variables in the same `@theme inline` block — never raw palette classes.
- The health pills' raw `emerald-*`/`amber-*` classes become proper theme
  variables (`--success`, `--warning`, harmonized with the preset's amber
  ramp), so migrated status badges use semantic tokens like everything else.

### 4.4 State and data flow

Unchanged. TanStack Router/Query, ConnectRPC streaming, and component-level
`useState` stay as they are; shadcn primitives are uncontrolled UI shells with
controlled bindings where the app already controls state.

### 4.5 Lint / format boundaries

`components/ui/**` is added to the oxlint and oxfmt ignore lists. Our own
wrappers and routes remain fully gated.

## 5. Component mapping

| Current hand-rolled pattern | shadcn replacement | Scale | Issue |
| :--- | :--- | :--- | :--- |
| 25 button class signatures | Button | 103 instances | RUN-170 |
| 45 `rounded-full` pills, stat cards | Badge, Card | 45 pills | RUN-169 |
| raw inputs / selects / textareas / checkboxes | Field primitives (Field/FieldGroup/FieldSet), Input, Textarea, Select, Checkbox, NativeSelect, InputGroup, ToggleGroup | 66 fields | RUN-171 |
| 5 copy-pasted modal shells | Dialog, AlertDialog | 6 overlays | RUN-172 |
| native `title=""` tooltips, ad-hoc actions | Tooltip, DropdownMenu | 13 tooltips | RUN-173 |
| 18 native `<table>` blocks, ad-hoc empty states | Table, Empty | 8 files | RUN-174 |
| hand-rolled app shell (collapsing sidebar bug) | Sidebar, Sheet, Avatar, Breadcrumb, Separator, ToggleGroup (theme switch) | app-shell | RUN-175 |
| inline notification banner, ad-hoc loaders | Alert (persistent), toast (transient, Base UI), Skeleton, Progress, Spinner | — | RUN-176 |
| hand-rolled SVG line chart | Chart (recharts) | 1 chart | RUN-177 |

## 6. Migration plan

One PR per issue, ordered by dependency and risk; RUN-167 blocks all others.
Each PR is a self-contained adopt-and-replace pass with green gates.

1. **RUN-167 — Foundation:** init with preset `b7QqImqdoe` (Base UI base),
   alias, `cn()`, preset tokens, lint exclusions; verified with a throwaway
   `button` spawn.
2. **RUN-170 — Button** (highest duplication, mechanical replace).
3. **RUN-169 — Badge & Card**; **RUN-171 — Form primitives** (Select is the
   one behavioral change: Base UI listbox replaces native `<select>`).
4. **RUN-172 — Dialog & AlertDialog** (biggest a11y win).
5. **RUN-173 — Tooltip & DropdownMenu**; **RUN-174 — Table**.
6. **RUN-175 — AppShell rebuild on Sidebar/Sheet**; supersedes and closes the
   collapsed-sidebar bug; collapsed state persists like `use-theme` does.
7. **RUN-176 — Toast/Alert/Skeleton/Progress**; **RUN-177 — Charts** (recharts
   adopted for the queue-latency chart).
8. **RUN-178 — Cleanup & close:** grep sweeps prove no bespoke duplicates
   remain; docs/09 + this doc reflect shipped state; README roadmap entry
   moves to Features.

## 7. Testing & verification

- Vitest suites keep running per phase; selectors are updated to the new
  markup as files are migrated. `make test-web` stays green on every PR.
- E2E (Playwright, docs/13) is untouched by the migration itself and remains
  the human-check vehicle for UI-facing PRs per AGENTS.md.
- Per AGENTS.md, visual verification (light + dark, desktop + mobile) is the
  product owner's on every adoption PR; PR bodies carry a human-check list.

## 8. Security implications

- **Supply chain:** `pnpm dlx shadcn@latest` executes the official shadcn CLI
  at dev time. Components are vendored into the repo at generation time and
  reviewed in the PR diff — there is no runtime registry dependency. Base UI
  runtime packages are pinned by `pnpm-lock.yaml` and kept current by
  Renovate. All commands run inside the Nix dev shell.
- Preset codes are opaque strings resolved by the official CLI (`init
  --preset`, `apply`); inspection only via `preset decode` — never
  hand-built or hand-decoded. The owner generated `b7QqImqdoe` at
  ui.shadcn.com/create.
- **No new attack surface:** the app already executes only in the operator's
  browser against the supervisor's authenticated ConnectRPC API (docs/05,
  docs/08). No credentials, tokens, or container-runtime semantics are touched.
- Base UI brings focus-trap/portal behavior the hand-rolled shells lack,
  removing the current risk of modals leaking focus/scroll behind them.

## 9. Risks

| Risk | Mitigation |
| :--- | :--- |
| Generated code drifts from our lint/format rules | Excluded from oxlint/oxfmt; never edited (§3, §4.5) |
| Select behavioral change breaks flows | RUN-171 is isolated; controlled state kept; owner walks wizard + onboarding |
| Base UI is newer than Radix (smaller ecosystem track record) | Decided (§4.1); the skill's base-vs-radix rules plus per-base `shadcn docs` URLs resolve API differences (`render` vs `asChild`, toast) |
| recharts adds ~100 kB gzipped for the queue-latency chart | Accepted (RUN-177): Charts consume the preset's `--chart-*` tokens, so future analytics widgets reuse the same foundation |
| Visual regressions across 10 routes | One surface class per PR; E2E + owner visual checks per PR |
