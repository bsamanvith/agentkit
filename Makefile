# Entry point for building, linting and testing. Environment work (containers,
# topologies) lives in scripts/dev.sh. See docs/ENGINEERING.md.

GO ?= go
GOLANGCI_VERSION := v2.14.0
GOLANGCI := bin/golangci-lint-$(GOLANGCI_VERSION)
ACTIONLINT_VERSION := v1.7.12
ACTIONLINT := bin/actionlint-$(ACTIONLINT_VERSION)
ZIZMOR_VERSION := 1.30.1
# Under -race the SQLite store's suites took about 11 minutes on 4 cores (most
# of it the race build of modernc.org/sqlite, not fsync: synchronous=NORMAL
# saved nothing), past go test's 10-minute default. T056 cut that about
# threefold (kept statements, one migration per test binary); the timeout
# stays as headroom. A timeout is not a pass.
TESTFLAGS ?= -race -shuffle=on -timeout 30m
FUZZTIME ?= 10s

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Compile every package
	$(GO) build ./...

# TEST_JSON=file makes make test (and so make check) keep go test's -json
# events in file, printing what go test would have printed anyway, for
# make test-timing and CI's slowest-tests step (scripts/test-timing.sh).
TEST_JSON ?=

.PHONY: test
test: ## Unit tests (race detector, shuffled order); TEST_JSON=file also keeps the -json events
ifeq ($(TEST_JSON),)
	$(GO) test $(TESTFLAGS) ./...
else
	@status=0; $(GO) test $(TESTFLAGS) -json ./... > $(TEST_JSON) || status=$$?; \
	scripts/test-timing.sh --output $(TEST_JSON); exit $$status
endif

.PHONY: test-timing
test-timing: ## Unit tests, then the slowest packages and top-level tests (TOP=10)
	@mkdir -p bin
	@status=0; $(MAKE) --no-print-directory test TEST_JSON=bin/test.json || status=$$?; \
	echo; scripts/test-timing.sh bin/test.json; exit $$status

.PHONY: test-integration
test-integration: ## Unit + integration tests against your own deps (AGENTKIT_PG_URL, AGENTKIT_REDIS_URL; see make integration)
	$(GO) test $(TESTFLAGS) -tags integration ./...

# make integration runs the integration tests with no Docker (cloud sessions):
# on SQLite by default, or STORE=postgres for CI's integration tier against a
# throwaway Postgres and Redis run as processes. See scripts/integration.sh.
STORE ?= sqlite
.PHONY: integration
integration: ## Integration tests, no Docker: STORE=sqlite (default, light) or postgres (CI's tier); live tests too if ANTHROPIC_API_KEY is set
	STORE=$(STORE) scripts/integration.sh

# LIVE_REQUIRED=1 makes a missing Anthropic key a failure instead of a run
# whose tests all skip: CI's live workflow (.github/workflows/live.yml) sets it.
LIVE_REQUIRED ?=

.PHONY: test-live
test-live: ## Live tests against real model providers (ANTHROPIC_API_KEY, or ANTHROPIC_KEY as in Claude sessions); LIVE_REQUIRED=1 fails without a key
	@if [ -n "$(LIVE_REQUIRED)" ] && [ -z "$${ANTHROPIC_API_KEY:-$$ANTHROPIC_KEY}" ]; then \
		echo "test-live: ANTHROPIC_API_KEY is not set, and LIVE_REQUIRED=1 needs it: the live tests would all skip." >&2; \
		echo "test-live: in GitHub Actions, add the repository secret ANTHROPIC_API_KEY (Settings > Secrets and variables > Actions)." >&2; \
		[ "$$GITHUB_ACTIONS" != true ] || echo "::error title=no ANTHROPIC_API_KEY::make test-live LIVE_REQUIRED=1: the repository secret ANTHROPIC_API_KEY is missing or empty, so the live tests did not run." >&2; \
		exit 1; \
	fi
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
lint: $(GOLANGCI) ## Run golangci-lint, then the skills and task-file lint (scripts/lint-skills.sh) and its fixtures, and scripts/test-timing.sh's and scripts/ci-changes.sh's
	$(GOLANGCI) run ./...
	scripts/lint-skills.sh
	scripts/lint-skills.sh --test >/dev/null
	scripts/test-timing.sh --test >/dev/null
	scripts/ci-changes.sh --test >/dev/null

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
check: tidy-check fmt-check lint workflow-lint deps-check test ## Everything a change must pass

