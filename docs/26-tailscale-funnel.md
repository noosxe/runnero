# 26. Embedded Tailscale: Funnel Webhooks and Tailnet-Only Management Without a Public Host

| | |
| :--- | :--- |
| Status | Implemented (RUN-155 — accepted with this design; shipped in the implementation PR) |
| Linear | [RUN-155](https://linear.app/runnero/issue/RUN-155) (implementation) · depends on [RUN-153](https://linear.app/runnero/issue/RUN-153) (webhook receiver wiring — **done**, PR #206) |
| Related | RUN-154 (Cloudflare Tunnel compose option — sibling/alternative; *not* part of this work) · docs/03 §4 (webhook-driven scaling) · docs/10 (reverse proxy TLS) |
| Touches | `go.mod` (`tailscale.com/tsnet`), `internal/config` (new env contract), new `internal/tailscale` package, `cmd/runnero-supervisor/daemon.go` (wiring + shutdown), README (features, prerequisites, env table), `.env.example`, `docker-compose.yml` (env pass-through only), docs/03 §4 |

## 1. Problem

Webhooks need a publicly reachable `POST /hooks/{provider}`; the management UI
needs to be reachable by the operator. Today both require either a public host
with published ports or a reverse proxy / tunnel sidecar (docs/10, RUN-74,
RUN-154). On the deployment that matters most to the owner — a workstation
behind NAT — that means either exposing the supervisor to the LAN-wide public
ingress, or bolting on infrastructure the owner doesn't want to run.

The owner already runs Tailscale. Tailscale can solve both problems with zero
extra processes: **Funnel** publishes a public HTTPS endpoint relayed by
Tailscale's infrastructure, and plain **tailnet reachability** provides remote
management that never touches the public internet.

## 2. Decision: embedded tsnet, no sidecars

Per the owner's call on RUN-155: the Tailscale node is embedded **directly in
the supervisor binary** via [`tailscale.com/tsnet`](https://pkg.go.dev/tailscale.com/tsnet).
There is no compose sidecar, no extra container, no serve-config file, and no
published port for either listener. The Cloudflare Tunnel compose option stays
available as a separate, independent deployment choice (RUN-154).

Verified against upstream (`tailscale.com v1.102.3`, probed in a scratch module
with `CGO_ENABLED=0` on 2026-09-09):

- **CGO-free holds.** `go list -deps` over the tsnet dependency graph contains
  zero packages with cgo files — the wireguard-go userspace netstack lineage is
  pure Go. The repo-wide `CGO_ENABLED=0` constraint is unaffected.
- **Binary size is acceptable.** A stripped, trimpathed probe binary linking
  only tsnet weighs 19.7 MB against the current 26.9 MB supervisor binary.
  Shared standard-library/crypto dependencies overlap, so the net addition to
  the real binary is expected around +12–16 MB (final image is
  `alpine:3.24` — immaterial). Measured precisely at implementation.
- **Toolchain note:** the `tailscale.com v1.102.3` module declares
  `go 1.26.6`; the repo builds on `go 1.26.4` (nix shell) while the Docker
  build stage already uses `golang:1.27-alpine`. Implementation must pin the
  newest `tailscale.com` release whose `go` directive is satisfied by the
  supported toolchain (or bump the nix toolchain — the product owner's env).
- **API surface used:** `tsnet.Server{Dir, Hostname, AuthKey, Logf, UserLogf}`,
  `Start()`, `Close()`, `ListenFunnel("tcp", ":443", tsnet.FunnelOnly())`,
  `ListenTLS("tcp", ":8443")`.
- **Funnel constraints (upstream KB):** TCP ports **443 / 8443 / 10000** only,
  TLS only with automatic ts.net certificates, requires MagicDNS + HTTPS
  enabled on the tailnet + the `funnel` node attribute in the tailnet policy
  file, and **serve and funnel cannot share a port** on the same node.
- **Auth behavior:** `Server.AuthKey` takes precedence; once the node is
  enrolled, state in `Dir` is reused and the auth key is ignored (unless
  `TSNET_FORCE_LOGIN=1`).

## 3. Architecture

```
                         ┌──────────────────────────────────────────────┐
  Public internet ──HTTPS │ runnero-supervisor binary                    │
  https://runnero.<net>.  │                                              │
  ts.net:443/hooks/* ────▶│ Funnel listener  :443 (FunnelOnly, TLS auto) │
                          │   mux: POST /hooks/{provider} ONLY           │
                          │   everything else → 404/405                  │
                          │        │ in-process                         │
                          │        ▼                                     │
                          │   webhook.Receiver (HMAC) → PoolController   │
                          │                                              │
  Tailnet devices ──HTTPS │ Tailnet listener :8443 (ListenTLS, ts.net)   │
  https://runnero.<net>.  │   mux: the full server.Handler()             │
  ts.net:8443/* ─────────▶│   (UI + ConnectRPC API + hooks + health)     │
                          │        │                                     │
                          │        ▼                                     │
                          │   existing auth (login/session) still applies│
                          │                                              │
  LAN :8090 (unchanged) ─▶│ LAN listener :8090 (existing http.Server)    │
                          └──────────────────────────────────────────────┘
                                   │ outbound only
                                   ▼
                        Tailscale control plane + DERP relays
                        (UDP WireGuard direct when possible)
```

Three listeners total when enabled:

1. **LAN listener `:8090`** — today's `http.Server`, unchanged, always on.
2. **Funnel listener `:443`** — public, `tsnet.FunnelOnly()` so even tailnet
   clients use the UI listener instead. Serves **only** the webhook receiver
   route group (`POST /hooks/{provider}`), mounted from
   `server.WebhookReceiver()` with exactly the semantics RUN-153 shipped:
   receiver mounted → signed deliveries accepted, `ping` → 200, tampered/missing
   signature → 401, unconfigured provider → 500, unsupported provider → 400;
   no receiver configured → route unmounted, POST answers 405. **Every other
   path and method on the funnel listener answers 404.** No SPA, no API, no
   cookies, no sessions, no CORS, no health endpoints.
3. **Tailnet listener `:8443`** — `ListenTLS` with automatic ts.net
   certificates, reachable only inside the tailnet by construction. Serves
   `server.Handler()` verbatim: the complete management UI, ConnectRPC API,
   webhook routes, and health endpoints. The existing supervisor
   authentication (bootstrap admin, login, session cookies) still applies —
   tailnet membership is network reachability, **not** an authorization
   bypass. Because the listener is HTTPS, `SUPERVISOR_SECURE_COOKIE=true`
   works over the tailnet (and only breaks plain-HTTP LAN logins exactly as
   documented today).

The serve/funnel same-port limitation is why the two listeners use different
ports: `:443` funnel (webhooks), `:8443` tailnet UI. Both are fixed constants —
the embedded node is dedicated to this supervisor, so port knobs would only add
surface for no benefit.

**No inbound port publishing.** Both listeners live inside tsnet's userspace
netstack; the container publishes nothing new. The only connectivity requirement
is **outbound**: UDP (WireGuard, direct paths where NAT allows) and HTTPS 443
(derp relays + control plane) — the normal Tailscale egress profile.

### Boot and shutdown

- Config load → if `SUPERVISOR_TAILSCALE_AUTHKEY` is unset/blank: the
  integration is **completely off**. No tailscale code path runs, no goroutine,
  no listener, no network packet; boot proceeds exactly as today. This mirrors
  the RUN-153 mount-when-configured contract.
- If the auth key is set: construct the `tsnet.Server` (state dir under the
  data volume), `Start()` with a small bounded retry (3 attempts, 10 s apart)
  to ride transient control-plane hiccups, then **fail boot** on final error.
  The operator explicitly asked for Tailscale; silently degrading would hide
  breakage. (Compare: an unset webhook secret degrades to polling, because
  that mode is documented as the default.)
  A failed boot also unwinds cleanly (RUN-160): the daemon's background
  loops (pool controller /events stream, audit/reconcile cycles, backup,
  retention, and cron schedulers) run on a daemon-owned context that every
  early-error return cancels and waits for, so nothing outlives the boot
  error. `TestDaemonTailscaleBootFailureAbortsBoot` asserts the event
  stream is released via `fakedocker.EventsStreamsActive`.
- Open the configured listeners, wrap each in an `http.Server`, serve in
  goroutines, and log the resulting URLs:
  - funnel: `https://<hostname>.<tailnet>.ts.net/hooks/{github,gitea,forgejo}`
    — this URL goes straight into the provider's webhook configuration
    (docs/03 §4).
  - UI: `https://<hostname>.<tailnet>.ts.net:8443/`
  - If the funnel is enabled but no webhook secrets are configured, log a
    warning: the listener will only ever answer 405/404.
- Graceful shutdown (existing signal path): drain both new `http.Server`s,
  then `tsnet.Server.Close()`, then proceed with today's teardown. Tailscale
  failure must never block the supervisor's core shutdown path.

## 4. Configuration contract

All options are `SUPERVISOR_TAILSCALE_*`; the feature is **off unless the auth
key is present**. Values are trimmed; empty means default/off per row.

| Variable | Type | Default | Meaning |
| :--- | :--- | :--- | :--- |
| `SUPERVISOR_TAILSCALE_AUTHKEY` | String | *(empty = feature off)* | Tailscale auth key used once to enroll the node; ignored on later boots while node state exists (upstream behavior). **Required to enable anything below.** |
| `SUPERVISOR_TAILSCALE_HOSTNAME` | String | `runnero` | Node hostname inside the tailnet; final DNS name is `<hostname>.<tailnet>.ts.net`. Must be unique per tailnet — set it when running more than one supervisor. |
| `SUPERVISOR_TAILSCALE_FUNNEL` | Bool | `true` | Public funnel listener on `:443` for provider webhooks (FunnelOnly). |
| `SUPERVISOR_TAILSCALE_UI` | Bool | `true` | Tailnet-only HTTPS management listener on `:8443`. |
| `SUPERVISOR_TAILSCALE_STATE_DIR` | String | `<SUPERVISOR_DATA_DIR>/tailscale` | tsnet state directory (node identity, `tailscaled.state`). Lives inside the existing supervisor data volume. |

Validation rules (fail boot with a clear message):

- auth key empty → feature off; all other variables ignored, **no validation
  performed** (a stray `SUPERVISOR_TAILSCALE_FUNNEL=bogus` must not start
  failing boots for users who never opted in).
- auth key set **and** `FUNNEL=false` **and** `UI=false` → boot error: a node
  with no listeners serves nothing; this is a misconfiguration, not a mode.
- Bool parsing follows the existing config conventions; unknown/invalid values
  are boot errors in the enabled state.

Rotation of the auth key = operator deletes the node in the Tailscale admin
console, clears `<state_dir>`, sets the new key, restarts. Documented as
restart-time configuration (same posture as webhook secrets, RUN-153).

## 5. Security implications

- **Public exposure is minimal by construction.** The funnel listener serves a
  single route group that is HMAC-verified (RUN-68) and carries no cookies,
  sessions, CSRF surface, or UI code. Everything else 404s. TLS certificates
  are ts.net-issued automatically; no cert management.
- **Certificate lifecycle is automatic (verified in upstream source, `v1.102.3`).** Both listeners install an SNI-based `GetCertificate` callback (`tsnet.Server.getCert` → the embedded node's local client), so no static cert is ever pinned. Underneath runs the same ACME machinery tailscaled uses (`feature/acme`): first use issues a Let's Encrypt certificate for the node's ts.net name (requires HTTPS enabled on the tailnet — §3 prerequisite); a cached cert is served only while it outlives the requested minimum validity; certificates approaching expiry renew asynchronously (requests never stall behind renewal); a **background refresh loop periodically pokes the cert manager so renewals happen on idle nodes** — a supervisor with zero webhook traffic still keeps its certs fresh; and a blocking on-demand fetch covers the expired/missing case, with ACME rate-limit responses surfaced as retryable errors. Certificates persist in the tsnet state dir, so restarts never re-issue. Renewal shares the node's outbound-only egress profile; no certificate code exists in supervisor scope.
- **`FunnelOnly()`** prevents tailnet clients from using the public listener;
  tailnet users get the UI listener. Least privilege: each listener serves
  exactly one audience.
- **Management plane stays behind two locks.** The tailnet listener requires
  WireGuard-encrypted tailnet membership *and* the existing supervisor
  credentials. Compromise of one factor is not enough for the other.
- **Auth key is a credential.** Zero-leak policy applies: it lives only in
  `.env` (gitignored) / the orchestrator's secret mechanism, is passed through
  compose like other secrets, and is never logged. Recommend a **tagged,
  reusable** key (e.g. `tag:runnero`) so the node cannot act as a user; the
  tag must be declared in `tagOwners`. Ephemeral nodes are explicitly *not*
  used — the supervisor's node identity persists in the state dir so the
  provider webhook URL survives restarts.
- **State dir contains node keys** (`tailscaled.state`). It lives under the
  data volume next to the encrypted DB; created with `0700`. Operators backing
  up the data dir should treat it as secret, same class as the DB.
- **Log telemetry disclosure:** tsnet uploads node logs to
  `log.tailscale.com` by default (config file lands in the state dir). We
  route tsnet's `Logf`/`UserLogf` into the supervisor's logger so boot
  problems are visible locally; implementation must verify whether upstream
  offers a supported no-upload switch and document the outcome either way.
  Webhook payloads never reach Tailscale's logs — only node diagnostics.
- **Funnel bandwidth** is relayed via DERP with non-configurable limits —
  irrelevant for webhook volume (bytes), and tailnet UI traffic is
  peer-to-peer WireGuard, not relayed, except as a fallback.
- **On-mode boot is fail-fast** (§3) so a dead auth key or blocked egress is
  an incident, not a silent policy change: if Tailscale was the webhook path,
  the operator wants to know immediately.

## 6. Protocol / API / data-model changes

None. The RPC surface, DB schema, and HTTP API are unchanged; the funnel and
tailnet listeners serve existing handlers. The only new contract is the
environment table in §4 plus documentation. `docs/03 §4` gains a pointer:
the webhook URL may be the funnel URL produced at boot.

## 7. Testing strategy

- **Unit (CI):**
  - config: defaults, trimming, off-mode ignoring every other variable,
    validation errors (both listeners off; bad bool in enabled mode).
  - funnel mux: table tests over the restricted handler — signed payload →
    receiver, tampered/missing signature, unconfigured provider, unsupported
    provider, and *every* non-hook path/method → 404; receiver-unmounted → 405.
    Pure handler tests; no tsnet involved.
  - wiring: `internal/tailscale` hides `tsnet.Server` behind a small interface
    (`Start`, `ListenFunnel`, `ListenTLS`, `Close`), letting daemon tests use a
    fake to assert listener/server lifecycles: enabled boot creates both
    servers wired to the expected handlers; off-mode boot creates none;
    shutdown closes everything; tsnet boot failure aborts startup with the
    retry budget.
- **Not CI-testable:** real enrollment, Funnel delivery, and cert issuance
  (requires the Tailscale control plane). Covered by the PR human-check list:
  node appears in admin console with the tag; signed GitHub webhook delivered
  to the funnel URL → 202 → runner spawns; tampered → 401; `GET /` on `:443`
  → 404; UI over `:8443` with HTTPS and login working from another tailnet
  device; LAN listener unaffected; supervisor restart reuses node identity
  (same URL, no re-enrollment); auth-key-less boot shows zero tailscale
  activity.
- **E2E suite:** untouched — the Playwright stack runs the supervisor without
  tailscale config; off-mode is asserted implicitly by every existing test.

## 8. Implementation plan

1. Pin `tailscale.com` in `go.mod` (version/toolchain decision per §2),
   re-run the CGO-free + size probe against the pinned release, record numbers
   in the PR.
2. `internal/config`: five fields, env registry entries, defaults, normalize,
   validation, unit tests.
3. `internal/tailscale`: config → `tsnet.Server` factory, listener opening,
   logger adapter, the small interface + fake for tests.
4. `cmd/runnero-supervisor/daemon.go`: wiring block after the RUN-153 receiver
   construction (order matters: the funnel mux needs `server.WebhookReceiver()`),
   funnel mux construction, two `http.Server`s, boot logs, shutdown hooks,
   wiring tests with the fake.
5. Deploy/docs: `.env.example` block, `docker-compose.yml` env pass-throughs
   (five lines, no ports/volumes), README (feature bullet, prerequisites +
   ACL snippet, env table, webhook-URL note), docs/03 §4 pointer. RUN-154 and
   this feature are documented as independent options.
6. Full gate suite in the nix shell; PR.

## 9. Open items for implementation

- ~~Exact `tailscale.com` pin and measured binary delta~~ **resolved:** pinned
  `tailscale.com v1.102.3` (`go.mod` `go` directive bumped to `1.26.6`; dev shell
  is on Go 1.27 since the RUN-155 toolchain pin). CGO-free re-verified on the
  real build; measured binary delta recorded in the implementation PR.
- ~~Upstream no-log-upload switch~~ **resolved:** `TS_NO_LOGS_NO_SUPPORT=true`
  disables log-tail uploads in v1.102.3 (verified in the module source,
  `envknob.NoLogsNoSupport` → logtail config); documented in the README
  Tailscale section. Default (upload on) stays, as disclosed in §5.
- If Funnel delivery of `X-Forwarded-For`/client IP ever matters (it doesn't
  for HMAC-verified webhooks), revisit; today the supervisor ignores client IPs.
