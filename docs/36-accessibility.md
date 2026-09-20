# 36 · Accessibility: WCAG 2.2 AA Baseline & Automated Enforcement

Status: Design (RUN-255). Docs-only PR; implementation follows in phased PRs after merge.

## 1. Summary

Accessibility in the web UI is currently incidental: good outcomes come from
vendor primitives and one-off fixes (RUN-256 contrast work, the docs/30 form
error pattern), not from a product rule. This doc defines a product-wide
accessibility story with an automated enforcement layer so that AA compliance,
once reached, cannot silently regress.

Target bar: **WCAG 2.2 AA** for the supervisor web UI, on the two engines we
ship (Chromium E2E, plus jsdom unit semantics), with NVDA + Chrome and
VoiceOver + Safari as the manual screen-reader reference pairs.

Three phases, each shippable on its own:

1. **Audit** — axe-core baseline across every route in both themes, a
   keyboard-only pass, and a triaged findings table appended to this doc.
2. **Fix by area** — concrete per-surface patterns (§5), from focus
   management to live-region policy.
3. **Enforcement** — `@axe-core/playwright` scans gating the E2E suite on
   serious/critical violations, axe in vitest for mounted composites, no new
   CI workflow (rides the existing gates).

## 2. Scope & non-goals

**In scope:** the embedded React web UI (`web/`) — every route, both themes,
both roles (admin, viewer). Auth surfaces (login, onboarding, passkey
ceremony UI) are included; the WebAuthn browser APIs themselves are not
accessibility-relevant beyond their UI affordances.

**Non-goals:**

- WCAG AAA (contrast beyond AA, sign language, extended audio description).
- RTL / i18n — `lang="en"` is fixed; translated UI is a separate future story.
- Rebuilding vendor primitive behavior — Base UI already implements the
  WAI-ARIA patterns (dialog focus trap/restore, menu/tooltip keyboard models,
  toast live regions). We audit *usage*, not the library.
- Accessibility of the runner image or CLI-adjacent surfaces.

**Relationship to hotkeys (RUN-254):** complementary. Hotkeys are
power-user affordances; this doc's keyboard work is about baseline
operability (focus order, visible focus, Esc/Enter/Space semantics). No
dependency in either direction; implementation PRs must not conflict because
both touch `log-terminal.tsx` and table headers — whichever merges second
rebases.

## 3. Current state (code-audited inventory)

### 3.1 Already in place

| Area | Evidence |
| :--- | :--- |
| Dialog/AlertDialog focus trap, restore, Esc; menu/tooltip keyboard patterns | Base UI primitives via shadcn (`web/src/components/ui/*`) |
| Toast close-button labels, decorative icons `aria-hidden` | `ui/toast.tsx` |
| Form errors: `aria-invalid` + `aria-describedby` paired to a stable error id | `lib/forms/fields/*`, `lib/forms/fields/form-error.tsx` (docs/30) |
| Contrast tokens at AA in both themes (light theme realigned in RUN-256) | `index.css` oklch tokens; documented deltas in docs/09 |
| `<html lang="en">`, `<header>`/`<main>` landmarks in the app shell | `web/index.html`, `layout/app-shell.tsx` |
| Theme toggle and nav links carry accessible names | `app-shell.tsx` |
| LogTerminal pause / auto-scroll / stream filters expose state via `aria-pressed` | `terminal/log-terminal.tsx` |
| Sortable header buttons have `aria-label` | `lib/tables/sortable-header.tsx` |

### 3.2 Known gaps (from code reading; Phase 1 confirms and quantifies)

- **No per-route `document.title`** — every page is "Runnero"; screen-reader
  users cannot orient, browser history is unusable.
- **No route-change announcement** and **no skip link** to `<main>`.
- **No `aria-sort`** on sortable table headers (name is announced, state is not).
- **LogTerminal live-region policy undecided** — streaming log text in the DOM
  with no `role`/`aria-live` decision; today it is inert to screen readers.
- **Charts have no text alternative** (recharts SVGs; queue latency, capacity).
- **Pinned-column tables** (`data-table.tsx` `pinFirst`) — sticky clipping may
  cut focus outlines during horizontal scroll; unverified.
