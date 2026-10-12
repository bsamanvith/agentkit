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
test-live: ## Live tests against real model providers (ANTHROPIC_API_KEY, or ANTHROPIC_KEY as in Claude sessions)
	ANTHROPIC_API_KEY="$${ANTHROPIC_API_KEY:-$$ANTHROPIC_KEY}" $(GO) test $(TESTFLAGS) -tags live -count=1 ./...

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

# Images (see Dockerfile). BIN picks the binary in cmd/, TAGS its build tags.
DOCKER ?= docker
comma := ,
BIN ?= agentkit
TAGS ?=
GO_VERSION ?= $(shell sed -n 's/^toolchain go//p' go.mod)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
# The commit's date, not the build's, so rebuilding a commit gives the same image.
DATE ?= $(shell git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null)
IMAGE ?= $(BIN)
# Budget for an image's size: its files, uncompressed (the flattened
# filesystem, the same whatever docker's image store). agentkit measured
# 17.7 MB in T005 (5.9 MB compressed); PLAN.md targets 10-20 MB.
IMAGE_BUDGET_MB ?= 20
# Binaries that get an image; fakellm joins when cmd/fakellm exists (T004).
IMAGE_BINS := $(notdir $(wildcard $(addprefix cmd/,agentkit fakellm)))
# An extra CA for the module download, behind a TLS-intercepting proxy.
BUILD_CA ?=

.PHONY: image
image: ## Build an image: BIN=agentkit|fakellm TAGS=... VERSION=... (tags :VERSION and :latest)
	$(DOCKER) build \
		$(if $(BUILD_CA),--secret id=ca$(comma)src=$(BUILD_CA)) \
		--build-arg GO_VERSION=$(GO_VERSION) \
		--build-arg BIN=$(BIN) \
		--build-arg TAGS="$(TAGS)" \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

.PHONY: images
images: ## Build the image of every binary in cmd/
	@set -e; for b in $(IMAGE_BINS); do $(MAKE) --no-print-directory image BIN=$$b IMAGE=$$b; done

.PHONY: image-size
image-size: ## Fail if the built image (BIN=..., VERSION=...) is over IMAGE_BUDGET_MB
	@id=$$($(DOCKER) create --pull=never $(IMAGE):$(VERSION)) || exit 1; \
	size=$$($(DOCKER) export $$id | wc -c); $(DOCKER) rm $$id >/dev/null; \
	budget=$$(( $(IMAGE_BUDGET_MB) * 1000 * 1000 )); \
	echo "$(IMAGE):$(VERSION): $$size bytes uncompressed, budget $$budget ($(IMAGE_BUDGET_MB) MB)"; \
	test $$size -gt 0 -a $$size -le $$budget || { echo "$(IMAGE):$(VERSION) is over its size budget" >&2; exit 1; }
