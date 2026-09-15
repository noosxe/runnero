# runnero — Multi-Provider Actions Runner & Supervisor
#
# All targets are intended to run inside the Nix development shell:
#   nix develop --command make <target>

# The supervisor is strictly CGO-free (docs/06 §1: pure-Go stack for
# seamless ARM64/AMD64 cross-compilation). Force it so host toolchains
# with a C compiler present (e.g. the Nix development shell) cannot sneak
# in dynamic glibc linking, which breaks test binaries whose interpreter
# path no longer exists in the Nix store.
export CGO_ENABLED := 0

BINARY := runnero-supervisor
PKG     := ./...

.PHONY: build build-web build-image-runner build-image-supervisor test test-race test-scripts test-web test-e2e test-e2e-ui clean-e2e lint lint-web fmt fmt-web vet tidy clean generate proto-lint launch stop restart logs status

## generate: run code generation tools (sqlc, buf)
generate:
	sqlc generate
	buf generate proto

## proto-lint: lint protobuf schemas via buf
proto-lint:
	buf lint proto

## build-web: compile Vite/React frontend SPA into web/dist
build-web:
	cd web && pnpm run build

## build: compile all packages and produce the supervisor binary
build: build-web
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/$(BINARY)

## build-image-supervisor: build local supervisor container image
build-image-supervisor:
	docker build -f Dockerfile.supervisor -t ghcr.io/noosxe/runnero-supervisor:local .

## test: run the Go test suite
test: build-web
	go test $(PKG)

## test-race: run the Go test suite with data race detection (requires CGO)
test-race: build-web
	CGO_ENABLED=1 go test -race $(PKG)

## test-web: run frontend Vitest suite
test-web:
	cd web && pnpm test

## lint-web: run frontend Oxlint and Oxfmt checks
lint-web:
	cd web && pnpm run lint && pnpm run format:check

## fmt-web: format frontend sources in place with oxfmt
fmt-web:
	cd web && pnpm run format

## build-image-runner: build local runner container image
build-image-runner:
	docker build -f Dockerfile -t ghcr.io/noosxe/runnero:local \
		--build-arg IMAGE_VERSION=24.04.$$(date +%Y%m) .

## test-scripts: run unit tests for runner image scripts
test-scripts:
	bash tests/unit/entrypoint_test.sh
	bash tests/unit/parity_packages_test.sh
	bash tests/unit/playwright_lockstep_test.sh

## lint: static analysis via golangci-lint
lint: build-web
	golangci-lint run

## fmt: format all Go sources in place
fmt:
	gofmt -w .

## vet: lightweight static checks
vet: build-web
	go vet $(PKG)

## tidy: prune and re-pin module dependencies
tidy:
	go mod tidy

## clean: remove build artifacts
clean: clean-e2e
	rm -rf bin web/dist

## test-e2e: run containerized Playwright E2E tests
# --- E2E stack collision guards (RUN-208) -----------------------------------
# The self-hosted CI runner shares one Docker engine with local development,
# and both drive the same compose file. Three guards keep them apart:
#   1. Project namespacing: CI (GitHub Actions sets CI=true) keeps the
#      workflow's fixed project `runnero-e2e`; manual runs get a per-user
#      project, so neither side can touch the other's containers, networks,
#      or volumes (COMPOSE_PROJECT_NAME overrides the stack's `name:`).
#   2. flock: same-project invocations (two local terminals, or the CI job's
#      own test -> clean steps) serialize on a per-project lockfile instead
#      of racing; the wait is capped by E2E_LOCK_WAIT.
#   3. In-flight guard: clean-e2e refuses to tear down while the suite's
#      Playwright container still runs -- the RUN-208 failure mode. Override
#      with E2E_FORCE_CLEAN=1 (the CI teardown sets it: after a job timeout
#      the suite is already dead and cleanup must still proceed).
E2E_PROJECT ?= $(if $(CI),runnero-e2e,runnero-e2e-$(shell id -un 2>/dev/null || echo local))
# COMPOSE_PROJECT_NAME is deliberately NOT exported globally: it is scoped
# to the three E2E targets below, so the deployment compose targets
# (launch/stop/status/...) keep the project the compose file implies
# (`runnero`, derived from the directory) instead of the E2E-local one.
test-e2e test-e2e-ui clean-e2e: export COMPOSE_PROJECT_NAME := $(E2E_PROJECT)
E2E_COMPOSE := docker compose -f tests/e2e/docker-compose.e2e.yml
E2E_LOCK := /tmp/$(E2E_PROJECT).lock
E2E_LOCK_WAIT ?= 600

## test-e2e: run containerized Playwright E2E tests
test-e2e:
	# Fresh state must not depend on the previous run's teardown: the supervisor
	# keeps its database in an anonymous volume (VOLUME /data), so a stack left
	# behind by a killed run would serve the next run stale state (RUN-166:
	# flow-01 saw adminCreated=true from a leftover mid-onboarding DB and the
	# wizard rendered its login branch instead of step 1). Surface down errors
	# instead of swallowing them, and recreate containers with renewed
	# anonymous volumes so every boot starts empty even if cleanup leaked.
	flock -w $(E2E_LOCK_WAIT) $(E2E_LOCK) bash -c '\
		$(E2E_COMPOSE) down -v --remove-orphans && \
		$(E2E_COMPOSE) up \
			--build \
			--force-recreate \
			--renew-anon-volumes \
			--abort-on-container-exit \
			--exit-code-from e2e-playwright'

## test-e2e-ui: run Playwright E2E tests with UI mode on port 9323
test-e2e-ui:
	flock -w $(E2E_LOCK_WAIT) $(E2E_LOCK) $(E2E_COMPOSE) run \
		--rm -p 9323:9323 e2e-playwright pnpm exec playwright test --ui-port=9323 --ui-host=0.0.0.0

## clean-e2e: clean up E2E containers, networks, and scratch volumes
clean-e2e:
	@if [ -n "$${E2E_FORCE_CLEAN:-}" ] || ! docker ps -q \
			--filter "label=com.docker.compose.project=$(E2E_PROJECT)" \
			--filter "label=com.docker.compose.service=e2e-playwright" \
			--filter status=running | grep -q .; then \
		flock -w $(E2E_LOCK_WAIT) $(E2E_LOCK) $(E2E_COMPOSE) down -v --remove-orphans 2>/dev/null || true; \
	else \
		echo "clean-e2e: an E2E suite is still running in project $(E2E_PROJECT) - refusing to tear it down (E2E_FORCE_CLEAN=1 overrides)"; \
	fi

# --- Deployment compose stack (docker-compose.yml) -------------------------
# Lifecycle wrappers for the self-hosted stack. Plain docker compose calls,
# so these work outside the Nix development shell too.

## launch: start the deployment stack in the background
launch:
	docker compose -f docker-compose.yml up -d

## stop: stop and remove the deployment stack
stop:
	docker compose -f docker-compose.yml down

## restart: restart the deployment stack (stop, then launch)
restart: stop launch


## rebuild: rebuild the local supervisor image and restart the deployment stack
rebuild:
	make build-image-supervisor
	docker compose -f docker-compose.yml down
	docker compose -f docker-compose.yml up -d

## logs: follow the deployment stack's logs
logs:
	docker compose -f docker-compose.yml logs -f

## status: show the deployment stack's container status
status:
	docker compose -f docker-compose.yml ps
