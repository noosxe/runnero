# AGENTS.md

Working rules for AI agents in this repository. Follow precisely.

Runnero is a self-hosted, multi-provider (GitHub/Gitea/Forgejo) Actions runner
supervisor: a CGO-free Go daemon with an Echo/ConnectRPC web control plane and
a Vite/React frontend, orchestrating ephemeral one-job runner containers.
Architecture details live in `docs/02-architecture-design.md`.

## Ground rules

- **`main` is protected.** All changes go through PRs; the product owner
  reviews, human-tests, and merges. Never push to `main`, never commit
  directly to it. Branch names are prefixed by the nature of the change:
  `feature/`, `bug/`, `docs/`, `refactor/`, `test/`, `ci/`
  (e.g. `bug/fix-memory-leak`).
- **Everything runs in the Nix dev shell.** Never run builds, tests, linters,
  or Docker tooling directly on the host. One-off commands:
  `nix develop --command <command>`; persistent shell: `nix develop` (direnv
  is configured).
- **Stay focused.** Keep work scoped to the current task. If something
  unrelated needs fixing or you see an improvement opportunity, file a Linear
  issue — do not fold it into the current PR.
- **Ask for help freely.** Do not guess where a decision is the product
  owner's to make. Blocking questions are cheap; wrong assumptions are not.
- **Never blindly discard work.** When in doubt about changes, stop and ask
  the user; stash with `git stash` rather than resetting.
- **No AI attribution in commits.** No "co-authored by AI" statements — the
  repository README already carries a global notice.
- **MCP servers are available** (not connected by default, but discoverable)
  and may be used when needed — Linear for project management (see below),
  plus web search and context7 for verifying upstream sources. Never design or
  implement against remembered API details; verify against pinned upstream
  code/docs.

## Project management — Linear

Work is tracked in Linear via the Linear MCP server, not GitHub issues:

- Unrelated bugs and improvement ideas spotted while working are filed as
  Linear issues, not fixed opportunistically.
- Big features get a Linear issue for implementation after their design-doc
  PR merges (see workflow below); split into multiple issues if the scope is
  too big for one.
- Keep issues you own updated: link the PR, post progress comments, close on
  merge.

## Documentation

- **Every feature must be reflected in the docs.** The design docs in `docs/`
  are the source of truth and must always be up to date with the shipped
  behavior when a PR lands.
- Design docs are numbered `docs/NN-slug.md` (two digits, next free number —
  e.g. `docs/22-next-feature.md`).
- **No docs lifecycle.** There is no draft/accepted/implemented state
  tracking: docs are written and sent with the PR that carries them (the
  design-doc PR for big features, the implementation PR for small ones), and
  **a merged PR means the docs are accepted**. Never open separate
  status-flip PRs after implementation.

## Feature workflow

### Big features — design first

1. **Design doc first, as its own PR.** The PR contains the design doc and
   nothing else (docs-only). It covers architecture, protocol changes, and
   security implications, and adds the feature to the **Roadmap** section of
   `README.md` marked *[Design Phase]*.
2. The product owner reviews and merges the design-doc PR.
3. After the design-doc PR is merged, the agent must:
   - clean up related stale branches (remote and local);
   - file a Linear issue for the implementation — split into multiple issues
     if the scope is too big for a single issue;
   - **ask for the go-ahead**;
   - wait for the product owner's go before making any code changes.
4. The implementation PR carries code, tests, and the README update: remove
   the feature from the **Roadmap** and list it under **Features**.

### Small requested features — one PR

- No separate doc PR and no Linear issue: documentation and code changes go
  into the **same PR**.
- Like all feature PRs, the product owner is responsible for human tests and
  merging.

### PR discipline (both flows)

- **PRs stay focused.** Any issue that does not directly interfere with the
  current feature must be filed on Linear for later pickup. Suggested
  improvements and follow-ups must also be filed, not implemented
  opportunistically.
- **PRs stay focused.** Any issue that does not directly interfere with the
  current feature must be filed on Linear for later pickup. Suggested
  improvements and follow-ups must also be filed, not implemented
  opportunistically.
- **Don't babysit PRs.** Commit, push, and move on — do not poll CI status
  or sleep/wait on checks. The product owner reports CI failures back when
  they need fixing. Only watch CI when the user explicitly asks for it.
