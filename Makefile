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

.PHONY: build build-web build-image-runner build-image-supervisor test test-race test-scripts test-web test-e2e test-e2e-ui clean-e2e lint lint-web fmt fmt-web vet tidy clean generate proto-lint

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
test-e2e:
	# Fresh state must not depend on the previous run's teardown: the supervisor
	# keeps its database in an anonymous volume (VOLUME /data), so a stack left
	# behind by a killed run would serve the next run stale state (RUN-166:
	# flow-01 saw adminCreated=true from a leftover mid-onboarding DB and the
	# wizard rendered its login branch instead of step 1). Surface down errors
	# instead of swallowing them, and recreate containers with renewed
	# anonymous volumes so every boot starts empty even if cleanup leaked.
	docker compose -f tests/e2e/docker-compose.e2e.yml down -v --remove-orphans
	docker compose -f tests/e2e/docker-compose.e2e.yml up \
		--build \
		--force-recreate \
		--renew-anon-volumes \
		--abort-on-container-exit \
		--exit-code-from e2e-playwright

## test-e2e-ui: run Playwright E2E tests with UI mode on port 9323
test-e2e-ui:
	docker compose -f tests/e2e/docker-compose.e2e.yml run \
		--rm -p 9323:9323 e2e-playwright pnpm exec playwright test --ui-port=9323 --ui-host=0.0.0.0

## clean-e2e: clean up E2E containers, networks, and scratch volumes
clean-e2e:
	docker compose -f tests/e2e/docker-compose.e2e.yml down -v --remove-orphans 2>/dev/null || true

