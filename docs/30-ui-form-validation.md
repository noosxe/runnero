# UI Form Validation — TanStack Form Adoption

| | |
|---|---|
| **Issue** | RUN-216 (web: client-side validation for free-form inputs — blur validation that gates submit/next-step) |
| **Status** | Design phase — no implementation yet |
| **Scope** | Web frontend only (`web/src`); no protocol changes; no server changes |
| **Related** | docs/09 §frontend architecture (form conventions, `CodeInvalidArgument` mapping), RUN-147 (sentinel collision that surfaced this gap), docs/27 (shadcn/ui migration) |

## 1. Problem

Client-side validation today is ad-hoc: each surface hand-rolls its own
`useState` fields plus an error banner that is only computed when the user
takes a step-level action (wizard `handleNextFromStepN`, modal submit
handlers). Consequences:

- **No field-level feedback.** Errors appear as a banner after clicking
  Continue/Submit, not at the field that caused them, and never on blur.
- **Invalid state is reachable.** The concrete RUN-147 failure: the pool
  wizard's *Memory Swap → Custom…* input left empty trims to `""`, which the
  server reads as the *unset* sentinel — the pool silently ships with the
  Docker 2× daemon default, contradicting the picked mode. Nothing at the
  form layer catches empty-vs-sentinel collisions.
- **Seven divergent implementations.** Every form invents its own required
  checks, error rendering, and gating — none of it shared, none of it tested
  systematically.

## 2. Current-state audit

| Surface | Fields (free-form) | Validation today | Gap |
|---|---|---|---|
| Pool wizard, create + edit (`pool-wizard-modal.tsx`, 1.4k LoC) | pool name, runner image, CPU limit, memory limit, memory-swap custom, min-idle, max-concurrency, labels, renovate image + cron | step-boundary banners; slug regex only real field rule (`data-invalid` on shadcn `Field`) | no blur errors, no gating, sentinel collision (RUN-147), swap ≥ memory unchecked |
| Onboarding (`onboarding.tsx`, 1.5k LoC) | admin username + passphrase, pool step (same fields as wizard) | banner on submit | same |
| Auth profile modal (`auth-profile-modal.tsx`) | profile name, PAT, GitHub App ID, private key PEM | `required` HTML attrs + submit-time `setError` | errors only after submit; HTML `required` is trivially bypassed and unstyled |
| Login (`login.tsx`) | passphrase | banner after auth fails | no empty-gating |
| Settings (`settings.tsx`) | passphrase change, retention inputs | banner after submit | same |
| Logs filters (`routes/logs.tsx`, RUN-219) | runner id, since/until dates | none (server tolerates) | optional scope — see §7 phase 4 |
| Pool detail dialogs (`pool-detail.tsx`) | terminate/drain confirmations | none needed (no free-form inputs) | out of scope |

Server behavior is already correct and stays untouched: bad values are
rejected with `CodeInvalidArgument` (surfaced as banners per docs/09 §5), and
`""` = unset sentinel semantics are deliberate (RUN-216 out-of-scope note).

## 3. Goals and non-goals

**Goals**

1. One systemic toolkit: every form is built from the same factory, the same
   bound field components, and the same validation-timing policy.
2. **Blur-time validation** with inline field errors on every free-form
   input; **gating** — a step's Continue or a form's Submit is unusable while
   any of its required fields is empty or invalid.
3. Fix the RUN-147 sentinel collision at the UI layer (custom mode + empty
   input = blocked, with a clear message).
4. Server stays the authority: client rules **mirror the documented
   acceptance rules**, not the server's parser internals; server
   `CodeInvalidArgument` responses still render, now mapped onto fields.
5. Every migrated form gets jsdom behavior tests; the wizard migration gets
   E2E coverage (`make test-e2e` is the merge gate).

**Non-goals**

- Server-side changes (validation already exists; RUN-147 tests cover it).
- Changing sentinel semantics (`""` = unset stays).
- Redesigning the wizard's visual flow, shadcn components, or docs/27 rules
  (`components/ui/**` stays read-only; we compose wrappers).
