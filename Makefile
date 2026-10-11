# Entry point for building, linting and testing. Environment work (containers,
# topologies) lives in scripts/dev.sh. See docs/ENGINEERING.md.

GO ?= go
GOLANGCI_VERSION := v2.14.0
GOLANGCI := bin/golangci-lint-$(GOLANGCI_VERSION)
TESTFLAGS ?= -race -shuffle=on
FUZZTIME ?= 10s

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Compile every package
	$(GO) build ./...

.PHONY: test
test: ## Unit tests (race detector, shuffled order)
	$(GO) test $(TESTFLAGS) ./...

.PHONY: test-integration
test-integration: ## Unit + integration tests (needs: scripts/dev.sh up deps)
	$(GO) test $(TESTFLAGS) -tags integration ./...

.PHONY: test-live
test-live: ## Live tests against real model providers (needs API keys)
	$(GO) test $(TESTFLAGS) -tags live -count=1 ./...

.PHONY: golden
golden: ## Rewrite golden files from current output (review the diff!)
	AGENTKIT_UPDATE_GOLDEN=1 $(GO) test -count=1 ./...

.PHONY: fuzz
fuzz: ## Run every fuzz target for FUZZTIME (default 10s)
	scripts/fuzz.sh $(FUZZTIME)

.PHONY: lint
lint: $(GOLANGCI) ## Run golangci-lint
	$(GOLANGCI) run ./...

.PHONY: fmt
fmt: $(GOLANGCI) ## Format code in place
	$(GOLANGCI) fmt ./...

.PHONY: fmt-check
fmt-check: $(GOLANGCI) ## Fail if code is not formatted
	$(GOLANGCI) fmt --diff ./...

.PHONY: tidy-check
tidy-check: ## Fail if go.mod/go.sum are not tidy
	$(GO) mod tidy -diff

.PHONY: check
check: tidy-check fmt-check lint test ## Everything a change must pass

$(GOLANGCI):
	@mkdir -p bin
	@# Build the linter with this repo's toolchain: it refuses to lint code that
	@# targets a newer Go than the one it was built with.
	GOBIN=$(CURDIR)/bin GOTOOLCHAIN=$$($(GO) env GOVERSION) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	mv bin/golangci-lint $@
