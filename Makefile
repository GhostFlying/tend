# Single entry point for local checks and CI.

GO ?= go
STATICCHECK_VERSION ?= v0.8.2
PKGS := ./...

.PHONY: check fmt-check vet staticcheck test it build

## check: everything that must pass before a commit is merged
check: fmt-check vet staticcheck test

fmt-check:
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet $(PKGS)

staticcheck:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) $(PKGS)

test:
	$(GO) test -race $(PKGS)

## it: integration tests that need a real harness or tailnet
it:
	$(GO) test -race -tags integration $(PKGS)

build:
	$(GO) build $(PKGS)
