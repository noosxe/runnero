# User Account Page (RUN-282)

Status: **Design** — implementation issue to be filed after this doc merges.

## 1. Summary

Personal account management gets a dedicated `/account` surface, reached only
through the sidebar footer avatar menu (which becomes a real menu item).
Today those surfaces are the Security tab inside `/settings`, and the passkey
enrollment card is *silently absent* whenever WebAuthn is not configured —
on an unconfigured stack there is no hint the feature exists. The account
page makes passkey support discoverable: the card is always rendered (for
admins when unconfigured, via the shadcn `Empty` component), and tabs become
**path-based URLs** (`/account/security`, `/account/sessions`) instead of
`?tab=` query params.

This is a pure web/IA refactor: **no proto, RPC, or schema changes.**

## 2. Current state

- **Sidebar footer** (`web/src/components/layout/app-shell.tsx`): the user
  avatar opens a dropdown whose content is a `DropdownMenuLabel` repeating
  the avatar plus a sign-out item. Nothing navigates anywhere.
- **`/settings?tab=security`** (`web/src/routes/settings.tsx` +
  `web/src/components/security/security-tab.tsx`): the Security tab stacks
  `ChangePasswordCard`, the sessions table (`useSessions`, per-row revoke,
  revoke-all-others; RUN-232/docs/32 §7), and — only when
  `onboarding.passkeyAvailable` is true — `PasskeysCard`
  (RUN-235/247/docs/34).