- Async uniqueness validation (docs/09 mentions real-time slug uniqueness —
  deferred; this design's shape supports it via async validators later).

## 4. Library decision — TanStack Form

**Chosen: [`@tanstack/react-form`](https://tanstack.com/form/latest) (v1.33.x)
+ [zod v4](https://zod.dev) as the schema layer.**

Rationale:

- **Ecosystem continuity.** We already run TanStack Query and TanStack Router;
  Form completes the stack with the same API philosophy (framework-agnostic
  core, headless, subscription-based reactivity) and the same release
  discipline.
- **Validation timing is a first-class concept.** Validators attach per event
  — `onMount`, `onChange`, `onBlur`, `onSubmit` — exactly matching RUN-216's
  blur-validation requirement, with `field.handleBlur`/`field.handleChange`
  wiring.
- **Native Standard Schema support.** zod v4 schemas can be passed directly as
  validators (no adapter package). One schema per form drives both field
  messages and submit-time checks. (Caveat we accept: TanStack Form uses
  Standard Schema *input* types and does not apply transforms to submitted
  values — our schemas validate, they don't transform; the wizard keeps its
  explicit `trim()`/defaulting at submit time.)
- **Composition built for "systemic" adoption.** `createFormHook` /
  `createFormHookContexts` let us bind our own field components once
  (`useAppForm` factory + `<form.AppForm>`), giving consistent forms
  app-wide with type-safe field names — the mechanism this design standardizes on (§5).
- **Gating is precise.** `form.Subscribe` selectors over
  `form.state.fieldMeta` let each wizard step gate only its own fields;
  `canSubmit`/`isSubmitting` gate final submit. The docs' accessibility
  guidance (`aria-disabled` over `disabled`) is adopted verbatim (§6).
- **shadcn/ui is a documented integration target** — shadcn's own docs carry
  a TanStack Form guide; our `Field`/`Input`/`Select` primitives bind with
  plain props (`value`/`onBlur`/`onCheckedChange`).
- **Cross-field rules** (swap ≥ memory) are supported via form-level
  validators and the Linked Fields pattern (`fieldApi.form.getFieldValue`).

**Alternatives considered:**

| Option | Assessment |
|---|---|
| Status quo (hand-rolled) | Zero deps, but each new form re-invents required/format/pairing checks; the RUN-147 gap is structural, not a one-off. Untestable systematically. |
| react-hook-form (+ zod resolver) | Mature, smaller learning curve. Rejected: uncontrolled-by-default model fights our controlled shadcn `Field` composition; re-render and per-field subscription ergonomics weaker than TanStack Form's `Subscribe` model; no equivalent of `createFormHook` for app-wide bound components. |
| Keep banners, add per-form blur checks | Smallest diff, but perpetuates the divergence this issue exists to fix. |

Risk note: TanStack Form's composition API (`createFormHook`) is newer than
its core; we pin `^1.33` and confine all usage behind our own factory (§5.1),
so an upstream API shift is a one-file migration.

## 5. Architecture

### 5.1 Dependencies and module layout

```
web/src/lib/forms/            ← the systemic toolkit (new)
  contexts.ts                 createFormHookContexts() — app-wide field/form contexts
  use-app-form.ts             createFormHook({ fieldComponents, formComponents, … })
                              → exports useAppForm (the ONLY form hook surfaces use)
  fields/
    text-field.tsx            Input/Textarea binding: value, onBlur→handleBlur,
                              onChange→handleChange, aria wiring, inline <FormError>
    select-field.tsx          Radix Select binding (onValueChange→handleChange);
                              select commits are immediate → validates onChange
    checkbox-field.tsx        onCheckedChange→handleChange
    submit-button.tsx         Subscribe(canSubmit, isSubmitting) + aria-disabled
    form-error.tsx            role=alert, text-destructive, data-testid="form-error"
  step-gate.tsx               <StepGate fields={[…]}> helper (§5.4)
web/src/lib/validation/       ← shared zod schemas mirroring server rules (new)
  pool.ts                     name slug, image ref, cpu, memory, swap custom,
                              swap≥memory refine, min-idle/max-concurrency, labels, cron
  auth.ts                     profile name, PAT/app-id/key non-empty
  admin.ts                    onboarding admin username/passphrase rules
```

New deps: `@tanstack/react-form@^1.33` (runtime) and `zod@^4` (runtime —
small, tree-shakeable; only the schemas a form imports are bundled).

**Ownership rules** (docs/27 discipline):

- `lib/forms/**` and `lib/validation/**` are ours — fully linted, tested.
- Nothing in this design touches `components/ui/**`.
- Surfaces import `useAppForm` and the bound `*Field` components; direct
  `useForm`/raw `validators` usage outside `lib/forms` fails review.

### 5.2 Validation-timing policy (uniform across all forms)

| Control kind | Event | Rationale |
|---|---|---|
| Text/number inputs (name, image, cpu, memory, swap custom, passphrase, PEM) | `onBlur` (format + required), `onSubmit` (full schema) | RUN-216 requirement; no red-on-first-keystroke |
| Select / checkbox / radio (swap mode, auth profile, scope) | `onChange` | commits are discrete; immediate feedback costs nothing |
| Cross-field pairs (swap ≥ memory, min-idle ≤ max-concurrency) | `onSubmit` form-level refine + `onBlur` re-check of the pair via `form.getFieldValue` | pairing needs both values |
| Server round-trip errors | `onSubmit` catch → `field.setErrorMap` / form banner (§5.5) | server stays authority |

Empty-required semantics: a field with a required rule renders an error on
blur when empty, and its step/submit is gated from first render (§5.4) —
mirroring the issue's "invalid/empty-required fields disable Continue".

### 5.3 Schemas mirror rules, not parsers

Each shared schema encodes the *documented acceptance rule* (README field
tables, docs/08 messages, `internal/config` behavior) with a stable error
string, e.g.:

```ts
// lib/validation/pool.ts (illustrative)
export const swapCustom = z.string().trim()
  .min(1, "Enter a swap amount, or switch the mode back to Unlimited")
  .refine(isQuantityGteServerMinimum, "Swap must parse and be ≥ 0");
```

Deliberate stance: we do **not** re-implement sentinel translation
(`""` → unset) or quantity parsing edge cases server-side code mirrors; we
check the *user-facing rule* ("custom mode needs a value"; "swap ≥ memory").
If the server tightens a rule, the client error may lag until mirrored — the
banner (§5.5) remains the backstop. This keeps drift one-directional and
harmless: the client can only *block early*, never *accept wrongly*.

### 5.4 Step gating (multi-step wizard)

Each wizard step declares its field names; its Continue button is a
`StepGate` consumer:

```tsx
const canProceed = form.useStore(s =>
  STEP_FIELDS[step].every(f => (s.fieldMeta[f]?.errors ?? []).length === 0));
```

Fields start error-free but *required-empty* fields contribute to the gate
via a tiny `requiredCheck` registered `onMount` (mounted validation without
rendering an error — the gate reads meta, the UI stays quiet until blur).
The final Create/Save uses `form.Subscribe` over `[canSubmit, isSubmitting]`
per the library's documented pattern. Buttons render
`aria-disabled` + no-op click while gated (docs' accessibility guidance),
with the existing button styling carried by a `data-disabled` attribute.

### 5.5 Server-error mapping

`onSubmit` wraps the existing ConnectRPC mutation:

- `CodeInvalidArgument` with a field-bearing message → map to the matching
  field via `form.setFieldMeta(field, …)` / `field.setErrorMap`, rendering
  inline; this implements the docs/09 §5 intent ("CodeInvalidArgument: inline
  form field validation errors") that banners approximate today.
- Everything else (unavailable, internal, auth) → existing banner path.
- First invalid field receives focus (focus management is a documented
  TanStack Form capability).

### 5.6 Uniform markup contract

Every bound field renders the same skeleton (structural `data-testid`s per
the #283 rule):

```tsx
<Field data-invalid={isInvalid}>
  <FieldLabel htmlFor={id}>…</FieldLabel>
  <Input id={id} aria-invalid={isInvalid} aria-describedby={errId} …/>
  <FormError id={errId} data-testid="form-error" />
</Field>
```

## 6. Migration plan (phased, each its own PR)

| Phase | Content | Gate |
|---|---|---|
| **0 — Toolkit** | `lib/forms` + `lib/validation` modules, factory wiring, unit tests for schemas (accept/reject matrices incl. the sentinel case) | jsdom tests |
| **1 — Pool wizard (pilot)** | Both create + edit modes onto `useAppForm`; step gating; swap-custom fix; renovate fields | jsdom + **new E2E assertions** (invalid custom swap blocks Next; blur error visible) |
| **2 — Onboarding + auth profile modal** | Admin credentials step + pool step reuse wizard schemas/profile schemas | jsdom + E2E for onboarding happy path unchanged |
| **3 — Login + settings** | Small forms; passphrase empty-gating | jsdom |
| **4 — Logs filters (optional)** | runner id + since/until via the same toolkit, if it pays for itself | jsdom |

Each phase leaves the app fully working; a phase can ship alone and be
reverted alone. Phases 1–3 complete RUN-216's definition; phase 4 is a
judgment call recorded at implementation time.

## 7. Testing strategy

- **Schema unit tests (vitest):** per shared schema — accept/reject matrix,
  including the RUN-147 sentinel case (`custom` mode + `""` → required error)
  and pair ordering (swap < memory rejected; equal accepted).
- **Form behavior tests (jsdom):** blur shows inline error; gating disables
  (aria-disabled) Continue/Submit until valid; select changes validate
  immediately; server `CodeInvalidArgument` maps onto the field; focus moves
  to first invalid field.
- **E2E (merge gate for phases 1–2):** extend the wizard spec — attempt to
  advance with an invalid field and assert the block; happy path still
  completes.
- All existing tests keep passing per phase (the wizard's current jsdom suite
  migrates with phase 1).

## 8. Security implications

- **Client checks are UX, not security.** The server remains the sole
  authority (its validation predates this design and is unchanged). No gate,
  schema, or disabled button is trusted server-side.
- **No new secret handling.** Credential fields (PAT, private key PEM,
  passphrase) get *non-empty* checks only — no format assumptions (PAT
  prefixes rotate; format "validation" of secrets creates false rejects and a
  false sense of filtering). Secrets never appear in error strings or logs;
  schemas treat them as opaque strings.
- **No new network surface.** Validation is local; no additional endpoints or
  prefetching. The only server interaction is the existing submit path.
- **XSS surface unchanged.** Error strings are rendered as React text nodes;
  no `dangerouslySetInnerHTML` anywhere in the toolkit.

## 9. Acceptance criteria (RUN-216 mapping)

1. Blur-time inline validation on every free-form input in the audited
   surfaces (§2, phases 1–3).
2. Continue/Submit gated while required-empty or invalid — verified by tests
   and the E2E block-advance assertion.
3. The RUN-147 case is impossible from the UI: custom swap + empty value
   cannot reach the server.
4. Server `CodeInvalidArgument` renders inline on the offending field; other
   codes keep the banner.
5. All forms in phases 1–3 are built from `useAppForm` + shared field
   components (no hand-rolled `useState` field stacks remain there).
6. README: Features lists "Unified client-side form validation (TanStack
   Form)" once phases 1–3 land; this doc's design-phase marker is removed by
   the implementation PR.
7. docs/09 is updated by the implementation PR to describe the form toolkit
   as the standard (replacing the banner-era conventions).

## 10. Out of scope / follow-ups

- Async slug-uniqueness on pool create (docs/09 mentions it) — the toolkit
  supports `onChangeAsync` when wanted; file separately.
- Pool detail dialogs, job-history filters: no free-form inputs today.
- Server protovalidate/field-error plumbing (structured per-field error
  codes would replace message parsing in §5.5) — server work, separate issue.
