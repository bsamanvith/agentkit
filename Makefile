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

.PHONY: record-cassettes
record-cassettes: ## Re-record connector cassettes from the real APIs (needs the providers' keys; review the diff, then make golden)
	AGENTKIT_RECORD_CASSETTES=1 $(GO) test -tags live -count=1 -run 'RecordCassettes' ./...

OPENAPI_CUT := $(GO) run ./pkg/toolset/toolsettest/testdata/openapi/cut.go
EXA_SPEC := https://exa.ai/docs/exa-spec.yaml
PARALLEL_SPEC := https://docs.parallel.ai/public-openapi.json
TAVILY_DOCS := https://docs.tavily.com/documentation/api-reference/endpoint

.PHONY: openapi-specs
openapi-specs: ## Re-vendor the providers' OpenAPI specs (connectors/*/testdata/openapi.json; review the diff, then make test)
	@d=$$(mktemp -d) && trap 'rm -rf "$$d"' EXIT && \
	curl -fsSL -o $$d/exa.yaml $(EXA_SPEC) && \
	curl -fsSL -o $$d/parallel.json $(PARALLEL_SPEC) && \
	curl -fsSL -o $$d/search.md $(TAVILY_DOCS)/search.md && \
	curl -fsSL -o $$d/extract.md $(TAVILY_DOCS)/extract.md && \
	$(OPENAPI_CUT) -o connectors/exa/testdata/openapi.json -source $(EXA_SPEC) \
		-op 'POST /search' -op 'POST /contents' $$d/exa.yaml && \
	$(OPENAPI_CUT) -o connectors/parallel/testdata/openapi.json -source $(PARALLEL_SPEC) \
		-op 'POST /v1/search' -op 'POST /v1/extract' $$d/parallel.json && \
	$(OPENAPI_CUT) -o connectors/tavily/testdata/openapi.json -overlay connectors/tavily/testdata/openapi-overlay.json -source $(TAVILY_DOCS)/search.md -source $(TAVILY_DOCS)/extract.md \
		-op 'POST /search' -op 'POST /extract' $$d/search.md $$d/extract.md

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
check: tidy-check fmt-check lint deps-check test ## Everything a change must pass

$(GOLANGCI):
	@mkdir -p bin
	@# Build the linter with this repo's toolchain: it refuses to lint code that
	@# targets a newer Go than the one it was built with.
	GOBIN=$(CURDIR)/bin GOTOOLCHAIN=$$($(GO) env GOVERSION) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	mv bin/golangci-lint $@

# The web workspace (web/, see web/README.md): Node and pnpm only here; go
# build and the image use the committed build in pkg/web/dist. Kept out of
# check so Go feedback stays fast; CI runs it as its own job.
PNPM ?= pnpm

.PHONY: web
web: ## Build the console into pkg/web/dist (commit it with the sources)
	$(PNPM) --dir web install --frozen-lockfile
	$(PNPM) --dir web build

.PHONY: web-check
web-check: ## Web: lint, format, typecheck, unit tests, audit, build; fail if pkg/web/dist is stale or over budget
	$(PNPM) --dir web install --frozen-lockfile
	$(PNPM) --dir web lint
	$(PNPM) --dir web format:check
	$(PNPM) --dir web typecheck
	$(PNPM) --dir web test
	$(PNPM) --dir web audit --audit-level high
	web/scripts/check-build.sh
	web/scripts/check-size.sh

.PHONY: web-smoke
web-smoke: ## Browser smoke test (Playwright) against agentkit serve; uses installed browsers, never downloads
	$(GO) build -o bin/agentkit-e2e ./cmd/agentkit
	AGENTKIT_BIN=$(CURDIR)/bin/agentkit-e2e $(PNPM) --dir web e2e

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
# 17.7 MB in T005 (5.9 MB compressed). Raised from 20 to 30 when the store
# drivers joined the one default binary (SQLite: about 21 MB, then pgx; E18).
# Log every size change, with its reason, in docs/SIZES.md (AGENTS.md, Sizes).
IMAGE_BUDGET_MB ?= 35
# Variants of the agentkit image, by the build tags that leave parts out
# (cmd/agentkit/backends.go). `make image-<variant>` builds agentkit-<variant>;
# `make image` is the all-in-one, which runs any set of components.
#   local     one machine: memory or SQLite, no Postgres, no Redis, no migrate
#   dev       deployed replicas: memory or Postgres, and Redis; no SQLite
# The split deployment (T102, docs/COMPONENTS.md), one image per component:
#   api       agent API and console; agents run on a runner, no model SDK,
#             no connectors, no MCP server
#   runner    the agents' turn loops: the model SDK; toolsets come from the
#             toolsets component; no store, bus, console or MCP server
#   toolsets  the connectors, to runners (/rpc/toolsets/) and over MCP; no
#             model SDK, store, bus or console
VARIANTS := local dev api runner toolsets
TAGS_local := nopostgres noredis
TAGS_dev := nosqlite
TAGS_api := nomodels noconnectors nomcp nosqlite
TAGS_runner := noconnectors nomcp noweb nosqlite nopostgres noredis
TAGS_toolsets := nomodels noweb nosqlite nopostgres noredis
IMAGE_BUDGET_MB_local ?= 26
IMAGE_BUDGET_MB_dev ?= 30
IMAGE_BUDGET_MB_api ?= 22
IMAGE_BUDGET_MB_runner ?= 20
IMAGE_BUDGET_MB_toolsets ?= 17
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

# The split deployment: one image per component.
SPLIT := api runner toolsets

.PHONY: images-split
images-split: ## Build agentkit-api, agentkit-runner and agentkit-toolsets
	@set -e; for v in $(SPLIT); do $(MAKE) --no-print-directory image-$$v; done

.PHONY: deps-check
deps-check: ## Fail if a variant's binary links a dependency its build tags leave out
	@$(foreach v,$(VARIANTS),TAGS_$(v)="$(TAGS_$(v))") scripts/check-deps.sh $(VARIANTS)

.PHONY: $(addprefix image-,$(VARIANTS)) $(addprefix image-size-,$(VARIANTS))
$(addprefix image-,$(VARIANTS)): image-%: ## Build agentkit-<variant>: local, dev, api, runner or toolsets (see VARIANTS)
	@$(MAKE) --no-print-directory image BIN=agentkit IMAGE=agentkit-$* TAGS="$(TAGS_$*)"
$(addprefix image-size-,$(VARIANTS)): image-size-%: ## Check agentkit-<variant> against its budget
	@$(MAKE) --no-print-directory image-size IMAGE=agentkit-$* IMAGE_BUDGET_MB=$(IMAGE_BUDGET_MB_$*)

.PHONY: image-size
image-size: ## Fail if the built image (BIN=..., VERSION=...) is over IMAGE_BUDGET_MB
	@id=$$($(DOCKER) create --pull=never $(IMAGE):$(VERSION)) || exit 1; \
	size=$$($(DOCKER) export $$id | wc -c); $(DOCKER) rm $$id >/dev/null; \
	budget=$$(( $(IMAGE_BUDGET_MB) * 1000 * 1000 )); \
	echo "$(IMAGE):$(VERSION): $$size bytes uncompressed, budget $$budget ($(IMAGE_BUDGET_MB) MB)"; \
	test $$size -gt 0 -a $$size -le $$budget || { echo "$(IMAGE):$(VERSION) is over its size budget" >&2; exit 1; }