- **Heading hierarchy, icon-only button naming, focus-visible rings** —
  present in some places, unswept product-wide.
- **No automated enforcement** — nothing fails CI on regression.

## 4. Phase 1 — Baseline audit

### 4.1 Automated scan (report-only)

New spec `tests/e2e/specs/15-a11y-scan.spec.ts` using `@axe-core/playwright`.
In the audit PR it **writes findings into the HTML report but does not fail
the run** (enforcement flips in Phase 3, §6.1).

Scan matrix (helper-driven, lives in one spec):

| Route set | Roles | Themes |
| :--- | :--- | :--- |
| `/login`, `/onboarding` (step 1) | unauthenticated | light, dark |
| `/` dashboard, `/pools`, `/pools/:id` (default tab), `/pools/:id?tab=config`, `/logs`, `/history`, `/history/:id`, `/profiles`, `/renovate`, `/settings` (admin default = constraints), `/settings?tab=users` | admin | light, dark |
| `/`, `/pools`, `/pools/:id`, `/logs`, `/history`, `/history/:id`, `/renovate`, `/settings` (viewer default = security) | viewer | light, dark |

Theme is pinned per scan by seeding `localStorage["runnero-theme"]`
(`use-theme.ts` storage key) before `goto` — no reliance on OS preference.
Route settle: each scan waits for the route's data to render (existing
fixtures' waiting patterns) before `AxeBuilder.analyze()`.

Rule set: tags `wcag2a`, `wcag2aa`, `wcag22aa`, `best-practice`.
`disableRules` entries are allowed only with an inline comment naming the
finding, the justification, and an expiry (waiver policy in §6.1).

### 4.2 Keyboard-only pass

Documented checklist (appendix to docs/13 or this doc), executed per
focus-prone surface — onboarding wizard, login/passkey, pool wizard modal,
log terminal, history table with pinned column + sortable headers, toasts:

1. Tab order is logical; no keyboard traps outside intentional modal focus.
2. Every control operable (Enter/Space; menus per Base UI patterns; Esc closes).
3. Focus is always visible, including after dialog close (restore target)
   and during pinned-column horizontal scroll.
4. Focus does not jump on route change (document stays usable); wizard step
   changes move focus deliberately.
5. `:focus-visible` rings render in both themes.

### 4.3 Screen-reader smoke (manual)

Two surfaces only, chosen for complexity — pool wizard modal and log
terminal — on NVDA + Chrome and VoiceOver + Safari. Checklist: landmarks and
headings navigate correctly; dialog name and role announced; form errors
announced on submit; toast announcements land; log terminal's decided
behavior (§5.4) holds.

### 4.4 Triage & baseline record

Findings land in a new "Appendix A — Baseline findings" section of this doc:
per finding — surface, WCAG SC, axe rule or manual origin, impact
(minor/moderate/serious/critical), fix phase mapping. The audit PR records
the baseline; later PRs tick items off. The appendix is deleted when the
last item ships.

## 5. Phase 2 — Fix by area

### 5.1 Route titles & announcements

- Per-route titles via TanStack Router route context / a small
  `usePageTitle(title)` hook in each page component:
  `document.title = "<Page> · Runnero"`. Detail pages include the entity
  (`"<pool name> · Pools · Runnero"`).
- One `role="status" aria-live="polite"` visually-hidden region in the app
  shell; on route change it announces the new page title. Modest, standard,
  no behavior change for other users.

### 5.2 Skip link & landmarks

First focusable element in the app shell: "Skip to content" →
`<main id="main-content">`, visible on focus. Landmarks already exist
(§3.1); nav gets an accessible name it already effectively has via links.

### 5.3 Tables

- `sortable-header.tsx` sets `aria-sort` on the `<th>` from
  `column.getIsSorted()`: `false → "none"`, `"asc" → "ascending"`,
  `"desc" → "descending"`. The existing `aria-label` stays for the button.
- Pinned column (`pinFirst`): verify focus outline visibility under the
  sticky clip; add `outline-offset` / scroll-on-focus as the audit dictates;
  row action menus stay keyboard-reachable (Base UI menu — confirm in audit).
- Server-paged tables announce result context via the page controls'
  accessible names (e.g. "Page 2 of 7") — naming sweep, not structure.