- PR bodies carry a human-check list — interactive/visual verification is the
  product owner's job. For UI-facing changes, exercise the change through the
  E2E suite first (see below) and say so in the PR body.
- Run the gates before pushing (inside the Nix shell):

  ```bash
  nix develop --command bash -c "make test && make test-scripts && make test-web && make lint && make lint-web && make vet && make build && shellcheck src/*.sh && shfmt -d src/*.sh && hadolint Dockerfile Dockerfile.supervisor"
  ```

## Debugging & verification

All verification happens inside the Nix dev shell; the `Makefile` is the
single entry point:

- **Go unit tests:** `make test` (compiles the frontend first);
  `make test-race` adds data-race detection.
- **Runner-image shell scripts:** `make test-scripts` (bash unit tests in
  `tests/unit/`); lint with `shellcheck src/*.sh`, format-check with
  `shfmt -d src/*.sh`.
- **Frontend:** `make test-web` (Vitest), `make lint-web` (oxlint + oxfmt).
  Use `pnpm` exclusively — never npm or yarn.
- **End-to-end:** `make test-e2e` runs the containerized Playwright suite via
  `docker compose -f tests/e2e/docker-compose.e2e.yml`; `make test-e2e-ui`
  opens interactive UI mode on port 9323. Tear down with `make clean-e2e`.
- **Static analysis:** `make lint` (golangci-lint), `make vet`,
  `make proto-lint` (buf), `hadolint Dockerfile Dockerfile.supervisor`.
- **Builds:** `make build` (supervisor binary), `make build-web`,
  `make build-image-runner` / `make build-image-supervisor` for local
  container images.

Rules:

- Verify behavior with the relevant gate or a focused
  `go test -run '<TestName>' ./internal/...` instead of guessing from code.
- Never commit a fix you cannot reproduce or verify.
- `make clean` removes build artifacts (`bin/`, `web/dist/`) and tears down
  E2E scratch state.

## Resource ownership — hands off what you don't own

- **Never stop, restart, or delete Docker resources you did not create**
  (containers, images, networks) without explicit permission. Check
  `docker ps` / `docker compose ls` first: only resources started by this
  session (the E2E stack, locally built runner images) are fair game.
- **Never delete files, volumes, or databases you did not create** without
  explicit permission — this includes `.env` files, data directories, and
  named volumes attached to other stacks.
- If a resource you do not own blocks your work (port conflict, name
  collision), ask the owner instead of force-removing it.

## Security

Runners execute untrusted workflow code; security is the absolute priority.

### Credential & token safety

- **Zero-leak policy:** never hardcode PATs, runner registration tokens, or
  any sensitive API keys in the Dockerfile, scripts, workflow files, or
  documentation.
- **Environment templates:** configuration templates live in `.env.example`;
  real `.env` files must stay in `.gitignore`.
- **Token lifetime:** prefer short-lived runner registration tokens over
  highly privileged PATs wherever possible.

### Container hardening

- **Non-root execution:** the runner process runs as a dedicated non-root
  user (`runner:runner`). Root is only acceptable where required for
  Docker-in-Docker, and rootless options are preferred even then.
- **Minimal base image:** lightweight, minimal base images only; multi-stage
  builds keep the final runner image slim.
- **Least privilege:** document minimal Docker configurations (e.g.
  `cap_drop`) to minimize host exposure.
- **Hadolint compliance:** Dockerfile statements must conform to hadolint
  rules (pin package versions, clean package caches in the same `RUN` layer).

## After merge — cleanup procedure

1. Hard-reset local `main` to `origin/main`
   (`git fetch && git reset --hard origin/main`).
2. Delete merged branches: remote branches are usually auto-deleted; local
   branches need manual `-d` (use `-D` after a rebase-merge, the squashed
   commit SHA will not match).
3. Do not re-run the local test suite — cleanup is housekeeping only:
   CI on `main` has already run the gates, and re-running everything locally
   wastes time. Run targeted checks only if something looks off, or the full
   gates if the user explicitly asks.
4. Tear down the E2E stack (`make clean-e2e`) and remove scratch files
   (e.g. under `/tmp`) used for smoke runs.