- **Passkey gating**: `passkeyAvailable` requires `SUPERVISOR_WEBAUTHN_RP_ID`
  to be configured; the RPCs answer `FailedPrecondition` otherwise. The
  current UI hides the card entirely, making the capability undiscoverable
  on unconfigured stacks (the product owner's local stack, notably).
- **Tab URL state**: `resolveRouteTab` (RUN-257/RUN-258) clamps a `?tab=…`
  search param against an allowed set; `/settings` and `/logs` use it.

## 3. Goals and non-goals

**Goals**

1. A real, keyboard/AT-accessible menu item in the footer dropdown that
   navigates to the account page (shadcn `nav-user` pattern: avatar + name +
   email row), with sign-out remaining a separate item.
2. An account page usable by **every role**, with tab-level gating by access
   level.
3. Passkey discoverability: the passkeys card is always visible somewhere —
   to all users when WebAuthn is configured, to admins only (as an `Empty`
   state pointing at the required configuration) when it is not.
4. Tabs as proper URL path segments; no `?tab=` on this page.

**Non-goals**

- No editing of account identity fields (username/role remain admin-managed
  via Settings › Users). The page is read-only about identity.
- No migration of the existing `?tab=` routes (`/settings`, `/logs`) to path
  segments — filed separately as a follow-up issue (RUN-283).
- No server/API work of any kind.

## 4. Information architecture

### 4.1 Entry point

The footer dropdown becomes:

```
[avatar trigger]
├─ Menu item: [avatar] <username> / <role>   →  /account/security
└─ Menu item: Sign out
```

Per the shadcn navigation-menu/nav-user examples the identity row is a real
`DropdownMenuItem` (focus ring, hover, `onSelect` navigation), not a static
label. This is the page's **only** entry point — no row in the main nav
list.

### 4.2 Routes and tabs

| Path                | Tab      | Who sees it                     |
|---------------------|----------|---------------------------------|
| `/account`          | redirect | → `/account/security`           |
| `/account/security` | Security | all authenticated users         |
| `/account/sessions` | Sessions | all authenticated users         |

Tabs are **navigation**: each tab is its own route/history entry, so
back/forward, deep links, and refreshes restore the tab without query-param
clamping. The active tab derives from `location.pathname`; the tab strip
follows the existing settings tab-strip look (RUN-251) but switches tabs via
router navigation instead of `setSearchParams`.

### 4.3 Tab contents

**Security** — `ChangePasswordCard` (unchanged) and the passkeys card:

- `passkeyAvailable === true`: the existing `PasskeysCard` (enroll/remove,
  current-password re-check) for **every role**, exactly as today.
- `passkeyAvailable === false`: card renders **for admins only** with the
  shadcn `Empty` component: title "Passkey support is not configured",
  description pointing at `SUPERVISOR_WEBAUTHN_RP_ID` /
  `SUPERVISOR_WEBAUTHN_ORIGINS` (empty origins default to
  `https://<rp_id>`) and a link to `docs/34`. Non-admins see nothing.

**Sessions** — the sessions table moves here verbatim (device label, clocks,
current-session marker, per-row revoke, revoke-all-others; docs/32 §7).

### 4.4 Settings page after the move

The security tab is **erased** from `/settings` — no redirect, no deep-link
compat for `/settings?tab=security` (single-user product; breaking changes
accepted). Remaining settings tabs: instance / constraints / images /
backups / users. Non-admin users now see only the instance tab, and the
non-admin default tab becomes `instance` (today it falls back to
`security`).

## 5. Component design

- `web/src/routes/account.tsx` — account shell: header (username + role from
  the existing session/whoami data), tab strip, `<Outlet />`.
- `web/src/routes/account-security.tsx` / `account-sessions.tsx` — tab
  bodies, largely re-locating existing JSX.
- `PasskeysCard` gains an unconfigured variant: `passkeyAvailable === false`
  renders the `Empty` state instead of `null` (a boolean prop such as
  `configured` keeps the card dumb; the admin-only decision stays with the
  caller).
- `app-shell.tsx` footer menu per §4.1, reusing the avatar/initials already
  computed there.
- `web/src/router.tsx` — `/account` route group guarded like `/settings`
  (authenticated, any role), with the redirect and two child routes.
- `SecurityTab` is dissolved; its parts move to the new tab files.

## 6. Routing & URL state

`resolveRouteTab` stays for `/settings` and `/logs` (query-param style) but
is **not** used on `/account`. Unknown `/account/*` paths fall through to the
existing not-found handling; `/account` redirects (replace) to
`/account/security` so there is exactly one canonical tab URL per tab.

## 7. Server & protocol impact

None. `passkeyAvailable` is already exposed on the onboarding payload and
role is already known client-side from the session. No proto changes, no new
RPCs, no schema/migration work.

## 8. Security implications

- **No authorization changes**: every card sits behind the same RPCs as
  today; move-only. Passkey enrollment/removal keeps its current-password
  re-check; session revoke endpoints are unchanged.
- The admin-only `Empty` state is **cosmetic gating** of a configuration
  notice — enrollment RPCs already enforce server-side authorization
  (passkeys are enrollable by any enabled role when configured), so no
  privilege boundary depends on the UI hide/show.
- WebAuthn "ships dark" semantics (docs/34) are preserved: unconfigured
  deployments expose no passkey RPCs; the `Empty` card only documents *how
  to enable* the feature and is itself admin-visible only.
- The footer menu item must not leak identity data on unauthenticated
  states — it renders only inside the authenticated shell, as today.

## 9. Testing strategy

- **Vitest**: settings tests drop the security tab (and the non-admin
  default becomes `instance`); new account route tests (redirect, tab
  active-state, role gating of the passkeys card in both `passkeyAvailable`
  states); footer-menu test for the identity item's navigation; existing
  `ChangePasswordCard`/`PasskeysCard`/sessions tests keep passing with
  adjusted mounting.
- **E2E (Playwright)**: Flow 12 (passkey) enrollment navigation moves from
  `/settings?tab=security` to `/account/security`; Flow 13 (viewer role)
  asserts the account page is reachable for viewers with the passkeys card
  present (configured stack in CI) and that `/settings` no longer shows a
  security tab; the a11y scan route lists gain `/account/security` and
  `/account/sessions` for both themes; Flow 07 (settings maintenance)
  updates its tab expectations.

## 10. Documentation impact

Implementation PR updates: `docs/09` (IA: nav entry points + account page
section), `docs/32` §7 (sessions tab location), `docs/34` (enrollment
location + the unconfigured `Empty` card), `docs/13` (E2E route list if it
enumerates pages). README moves the feature from **Roadmap** to **Features**
on merge.

## 11. Compatibility

Breaking (accepted by the product owner): `/settings?tab=security` is gone
with no redirect; the security tab disappears from `/settings` for all
roles. Bookmarks and muscle memory are the only casualties.
