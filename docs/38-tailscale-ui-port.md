# 38. Embedded Tailscale: Configurable Management (Tailnet UI) Listener Port

| | |
| :--- | :--- |
| Status | **Design** — docs-only PR; code, tests, and the doc deltas listed in §7 land together in the implementation PR. |
| Related | docs/26 (embedded Tailscale: funnel webhooks + tailnet-only management — shipped) · RUN-155 |
| Touches | `internal/config` (one new env knob), `internal/tailscale` (config field + default), `cmd/runnero-supervisor/daemon.go` (one plumb-through line), README, `.env.example`, `docker-compose.yml` (env pass-through only), docs/26 §3/§4, docs/03 |

## 1. Problem

docs/26 fixed both embedded-Tailscale listener addresses as package constants —
`FunnelAddr = ":443"` and `TailnetAddr = ":8443"` — reasoning that port knobs
"would only add surface for no benefit" on a node dedicated to one supervisor.
The operator-facing benefit that argues back: **the management URL carries the
port.** Today there is no way to get the clean `https://runnero.<net>.ts.net/`
form without the `:8443` suffix, and deployments with a tailnet-side port
convention (tooling, bookmarks, ACL muscle memory) cannot move the management
UI off 8443.

The funnel port must stay pinned: upstream Funnel accepts only TCP
443 / 8443 / 10000, and `:443` is the only choice that yields a
suffix-free public webhook URL. But the tailnet-only listener uses plain
`ListenTLS` inside tsnet's userspace netstack, where **any** port works —
there is no upstream reason to keep it hard-coded.

## 2. Decision

One new knob, port-only:

