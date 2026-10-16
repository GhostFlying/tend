# Single entry point for local checks and CI.

GO ?= go
STATICCHECK_VERSION ?= v0.8.2
PKGS := ./...

.PHONY: check fmt-check vet staticcheck test it build gen

## check: everything that must pass before a commit is merged
check: fmt-check vet staticcheck test

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
	$(GO) test -race $(PKGS)

## it: integration tests that need a real harness or tailnet
it:
	$(GO) test -race -tags integration -timeout 60m $(PKGS)

build:
	$(GO) build $(PKGS)

## gen: regenerate the owner API schema
gen:
	$(GO) run ./internal/protocol/schemagen
