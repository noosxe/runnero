# UI Form Validation — protovalidate as Single Source of Truth

| | |
|---|---|
| **Issue** | RUN-216 (web: client-side validation for free-form inputs — blur validation that gates submit/next-step) |
| **Revision** | **Rev 2** — supersedes the Rev 1 "zod mirror schemas" design (merged as PR #292) at owner direction: validation is a **hard constraint** owned by one source (proto annotations + protovalidate), evaluated natively on both sides. No zod. |
| **Status** | Design phase — no implementation yet |
| **Scope** | Proto annotations + Go server validation interceptor + web form toolkit. Rev 2 expands scope beyond the original issue text ("no server-side changes"): the owner directed the single-source architecture; server work is now load-bearing. |
| **Related** | docs/02 (architecture: ConnectRPC control plane), docs/08 (RPC protocols — error-details contract added here), docs/09 (frontend design — form conventions), RUN-147 (sentinel collision that surfaced the gap), docs/27 (shadcn/ui discipline) |

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
- **Two disconnected validators.** Go validation (`internal/config`,
  handler checks) and the web's zero validation share nothing; every new
  field risks divergence between what the UI permits and what the API
  accepts.

## 2. Current-state audit

| Surface | Fields (free-form) | Validation today | Gap |
|---|---|---|---|
| Pool wizard, create + edit (`pool-wizard-modal.tsx`, 1.4k LoC) | pool name, runner image, CPU limit, memory limit, memory-swap custom, min-idle, max-concurrency, pids-limit, labels, renovate image + cron | step-boundary banners; slug regex only real field rule (`data-invalid` on shadcn `Field`) | no blur errors, no gating, sentinel collision (RUN-147), min-idle ≤ max-concurrency unchecked |
| Onboarding (`onboarding.tsx`, 1.5k LoC) | admin username + passphrase, pool step (same fields as wizard) | banner on submit | same |
| Auth profile modal (`auth-profile-modal.tsx`) | profile name, PAT, GitHub App ID, private key PEM | `required` HTML attrs + submit-time `setError` | errors only after submit; HTML `required` is trivially bypassed and unstyled |
| Login (`login.tsx`) | passphrase | banner after auth fails | no empty-gating |
| Settings (`settings.tsx`) | passphrase change, retention inputs | banner after submit | same |
| Logs filters (`routes/logs.tsx`, RUN-219) | runner id, since/until dates | none (server tolerates) | optional scope — see §6 phase 4 |
| Pool detail dialogs (`pool-detail.tsx`) | terminate/drain confirmations | none needed (no free-form inputs) | out of scope |

Server-side: mutating RPCs hand-check inputs and reject with
`CodeInvalidArgument` banners (docs/09 §5). Checks live in Go handlers/config
code; the web cannot see *which* rule fired except by parsing messages.

## 3. Goals and non-goals

**Goals**

1. **One source of truth for validation rules**: [protovalidate](https://protovalidate.com)
   annotations on the proto messages — standard rules plus CEL expressions —
   enforced **server-side on every mutating RPC** and evaluated **client-side
   for UX**. Not "mirrored rules": the *same* rule artifacts, executed by the
   official per-language engines. Rule drift between front and back is
   impossible by construction.
2. **Blur-time inline errors** on every free-form input; **gating** — a
   step's Continue or a form's Submit is unusable while any of its required
   fields is empty or invalid (RUN-216's core ask).
3. Fix the RUN-147 sentinel collision at the UI layer (custom mode + empty
   input = blocked with a clear message).
4. **Server stays the authority.** Client evaluation is a fast, fail-first
   preview of exactly what the server will enforce; a structured
   violations-on-errors contract covers everything the client could not
   pre-check.
5. TanStack Form remains the form-state layer (field timing, touched state,
   gating, focus management) — it holds no data-format rules.
6. Every migrated form gets jsdom behavior tests; the wizard migration gets
   E2E coverage (`make test-e2e` gate).

**Non-goals**

- zod — dropped from this design entirely (Rev 1's approach).
- Changing wire field types (quantity strings stay strings; see §5.1
  class B) or sentinel semantics (`""` = unset stays).
- Redesigning the wizard flow, shadcn components, or docs/27 rules
  (`components/ui/**` stays read-only; we compose wrappers).
- Async uniqueness validation UX (docs/09) — remains server-checked with
  structured violations; no live re-check loop (§10).

## 4. Library decision

**Chosen: [protovalidate](https://github.com/bufbuild/protovalidate) (rule
source) + [`@tanstack/react-form`](https://tanstack.com/form/latest) (form
state), evaluated by `protovalidate-go` (server) and `protovalidate-es`
(browser).**

All APIs below were verified against upstream sources on 2026-09-15 (READMEs,
`validate.proto` source, connect-go v1.19.1 source — repo runs
`connectrpc.com/connect v1.21.0`, `@bufbuild/protobuf v2.15.0`,
`@connectrpc/connect v2.2.0`):

| Fact | Source |
|---|---|
| Rules are proto options: `buf.validate.field` standard rules + `buf.validate.message).cel` / `buf.validate.field).cel` custom [CEL](https://cel.dev) rules with stable `id` and human `message` | protovalidate README |
| Message-level CEL: `this` = the message → cross-field expressions ("`!has(this.first_name) \|\| has(this.last_name)`") are first-class | protovalidate README example |
| Go: `buf.build/go/protovalidate` — `protovalidate.Validate(msg)`; failures expose violations; violations serialize to the `buf.validate.Violations` proto | protovalidate-go README + repo |
| TS: `@bufbuild/protovalidate` — `createValidator()` → `validator.validate(Schema, msg)` → `result.kind !== "valid"`; plugs into our existing `@bufbuild/protobuf` v2 generated code; needs no extra codegen plugin | protovalidate-es README |
| TS pattern rules use `@bufbuild/re2` by default — RE2, linear time, ReDoS-safe, scoped/small | protovalidate-es README |
| Violation shape (from `buf/validate/validate.proto` v1): `rule_id` (stable string), `message` (human text), `field` (structured `FieldPath`), `for_key` | `validate.proto` source |
| Connect protocol errors carry typed `details` (type = fully-qualified proto name, base64 value); connect-go: `connect.NewErrorDetail(msg)` → `err.AddDetail(d)`; clients read typed details back | Connect protocol reference + connect-go source |

**Alternatives considered:**

| Option | Assessment |
|---|---|
| **Rev 1: hand-written zod mirror schemas** (merged design) | Rejected after owner review: two rule sources = permanent drift risk; "mirror the rules" depends on discipline, not mechanics. The user-facing rules *are* constraints — they belong in the contract, not beside it. |
| Codegen (JSON Schema from proto → generated zod) | Same two-runtime problem with an extra transpiler step; generated zod lags proto changes and re-couples client to parser-ish shapes. |
| react-hook-form + zod | Uncontrolled model fights our controlled shadcn `Field` composition; still leaves the two-source problem. |
| Status quo (hand-rolled) | The RUN-147 gap is structural. |

## 5. Architecture

### 5.1 Rule classification — the core of the design

Every validation rule gets exactly one home:

**Class A — schema rules (protovalidate annotations; the single source).**
Format, length, range, required, map, enum, and cross-field rules
expressible as standard rules or CEL over message fields. Evaluated by
**both** engines — server (interceptor, §5.3) and browser (adapter, §5.4).
Examples from `api.proto`:

- pool `name`: `string.pattern` (`^[a-z0-9][a-z0-9-]*$`), `min_len`/`max_len`
- `min_idle_runners`, `max_concurrency`, `pids_limit`: `int32.gte`/`lte`
  standard rules
- `min_idle_runners ≤ max_concurrency`: message-level CEL
  (`id: "pool.min_idle.max_concurrency"`,
  `expression: "this.min_idle_runners <= this.max_concurrency"`)
- labels: `map.min_pairs`, key/value `string.max_len`
- admin username/passphrase, auth-profile name/PAT: `string.min_len`/`max_len`
  (+ `pattern` for username)

**Class B — parser/stateful rules (server-enforced; delivered through the
same structured channel).** Rules whose semantics live in Go code the schema
cannot express: quantity-string parsing (`cpu_limit`, `memory_limit`,
`memory_swap_limit` — CEL cannot parse `"512Mi"`), cron syntax, pool-name
uniqueness, capacity checks. These fire in Go (where they already exist) and
**must** return violations in the identical `buf.validate.Violations` detail
format with a **stable `rule_id`** from a documented registry (e.g.
`pool.memory_swap.gte_memory`). The client treats class B results exactly
like class A — inline, field-mapped. One UX contract, no zod shim, no
message-parsing.

**Class C — UI-state rules (form-only; client-only by nature).** Rules over
client-only state that has no wire representation. The RUN-147 case itself:
swap *mode* (Unlimited/Custom) is wizard state — `api.proto` has no
`swap_mode` field — so "Custom selected ⇒ value required" is a form rule.
Same class: step gating. These live in TanStack Form validators, never
encode data formats, and the server remains untouched by them (the wire
meaning of `""` = unset stays valid).

### 5.2 Proto changes

`proto/api.proto` gains `import "buf/validate/validate.proto"` and
annotations on mutating-request messages (`CreateOrUpdatePoolRequest`,
`OnboardingRequest`, `CreateAuthProfileRequest`, auth/login/settings
messages). No field, type, or number changes — options only, wire-compatible
in both directions. Annotations carry user-facing `message` text (the single
rendered error string) and stable CEL `id`s. `buf lint` / `make proto-lint`
stays green; CEL compilation is checked by the protovalidate engines and by
unit tests at both ends.

Go side needs **no codegen change**: protovalidate-go reads rules from
descriptors at runtime. Web side: rules ride the existing
`buf generate proto` pipeline (protobuf-es embeds options in descriptors).

### 5.3 Server enforcement (new, load-bearing)

`internal/server/validate.go`: a unary Connect interceptor holding one
`protovalidate` validator. For every mutating RPC:

1. `protovalidate.Validate(msg)` → on `*protovalidate.ValidationError`,
   serialize violations to `buf.validate.Violations` and respond
   `connect.NewError(CodeInvalidArgument, …)` with
   `err.AddDetail(connect.NewErrorDetail(violations))` (verified API).
2. Class B checks (existing Go validation + uniqueness) return the same
   detail type with registered `rule_id`s instead of bare `CodeInvalidArgument`
   messages.
3. Fail-closed: interceptor errors that cannot evaluate fail the request
   (unavailable), never pass silently.

Wire compatibility: clients that ignore details see the same
`CodeInvalidArgument` behavior as today, with a human message. Our web
client upgrades to structured rendering (§5.4).

### 5.4 Web adapter and TanStack Form

```
web/src/lib/forms/                 ← the form toolkit (new)
  violations.ts                    violation ⇄ form-field mapping + rule_id registry mirror
  protovalidate.ts                 one createValidator(); validateForm(schema, values)
                                   → { field → [messages] } (+ focus-first target)
  contexts.ts / use-app-form.ts    createFormHook factory — useAppForm (the only form hook)
  fields/ (text, select, checkbox) shadcn binding: value/onChange/onBlur → form handlers,
                                   aria-invalid wiring, inline <FormError>
  submit-button.tsx, step-gate.tsx gating (unchanged mechanics from Rev 1)
web/src/lib/validation-ids.ts      re-export of rule_id constants shared with tests
```

- `deps`: `@bufbuild/protovalidate` (pulls `@bufbuild/re2` by default —
  ReDoS-safe, small). **No zod dependency.**
- **One evaluation path.** The wizard already assembles the typed request
  message it submits; blur/step-change handlers rebuild that message from
  current form values and run `validator.validate(...)` (message is tiny —
  evaluation cost is negligible), then the adapter filters violations whose
  `field` path touches the edited field(s).
- **Display rule** (uniform): an error renders when the field is touched
  (or submit was attempted) *and* a violation maps to it. Gating reads the
  same violation map — a field with a mapped violation disables its step —
  so gate and message can never disagree.
- **Server round-trip:** `onSubmit` catches `ConnectError`, extracts typed
  `buf.validate.Violations` details, runs the identical mapping → inline
  errors + focus first invalid field. Unknown fields/codes keep the banner
  fallback.
- Field-name mapping: proto field names are canonical; wizard field names
  align 1:1; class C rules reference form-state fields (`swapMode`) that
  never appear in violations.

### 5.5 Validation-timing policy (unchanged mechanics)

| Control kind | Event | Source of rule |
|---|---|---|
| Text/number inputs | `onBlur` (evaluate + show), `onSubmit` (evaluate + gate) | Class A (+ B on submit) |
| Select / checkbox | `onChange` | Class A / C |
| Cross-field (CEL or form-state) | message re-evaluation on any member edit; submit always | Class A CEL / class C |
| Server round-trip | submit catch | Class A + B |

### 5.6 Uniform markup contract

Unchanged from Rev 1 — every bound field renders `Field[data-invalid]`,
`Input[aria-invalid]`, `aria-describedby`, and `FormError[data-testid=
"form-error"][role=alert]`; structural testids per the #283 rule.

## 6. Migration plan (phased, each independently shippable/revertable)

| Phase | Content | Gate |
|---|---|---|
| **0 — Contract** (server + proto) | annotations on mutating messages; validation interceptor + violations-detail plumbing; class B `rule_id` registry; Go tests (interceptor, per-message rules, fail-closed) | `make test`, `make vet`, `make proto-lint` |
| **1 — Web toolkit + pool wizard pilot** | `lib/forms` adapter + factory; wizard create+edit on `useAppForm`; RUN-147 fix (class C gating); E2E block-advance assertions | jsdom + `make test-e2e` |
| **2 — Onboarding + auth profile modal** | same toolkit; admin + profile schemas already annotated in phase 0 | jsdom + E2E happy path |
| **3 — Login + settings** | small forms; empty-gating | jsdom |
| **4 — Logs filters (optional)** | runner id + since/until, if it pays for itself | jsdom |

Phases 0 and 1 land as separate PRs (server contract first — the client
needs the detail format to exist). Each later phase leaves the app fully
working alone.

## 7. Testing strategy

- **Go (phase 0):** interceptor tests — valid message passes; each annotated
  rule's violation produces `CodeInvalidArgument` + correct
  `rule_id`/field detail; class B rules return the registry ids; malformed
  CEL (compile failure) → fail-closed; non-mutating RPCs untouched.
- **Web adapter (vitest):** fixture messages → violation map, per-field
  filtering, mapping table, focus selection, detail-extraction path.
- **Form behavior (jsdom):** blur shows inline error; gating disables until
  valid; select changes validate immediately; server violations render
  inline identically; the RUN-147 custom+empty case cannot reach the server.
- **E2E (phases 1–2 gate):** wizard spec — advance blocked on invalid field;
  happy path completes; blur error visible; server-side-only rule (e.g.
  duplicate pool name) renders inline via the detail path.
- **No cross-implementation parity suite is needed**: both engines evaluate
  the *same* annotations; upstream ships conformance suites for exactly this
  guarantee. Tests above prove our *mapping and plumbing*, not rule parity.

## 8. Security implications

- **Annotations are hard constraints server-side**, enforced by the
  interceptor on every mutating RPC — the client preview is a courtesy, and
  skipping it buys an attacker nothing. No security-relevant decision ever
  depends on client evaluation.
- **Class B delivery** means quantity/cron/uniqueness rejections no longer
  depend on parsing error *messages* — stable `rule_id`s only. Message text
  is user-facing; no parser internals leak (rule semantics stay in Go, the
  client just displays verdicts).
- **ReDoS posture:** pattern rules run on RE2 (linear time) both sides —
  Go's regexp and `@bufbuild/re2` in the browser.
- **Details leak surface:** violation details carry field paths, rule ids,
  and message text — no values, no secrets. PATs/PEMs/passphrases keep
  non-empty/max-len rules only; secrets never appear in rule text or
  violations.
- **Fail-closed** evaluation errors; wire-compatible for clients ignoring
  details.

## 9. Acceptance criteria (RUN-216 mapping, Rev 2)

1. All data-format/cross-field rules for the audited surfaces live as
   protovalidate annotations (class A) or registered class B checks — zero
   client-side rule re-implementations; no zod dependency in `web/`.
2. Blur-time inline validation + submit/step gating on every free-form
   input in phases 1–3; RUN-147's custom-swap-empty cannot reach the server.
3. Server rejects annotated violations on every mutating RPC
   (`CodeInvalidArgument` + `buf.validate.Violations` details) — verified by
   Go tests; web renders server violations inline via the same mapping.
4. Stable `rule_id` registry documented (proto CEL ids + class B Go ids);
   client tests key on ids, not message text.
5. E2E: block-advance + inline-error assertions green (`make test-e2e`).
6. README: Features gains "Unified form validation (protovalidate +
   TanStack Form)" when implementation lands; this doc's *[Design Phase]*
   roadmap marker is removed by the final implementation PR.
7. docs/08 gains the violations-details contract; docs/09 describes the
   form toolkit as the standard (both in implementation PRs per the
   no-docs-lifecycle rule).

## 10. Out of scope / follow-ups

- Live async uniqueness (docs/09) — server class B check only; file
  separately if live UX is wanted.
- Promoting quantity comparisons to class A by evolving wire types
  (structured int64 quantities) — a protocol cleanup worth its own design;
  would retire the last class B parser rule.
- Bundle-size budget for `@bufbuild/protovalidate` + `@bufbuild/re2` in the
  web bundle — measured at phase 1, revisited against docs/09 budgets.
- gRPC surface parity: the supervisor API is Connect-only today; the
  interceptor design assumes unary Connect RPCs (docs/02).