### 5.4 LogTerminal live-region policy — decision

The terminal viewport (a DOM log list, not a canvas) gets:

- `role="log"` on the viewport container.
- **`aria-live="off"`** — permanently. Rationale: uncontrolled polite
  announcement of streaming log lines is noise that makes the page unusable
  for screen-reader users; nobody wants live dictation of stdout. Instead:
  pause + auto-scroll controls (already `aria-pressed`, get explicit
  accessible names where icon-only) let keyboard/SR users stop the stream
  and read statically; search/filter results are announced by the same
  route-level live region when the view changes deliberately.
- `aria-busy="true"` while the initial capture loads; "No matching log
  lines" empty state is regular text (already is).
- The decided policy and its rationale are documented here and in the
  component header so future contributors don't "helpfully" enable polite
  streaming.

This is the pragmatic AA answer: operable, predictable, and honest about
what live log streaming means for SR users.

### 5.5 Forms & validation

- Sweep: every field primitive carries the `aria-invalid` +
  `aria-describedby` error pairing (text/password/checkbox have it;
  textarea/select/native-select parity check).
- Submit buttons set `aria-busy` while pending (exists in some forms —
  sweep to the shared `submit-button.tsx` so it's everywhere at once).
- Wizard steps: on failed step validation, focus moves to the first invalid
  field; step headings are real headings (`h2`) so SR users can navigate.
- Login/inputs get `autocomplete` hints (`username`, `current-password`,
  `new-password`) — UX hardening with security upside (§7).

### 5.6 Charts & data alternatives

Every chart card (dashboard queue latency, capacity health) gets an
accessible name; the SVG is `aria-hidden` (decorative), and the card carries
a visually-hidden current-state summary built from the same query data
(e.g. "Queue latency p95 last 7 days: 42 s, trend rising"). Where the same
data already exists as a table elsewhere, the summary links to it instead
of duplicating.

### 5.7 Dialogs, wizard, menus, tooltips

Base UI provides trap/restore/keyboard patterns. Fix-by-audit items:
initial focus lands on the first meaningful control (not the close button);
AlertDialog confirms are keyboard-first; tooltips are not the only carrier
of essential information (icon buttons must have accessible names, §5.8);
pool wizard's step indicator exposes current step (`aria-current="step"`).

### 5.8 Icon-only buttons & naming sweep

Product-wide sweep driven by axe's `button-name`/`link-name` rules in
Phase 1 output: every icon-only control gets `aria-label`; decorative
icons stay `aria-hidden` (convention already established in toast/nav).

### 5.9 Focus visibility sweep

Confirm `:focus-visible` rings on every interactive primitive in both
themes, including badge-dense rows and the terminal header controls. Fix
with Tailwind ring utilities per primitive; no global reset.

### 5.10 Login, passkey & auth surfaces

Labels associated (they are — audit confirms), error messages announced on
failed login (route live region), passkey ceremony prompts carry accessible
names, and the virtual-authenticator E2E flow keeps working unchanged
(§7 — no ceremony change).

## 6. Phase 3 — Automated enforcement

### 6.1 E2E gate

The audit spec flips to enforcing: `AxeBuilder` with tags
`wcag2a`, `wcag2aa`, `wcag22aa`; the test asserts **zero violations with
impact serious or critical** over the full §4.1 matrix. Moderate/minor
findings are reported into the HTML report without failing (visibility
without flakiness).

**Waiver policy:** a `disableRules` entry (or skipped route) requires an
inline comment with: the finding reference, why it cannot be fixed now
(vendor limitation with an upstream issue link, or documented exception
like destructive-tinted contrast in RUN-256), and a review date. Waivers
are re-reviewed when the dependency bumps. Silent exceptions are not
allowed — the gate exists precisely to make exceptions explicit.

**Budget:** ~20 URLs × 2 themes ≈ 40 scans; at ~1.5–3 s per settled scan
plus route load, the spec adds roughly 3–4 minutes to the containerized
suite (playwright playtime today: 1.6 m for 31 tests). Accepted; if it
grows, the scan is the first candidate to split into its own Make target
(`test-e2e-a11y`) that CI runs in the same workflow job.

### 6.2 Vitest gate

`axe` (jsdom-runnable subset) in unit tests for the composites where
semantic regressions start: form fields (default + error state), data-table
(sorting + pinned column), LogTerminal controls, toast viewport. Layout-
dependent rules (`color-contrast`) are disabled in jsdom — contrast is
owned by tokens (RUN-256) and the E2E scans. Scope stays small: these are
shared components, so a handful of files cover most of the app.

### 6.3 CI wiring

No new workflow. The scan spec runs inside `make test-e2e` (already a PR
gate), the vitest additions inside `make test-web` (already a PR gate).
Docs: docs/13 gains an "Accessibility scanning" section (matrix, waivers,
local run instructions); docs/09 gains the a11y conventions summary
(live-region policy, naming rules, `aria-sort` pattern).

## 7. Security implications

- **No runtime, protocol, or auth changes.** All work is frontend
  presentation semantics plus dev-time tooling.
- **Tooling boundary:** axe scans run exclusively inside the containerized
  E2E stack with seeded credentials — the same boundary as every existing
  spec; never pointed at real deployments.
- **New dependencies are dev-only:** `axe-core`, `@axe-core/playwright`
  (Playwright image + `web` devDependencies). Pinned versions; zero runtime
  bundle impact — verified by the existing build gate and the supervisor
  image fingerprint practice (embedded asset names must not change size
  class; devDeps never reach `pnpm build` output except the vitest axe
  usage, which is test-only).
- **Auth-adjacent UX is hardening, not weakening:** `autocomplete` hints on
  login/change-password fields, clearer error announcement, and focus
  management in the passkey UI do not alter session semantics, rate
  limiting, or the WebAuthn ceremony (docs/32, docs/34 untouched).

## 8. Testing & rollout

| PR | Content | Gate effect |
| :--- | :--- | :--- |
| A (this) | Design doc + README roadmap | none |
| B — audit | `15-a11y-scan.spec.ts` report-only; keyboard checklist; baseline findings appended to docs/36 | scan reports, does not fail |
| C — core fixes | titles + announcements, skip link, `aria-sort`, icon-button naming sweep, focus visibility sweep | still report-only; manual verification per repo rules |
| D — streaming & forms | LogTerminal policy, charts summaries, forms/wizard polish, auth surfaces | still report-only |
| E — enforcement | flip gate (serious/critical fail), vitest axe, docs/13 + docs/09 updates, README: roadmap entry removed, Features bullet added | blocking |

Phases C/D can split further by audit findings; each PR keeps its docs in
the same PR per repo rules. E2E creds (`admin` / `AdminPassword123!`) and
the viewer fixture already exist in `tests/e2e/fixtures.ts`; the viewer
scan set reuses flow 13's promotion pattern in reverse (seeded viewer).

## 9. Risks & mitigations

| Risk | Mitigation |
| :--- | :--- |
| axe false positives (decorative SVGs, duplicates) | rule-specific `disableRules` with waivers (§6.1); decorative marking convention (§5.8) |
| Flaky scans on streaming routes (logs, dashboard live regions) | scans run against seeded fixtures and wait for settled route state; log terminal scanned in its loaded/paused state |
| E2E duration growth | budgeted 3–4 min (§6.1); escape hatch to dedicated target |
| Base UI upgrades shifting semantics | versions pinned; enforcement catches drift on the next scan |
| jsdom rule limits give false confidence | vitest axe scoped to semantic rules; contrast/visual owned by tokens + E2E |
| Matrix drift (new routes forget the scan) | matrix built from a single helper list; adding a route without a scan entry fails the "every route scanned" completeness assertion |

## 10. References

- RUN-255 (umbrella issue), RUN-254 (hotkeys — complementary).
- WCAG 2.2 AA; axe rule engine (`axe-core`), `@axe-core/playwright`.
- docs/09 (frontend design, token/contrast history from RUN-256),
  docs/30 (form validation & error rendering), docs/31 (table toolkit),
  docs/13 (E2E suite conventions).

## Appendix A — Baseline findings (RUN-262)

Recorded by the first full run of `tests/e2e/specs/15-a11y-scan.spec.ts`
(axe-core 4.13.0; tags `wcag2a`, `wcag2aa`, `wcag22aa`, `best-practice`)
against the seeded E2E stack: 21 page scans per theme across
unauth/admin/viewer, onboarding recorded as a justified skip. Full
per-URL data rides the HTML report (`axe-<role>-<theme>` text + `-raw`
JSON attachments); the table below deduplicates across roles/themes and
attributes each finding via the run's node targets plus code inspection.

| # | Rule (impact) | Where | Attribution | Fix phase |
| :--- | :--- | :--- | :--- | :--- |
| A2 | `color-contrast` (serious) | every scanned page, both themes, 2–14 nodes each | Flagged classes include `.bg-muted`, `.text-muted-foreground/70`; RUN-256 realigned tokens but component-level pairs still fail | RUN-266 |
| A5 | `heading-order` (moderate) | /, /pools, /pools/:id?tab=config, /settings, /settings?tab=users | Heading level skips in page/card headers | RUN-267 |
| A7 | Onboarding wizard | not scannable | Recorded skip: the seeded database redirects /onboarding; wizard coverage via the §4.2 keyboard pass and flows 01/02 | manual |

**Fixed in RUN-263** (verified by the scan staying report-only green and
the post-fix keyboard pass):

- A1 `button-name` (critical) — the six filter `SelectTrigger`s on
  /pools and /history carry `aria-label`s.
- A3 duplicate/nested `<main>` — `SidebarInset` is now a `<div>`; the
  shell's `<main id="main-content">` is the only main.
- A4 (partial) — /login and /onboarding render their own `<main>`;
  /login now scans **clean** in both themes. The remaining `region`
  findings (×4 per app page, moderate) are the sidebar brand block and
  scroll container, the Base UI toast portal, and the skip link itself —
  all fixed-position or deliberately landmark-preceding chrome, accepted
  as primitive-managed.
- A6 `empty-table-header` — logs boot/removals action columns and the
  users-table actions column carry sr-only names.

§5.1 titles + route-change announcements, §5.2 skip link, and §5.3
`aria-sort` shipped in the same PR. Measured effect (same matrix):
admin pages 64 rule hits / 136 nodes → **27 / 106** (light) and 138 →
**108** (dark); viewer 45 / 102 → **19 / 80** and 107 → **85**;
/login 2 rules → **0**. Zero critical and zero minor findings remain;
the residual serious finding is color-contrast (RUN-266) and the
residual moderate heading-order (RUN-267).

Dark/light deltas are confined to `color-contrast` node counts (e.g.
/pools 5 light vs 11 dark; /history/:id 14 light vs 10 dark) — the
structural findings are theme-independent. This appendix is deleted when
the last item ships (§4.4).

### §4.2 keyboard-only pass — first results (Chrome, assistant-driven)

Executed against the seeded debug stack via real CDP key events:

- **Tab order** logical on the shell: nav links (Dashboard → … →
  Settings) → user menu → sidebar toggle → theme buttons → content.
  Every stop carries an accessible name.
- **Focus visibility**: `:focus-visible` rings render (screenshot-verified
  on the theme toggle; shell links and buttons all report focus-visible).
- **Pool wizard modal**: Enter on "+ Add Runner Pool" opens it; initial
  focus lands on the first input (not the close button); the focus trap
  held across 14 Tabs (input → select trigger → Cancel → Close → wraps);
  Esc closes and restores focus to the trigger.
- **LogTerminal controls**: `Auto-scroll: ON/OFF` and stream filters
  (`All`/`stdout`/`stderr`) expose `aria-pressed`; Enter toggles
  auto-scroll, the label and state update, focus is retained.
- **Sortable headers**: operable via keyboard (native button), but no
  `aria-sort` state — confirmed live (**fixed in RUN-263**: DataTable now
  sets `aria-sort` on every header cell).
- **No skip link**: first Tab lands on the nav (**fixed in RUN-263**:
  "Skip to content" is now the shell's first focusable element).
- The `aria-live="polite" region` on every page is the Base UI toast
  viewport (`pointer-events-none fixed inset-…`) — toast announcements
  already covered by the primitive (§3.1).

Not executable here: the **§4.3 screen-reader smoke** (NVDA+Chrome,
VoiceOver+Safari) — owner-side manual task, tracked with this issue.