.PHONY: workflow-lint
workflow-lint: $(ACTIONLINT) ## Lint .github/: actionlint, and zizmor when it or uv is installed (CI installs it)
	$(ACTIONLINT)
	@ZIZMOR_VERSION=$(ZIZMOR_VERSION) scripts/zizmor.sh

.PHONY: print-zizmor-version
print-zizmor-version:
	@echo $(ZIZMOR_VERSION)

$(ACTIONLINT):
	@mkdir -p bin
	GOBIN=$(CURDIR)/bin GOTOOLCHAIN=$$($(GO) env GOVERSION) $(GO) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	mv bin/actionlint $@

.PHONY: tools
tools: $(GOLANGCI) ## Install the pinned linter into bin/

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
	PNPM=$(PNPM) web/scripts/audit.sh
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
# drivers joined the one default binary (SQLite: about 21 MB, then pgx; E18),
# then to 35 and 37 as feature tasks landed (docs/SIZES.md says why).
# Log every size change, with its reason, in docs/SIZES.md (AGENTS.md, Sizes).
# An image with a budget of its own (IMAGE_BUDGET_MB_<image>) gets that one.
IMAGE_BUDGET_MB ?= $(or $(IMAGE_BUDGET_MB_$(IMAGE)),37)
# fakellm (T004) is a test-only image, standard library only: 12 MB, about
# 1 MB over its binary plus the base. Growth there means something linked in.
IMAGE_BUDGET_MB_fakellm ?= 12
# Variants of the agentkit image, by the build tags that leave parts out
# (cmd/agentkit/backends.go). `make image-<variant>` builds agentkit-<variant>;
# `make image` is the all-in-one, which runs any set of components.
#   local     one machine: memory or SQLite, no Postgres, no Redis, no migrate
#   dev       deployed replicas: memory or Postgres, and Redis; no SQLite
# The split deployment (T102, docs/COMPONENTS.md), one image per component:
#   api       agent API and console; agents run on a runner, no model SDK,
#             no connectors, no MCP server
#   runner    the agents' turn loops: the model SDK; toolsets come from the
#             toolsets component; no agent API, store, bus, console, MCP or
#             A2A server
#   toolsets  the connectors, to runners (/rpc/toolsets/) and over MCP; no
#             model SDK, agent API, store, bus, console or A2A server
VARIANTS := local dev api runner toolsets
TAGS_local := nopostgres noredis
TAGS_dev := nosqlite
TAGS_api := nomodels noconnectors nomcp nosqlite
TAGS_runner := noconnectors nomcp noweb noa2a nosqlite nopostgres noredis noagent
TAGS_toolsets := nomodels noweb noa2a nosqlite nopostgres noredis noagent
IMAGE_BUDGET_MB_local ?= 31
IMAGE_BUDGET_MB_dev ?= 32
IMAGE_BUDGET_MB_api ?= 23
IMAGE_BUDGET_MB_runner ?= 20
IMAGE_BUDGET_MB_toolsets ?= 18
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

# The split deployment on Docker Desktop's Kubernetes (deploy/k8s/README.md).
# scripts/k8s-local.sh never creates a cluster; KUBE_CONTEXT picks another.
.PHONY: k8s-up k8s-status k8s-smoke k8s-down
k8s-up: ## Build the split images and deploy them, with Postgres, to Docker Desktop's Kubernetes; console on http://localhost:30080
	scripts/k8s-local.sh up
k8s-status: ## Show the local deployment's pods, services and console URL
	scripts/k8s-local.sh status
k8s-smoke: ## Run the smoke suite (test/e2e) against the local deployment; one chat run calls the model
	scripts/k8s-local.sh smoke
k8s-down: ## Delete the local deployment: its pods, secret and Postgres data
	scripts/k8s-local.sh down

# The smoke suite (test/e2e, T007): a few black-box flows against a running
# deployment. make smoke runs the split deployment as processes, on a fake
# model; make k8s-smoke runs the suite against make k8s-up's.
.PHONY: smoke
smoke: ## Smoke suite against the split deployment run as local processes (fake model; no Docker, no key)
	scripts/smoke.sh

.PHONY: $(addprefix print-tags-,$(VARIANTS))
$(addprefix print-tags-,$(VARIANTS)): print-tags-%:
	@echo $(TAGS_$*)
