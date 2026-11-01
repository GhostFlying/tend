# Single entry point for local checks and CI.

GO ?= go
PNPM ?= pnpm
STATICCHECK_VERSION ?= v0.8.2
PKGS := ./...
TEST_TIMEOUT ?= 60m
APP_TEST_SHARDS ?= 1
WEB := web

.PHONY: check fmt-check vet staticcheck test it build gen web web-deps web-check

## check: everything that must pass before a commit is merged
check: capability-check protocol-check fmt-check vet staticcheck test web-check

fmt-check:
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet $(PKGS)

# go run pkg@version builds outside this module, so it would pick the Go
# release staticcheck's own go.mod asks for, which may be too old to load
# this module. Pin it to the toolchain that builds the module.
staticcheck:
	GOTOOLCHAIN=$$($(GO) env GOVERSION) $(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) $(PKGS)

test:
ifeq ($(APP_TEST_SHARDS),1)
	$(GO) test -race -timeout $(TEST_TIMEOUT) $(PKGS)
else
	$(GO) run ./internal/testkit/racecheck -shards $(APP_TEST_SHARDS) -timeout $(TEST_TIMEOUT) $(PKGS)
endif

## it: integration tests that need a real harness or tailnet
it:
	$(GO) test -race -tags integration -timeout 60m $(PKGS)

build:
	$(GO) build $(PKGS)

## gen: regenerate the owner API schema and the web client's types from it
gen: web-deps
	$(GO) run ./internal/protocol/schemagen
	$(PNPM) --dir $(WEB) gen

## web: build the web client into internal/webui/dist/app, which tend-server
## embeds; build it before building tend-server
web: web-deps
	$(PNPM) --dir $(WEB) build

web-deps:
	$(PNPM) --dir $(WEB) install --frozen-lockfile

## web-check: the web client's typecheck, lint, format check, unit tests and build
web-check: web-deps
	$(PNPM) --dir $(WEB) check

.PHONY: protocol-check
protocol-check:
	$(GO) run ./internal/protocol/compatcheck/cmd

.PHONY: sandbox
## sandbox: build Web and run an isolated scripted service (never a release)
SANDBOX_DATA ?=
SANDBOX_ARGS ?=
sandbox: web
	@data="$(SANDBOX_DATA)"; temporary=; \
	if [ -z "$$data" ]; then data=$$(mktemp -d); data=$$(cd "$$data" && pwd -P); temporary=1; fi; \
	trap 'if [ -n "$$temporary" ]; then rm -rf "$$data"; fi' EXIT; \
	$(GO) run ./cmd/tend-sandbox -data "$$data" $(SANDBOX_ARGS)

.PHONY: capability-check
## capability-check: public interfaces must have executable, documented CLI entries
capability-check:
	$(GO) test ./internal/api -run '^TestPublicCapability' -count=1
	$(GO) test ./internal/cli -run '^TestCapability' -count=1