- **`SUPERVISOR_TAILSCALE_UI_PORT`** — TCP port of the tailnet-only HTTPS
  management listener. Default **8443** (today's constant), so existing
  deployments see zero behavior change.
- `FunnelAddr` stays the fixed `:443` (non-goal: no funnel port knob).

Port-only rather than a full `host:port` address: inside the userspace
netstack the host part is meaningless (the listener binds on the node's
tailnet IP; there is no host-scoping to configure), an integer matches the
existing `SUPERVISOR_PORT` convention, and range validation is trivial.

## 3. Design

### 3.1 Config layer (`internal/config`)

The knob follows the established tailscale contract (docs/26 §4) exactly:

- `EnvTailscaleUIPort = SUPERVISOR_TAILSCALE_UI_PORT`; raw string field
  `TailscaleUIPort` (`koanf:"tailscale-ui-port"`) with
  `DefaultTailscaleUIPort = "8443"` (string, like its sibling raw knobs);
  `TrimSpace`d in normalize.
- New accessor `TailscaleUIAddr() string` → `":" + DefaultTailscaleUIPort`
  when unset, `":" + port` otherwise. Documented: **call only on a validated
  config**, same contract as `TailscaleFunnelOn()` / `TailscaleUIOn()`.
- Validation, enabled-mode only (`TailscaleEnabled()`):
  - a non-empty value must parse as a TCP port 1–65535, else boot error in
    the existing message style:
    `invalid tailscale ui port %q: want 1-65535 (key 'tailscale-ui-port', env %s)`.
  - **Funnel/UI same-port guard:** funnel enabled (the default) **and** UI
    port `443` → boot error, because upstream serve and funnel cannot share a
    port on the same node (docs/26 §2) and the funnel is always `:443`.
    `FUNNEL=false` + `UI_PORT=443` stays legal — that is exactly the
    clean-URL case from §1.
- Off-mode (no auth key): the variable is ignored entirely and never
  validated — a stray `SUPERVISOR_TAILSCALE_UI_PORT=bogus` must not start
  failing boots for operators who never opted in (pinned contract, docs/26 §4).

### 3.2 Tailscale package (`internal/tailscale`)

- `TailnetAddr = ":8443"` becomes `DefaultTailnetAddr = ":8443"`;
  `FunnelAddr` unchanged.
- `tailscale.Config` gains `TailnetAddr string` — the listen address for the
  management listener; empty resolves to `DefaultTailnetAddr`.
- `serve()` resolves the effective address once and uses it both for
  `ListenTLS` and for the boot-log URL.

### 3.3 Daemon wiring (`cmd/runnero-supervisor/daemon.go`)

One line in the existing `tailscale.Config{...}` literal:
`TailnetAddr: cfg.TailscaleUIAddr()`. Everything else — opt-in gating,
fail-fast boot, retry budget, shutdown order — is untouched.

### 3.4 Operator surface

- Restart-time configuration, like every other `SUPERVISOR_TAILSCALE_*` knob:
  change the value, restart the supervisor, the listener reopens on the new
  port (node identity and certificates are unaffected — certs are issued for
  the ts.net name, not a port).
- Privileged ports are fine: the listener binds inside tsnet's userspace
  netstack, not the host kernel, so no root/CAP_NET_BIND_SERVICE consideration
  exists. Likewise nothing is **published**: the container's port map is
  unchanged regardless of the knob.

## 4. Configuration contract (delta over docs/26 §4)

| Variable | Type | Default | Meaning |
| :--- | :--- | :--- | :--- |
| `SUPERVISOR_TAILSCALE_UI_PORT` | Int | `8443` | TCP port of the tailnet-only HTTPS management listener. Any port 1–65535; while the funnel is enabled it must differ from `443` (upstream serve/funnel same-port limitation). |

Validation rules added to docs/26 §4's list:

- non-numeric, `0`, or `> 65535` in enabled mode → boot error (as in §3.1).
- funnel on + UI port `443` → boot error; funnel off + UI port `443` →
  allowed (clean `https://<hostname>.<tailnet>.ts.net/` URL).

## 5. Security implications

- **No exposure-posture change.** The listener remains tailnet-only
  `ListenTLS` inside the netstack with `FunnelOnly()` still walling off the
  public listener; the port number is reachability-neutral inside the
  tailnet and publishes nothing. Both existing locks (WireGuard membership +
  supervisor credentials) still apply.
- **The port is not a security control.** The knob exists for URL ergonomics
  and deployment conventions, not hardening; docs will not suggest otherwise.
- **The 443 guard is a correctness rule, not a security rule** — it prevents
  the upstream serve/funnel same-port misconfiguration from surfacing as a
  runtime listener failure instead of a clear boot error.
- Off-mode ignore semantics are preserved, so the new variable cannot become
  a boot-time liability for deployments that never enabled Tailscale.

## 6. Protocol / API / data-model changes

None. The RPC surface, DB schema, HTTP API, and listener handlers are
unchanged; the only new contract is the environment row in §4.

## 7. Docs to update in the implementation PR

- `docs/26` §3: API-surface line (`ListenTLS("tcp", ":8443")` → resolved from
  config), architecture-diagram listener label, and the "both are fixed
  constants" paragraph amended to: funnel fixed, tailnet port configurable.
- `docs/26` §4: env-table row (§4 above) + the two validation bullets.
- `docs/03` §4: "reachable tailnet-only on `:8443`" → "on the management port
  (default `:8443`)".
- `.env.example`: commented `#SUPERVISOR_TAILSCALE_UI_PORT=8443` in the
  Tailscale block.
- `docker-compose.yml`: one `SUPERVISOR_TAILSCALE_UI_PORT` pass-through line
  (no ports/volumes).
- README feature bullet: mention the knob and default.

## 8. Testing strategy

- **Config unit tests** (`internal/config/config_test.go`):
  - off-mode: stray `SUPERVISOR_TAILSCALE_UI_PORT=bogus` ignored, boot fine;
  - enabled defaults: `TailscaleUIAddr() == ":8443"`;
  - custom `9090` → `":9090"` (trimming covered);
  - `0`, `99999`, `abc` in enabled mode → boot errors naming the key/env;
  - funnel on + `UI_PORT=443` → boot error; `FUNNEL=false` + `UI_PORT=443`
    → loads.
- **Tailscale package** (`internal/tailscale/tailscale_test.go`): `serve` on a
  custom `TailnetAddr` — listener opens and answers on the configured port,
  empty field falls back to the default.
- **Wiring** (`cmd/runnero-supervisor/tailscale_wiring_test.go`): plumb-through
  assertion `cfg.TailnetAddr == ":8443"` in the default-boot case.
- **Not CI-testable:** real tailnet reachability on a custom port — PR
  human-check list (boot with a custom port, reach the UI from another
  tailnet device, verify the boot-log URL, restore default).
- **E2E suite:** untouched — the stack runs without tailscale config.

## 9. Implementation plan

Small-feature flow — one PR carrying code, tests, and the §7 doc deltas:

1. `internal/config`: env constant, default, field, defaults-map entry,
   normalize trim, `TailscaleUIAddr()`, validation (+ guard), unit tests.
2. `internal/tailscale`: `DefaultTailnetAddr`, `Config.TailnetAddr`,
   resolve-and-serve, package tests.
3. `cmd/runnero-supervisor/daemon.go`: plumb-through line; wiring test.
4. Docs/deploy per §7.
5. Full gate suite in the nix shell; PR.

## 10. Open items

None — the design is fully specified; default `8443` keeps every existing
deployment byte-for-byte compatible.
