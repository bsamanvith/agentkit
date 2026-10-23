# agentkit: Engineering Plan

> Status: agreed and in use. How we design modules, how we test them, how we run the system locally, and the agent skills that encode all of it.
> M0 put the tooling and test rules in place ([E01](../tasks/E01-foundations.md)); `pkg/tool` ([E03](../tasks/E03-tool-core-and-mcp.md)) is the first package built to these rules and the reference example. Skills: [E02](../tasks/E02-agent-skills.md).

This complements [PLAN.md](PLAN.md) (what we build) with how we build it.

---

## 0. Build order

Build the product from the inside out. Add infrastructure when there is something real for it to run or guide, not before.

| Milestone | What | Epic | Proves |
|---|---|---|---|
| **M0** *(done)* | `make check`, lint, architecture test, test helpers, minimal CI | [E01](../tasks/E01-foundations.md) (T001, T002) | Every later change is checked the same way |
| **M1** *(done)* | `pkg/tool`: definition, `tool.Func[In, Out]`, registry, middleware, conformance suite | [E03](../tasks/E03-tool-core-and-mcp.md) (T017) | The core abstraction and all conventions on real code |
| **M2** *(done)* | Toolsets from config; one MCP server process, one URL path per toolset | E03 (T026, T018) | Many connectors behind one server, usable from any MCP client. Independent of M3; can run in parallel |
| **M3** | `llm` + Anthropic adapter + harness + `pkg/flow` | [E04](../tasks/E04-llm-harness-flow.md) (T020-T025) | An agent loop and a multi-step flow run, with checkpoints and resume. **Current focus** |
| **M4** | `store` (SQLite), `pkg/runner`, compaction, flow shape check | [E05](../tasks/E05-store-runner.md) (T019, T027-T032) | Runs and conversations persist, resume and compact |
| **M5** | `pkg/component`, `agentkit serve` (done), agent API, `fakellm`, image, Compose, smoke tests, full CI | E01 (T003-T008), [E09](../tasks/E09-agent-api.md) (T036, T037) | Mix-and-match deployment; agents callable over HTTP |
| **M6** | Components on the wire (`pkg/rpc`): remote toolsets and runners, an image per component | [T102](../tasks/T102-component-images.md) | The `split` topology ([COMPONENTS.md](COMPONENTS.md)) |

Skills ([E02](../tasks/E02-agent-skills.md)) grow alongside: the skill layout and review checklist come with M0/M1; each other skill is written right after the first piece of real code it describes.

---

## 1. Module design

### 1.1 One Go module, many packages

- A single `go.mod` at the root. What keeps packages independent is **package boundaries**, not separate modules.
- What a single module does **not** cost:
  - Binary size: the linker only includes packages that are actually imported. A binary that never imports `credentials/vault` contains no Vault code.
  - Build time: only imported packages are compiled.
- What it **does** cost, and how we handle it:
  - Everyone who imports any agentkit package gets our whole `go.mod` in their dependency graph. Go picks the highest required version of each shared dependency, so a heavy backend (AWS SDK, Kubernetes client) can raise the minimum version of something the user also depends on (gRPC, OpenTelemetry, protobuf).
  - Mitigations: prefer the standard library and thin clients (for example, call the Kubernetes Secrets REST API with `net/http` instead of pulling in `client-go`); keep each heavy backend in its own subpackage so it can be moved out cleanly.
  - If one backend still becomes a problem, give that subpackage its own nested `go.mod` (for example `pkg/credentials/aws/go.mod`). The import path stays the same, so this does not break users.
  - All packages share one version number. That is fine before 1.0.

### 1.2 Standard package shape

Every package under `pkg/` follows the same shape:

```
pkg/store/
  doc.go            package docs + the one-paragraph "what and why"
  store.go          the interface(s), shared types, sentinel errors
  options.go        functional options (if any)
  example_test.go   runnable Example: documentation that is also a test
  sqlite/           an implementation (its own deps stay here)
  postgres/         another implementation
  storetest/        in-memory fake + conformance suite (see 2.4)
```

- **Interfaces in the parent, implementations in subpackages.** Importing `pkg/credentials` gives you the interface and types; importing `pkg/credentials/vault` pulls in Vault. Heavy dependencies never leak upward.
- **`xxxtest` package for every interface** (like `net/http/httptest`, `testing/fstest`): a fake for consumers to use and a conformance suite for implementers to run.
- **`internal/`** for helpers shared across packages that are not public API.

### 1.3 Design rules

| Rule | Why |
|---|---|
| Accept interfaces, return concrete types. Keep interfaces small (1-4 methods). | Easy to fake, easy to implement. |
| `New(required..., opts ...Option) (*T, error)`; validate in the constructor. | Invalid objects never exist; no `Init()` step. |
| No `init()`, no package-level mutable state, no global registries. Registries are values, built once and read-only afterwards. | Parallel tests, multi-tenant safety, no locks. |
| Optional backends behind build tags are listed in one table per seam, one small file per tag with a `_none.go` twin, not registered by `init()` in the backend: store backends in `pkg/store/storeopen`, buses in `pkg/bus/busopen`, model providers in `cmd/agentkit/models.go`. A spec or config naming a backend that was not compiled in fails at startup with a clear error naming the tag. A program adds its own backends as values (`storeopen.WithBackends`). | Keeps the no-globals rule with build tags. |
| `ctx context.Context` is the first parameter of anything that does IO or blocks. | Cancellation, deadlines, tenant scope. |
| Exported sentinel errors / typed errors, wrapped with `%w`. Callers use `errors.Is` / `errors.As`. | Tests and callers branch on error identity, never message text. |
| Inject time and randomness (`clock.Clock`, `io.Reader` for rand) where behavior depends on them. | Token refresh, rate limits, budgets and retries become deterministic to test. |
| Whoever starts a goroutine owns stopping it. Long-lived types have `Close()` or `Shutdown(ctx)`. | No leaks, clean shutdown. |
| Server-capable packages expose `http.Handler` / gRPC service registration, never `ListenAndServe`. Only `cmd/` owns listeners, flags, and env. | Libraries stay embeddable; tests use `httptest`. |
| Every public package has at least one runnable `Example`. | Docs that cannot rot. |

### 1.4 Dependency direction

```
cmd/*  ──wires──▶  adapters (llm/anthropic, store/postgres, credentials/vault, mcp/client ...)
                          │
                          ▼
                core interfaces (harness, llm, tool, store, credentials, tenancy)
                          │
                          ▼
           foundations (transport, observability, config, clock, internal/*)
```

- Arrows only point down. `harness` depends on the `llm.Model` interface, never on `llm/anthropic`.
- Enforced by an **architecture test** (`internal/archtest`), which runs with the unit tests and reads the import graph from `go list`:
  1. Nothing in `pkg/` or `internal/` imports `cmd/`.
  2. A package never imports its own subpackages (the parent is the interface; children are implementations).
  3. Non-test code never imports test helpers (`*test` packages, `internal/testkit`).
  4. **Third-party imports are allowlisted per package.** Adding a dependency to a package means adding one line to the table in `archtest`, which makes every new dependency a visible, reviewed decision.
  5. Mocking frameworks are banned everywhere, tests included.

### 1.5 Components: mix-and-match deployment

Goal: any set of packages can run in one container, or be spread across containers that talk to each other, **without code changes**.

**One binary, components chosen by config.** Each deployable piece (`harness`, `tools`, `mcp-server`, ...) is built by an ordinary typed constructor and then handed to the runtime in `pkg/component`, which only needs this:

```go
type Component interface {
    Name() string
    Handler() http.Handler            // mounted under the shared server (pkg/rpc, MCP over HTTP, ...)
    Ready(ctx context.Context) error  // feeds /readyz
    Shutdown(ctx context.Context) error
}
```

The wiring is **explicit Go code in `cmd/agentkit`**, not a generic dependency bag. For each dependency there is one `switch` on the config:

```go
var tools tool.Registry
switch cfg.Harness.Tools.Mode {
case "local":
    tools = localRegistry               // built in this process
case "remote":
    tools = toolclient.New(cfg.Harness.Tools.URL)
}
h, err := harness.New(model, tools)
```

Wrong wiring is a compile error, not a runtime type assertion.

The harness takes no store. The component that runs it feeds it a transcript loaded from the store and writes each turn event back, so the loop has no storage dependency and any backend works.

**Every dependency between components is an interface, and config picks local or remote:**

```yaml
# one container
components: [harness, tools, mcp-server]
harness:
  tools: { mode: local }

# two containers, same image
# container A
components: [harness]
harness:
  tools: { mode: remote, url: http://tools:8081 }
# container B
components: [tools, mcp-server]
mcp:
  toolsets:                       # one path each: /mcp/salesforce, /mcp/gmail
    - { name: salesforce, type: salesforce }
    - { name: gmail, type: gmail }
```

- `local`: the harness gets the in-process `tool.Registry`. No network, no serialization.
- `remote`: the harness gets a client (`pkg/tool/toolrpc`) that implements the same `tool.Registry` interface.
- Each remote client is tested with the **same conformance suite** as the local implementation (§2.4), so swapping one for the other is safe by construction.

**Communication between containers:**

| Path | Protocol | Why |
|---|---|---|
| Component to component (ours) | `pkg/rpc`: typed methods, JSON over HTTP, streamed as newline-delimited frames | One declaration per method serves and calls it; the JSON forms of `llm` and `harness` types cross as they are; no code generator, no dependency |
| To and from external agents and tools | MCP (stdio, Streamable HTTP) | Spec-compliant interop |
| Browser / curl | The agent API: HTTP + JSON with SSE ([E09](../tasks/E09-agent-api.md)) | No tooling needed |

ConnectRPC was the first plan here. T102 chose `pkg/rpc` instead: the interfaces that cross (`tool.Registry`, `runner.Agent`) already have JSON forms, so protobuf definitions and generated code would only restate them. Adding a method is a few lines; adding an interface is a package next to it with a `Register` and a `Client` ([COMPONENTS.md](COMPONENTS.md#adding-a-method-or-a-service)).

- Plain HTTP between containers on the cluster network, with a shared bearer token (`AGENTKIT_RPC_TOKEN`); mTLS when `pkg/credentials` lands.
- Tenant and request scope are not carried yet: the runner and toolsets act for the agent API, which scopes everything it stores. They travel in request headers when a remote component needs them.
- Addresses come from config (compose service names locally, Kubernetes Services in a cluster). No service-discovery system in v1.

**Images:**

- The default image is `agentkit` (everything in the core build) plus `fakellm` for dev and tests: it runs any shape by config. Per-component images (`agentkit-api`, `-runner`, `-toolsets`) are the same binary built with tags that leave out what the component does not run ([COMPONENTS.md](COMPONENTS.md#images)).
- Heavy optional backends sit behind build tags (for example `-tags aws,vault`) so the default image stays small. `make image TAGS=...` builds a variant.
- Size budget: measure the first real image (M5), then set the limit in CI. The pure-Go SQLite driver alone is several MB, so the earlier 10-20 MB target is a goal, not a promise. Every size change and its reason is logged in [SIZES.md](SIZES.md) (rule in [AGENTS.md](../AGENTS.md#sizes)); per-component images are T102.
- If you need a truly custom bundle, write a 20-line `main` that imports the components you want. They are ordinary packages.

**Swapping an implementation.** Every seam (store, bus, model, turn loop, run orchestration, tools, transport) is an interface with a conformance suite and one place that picks the implementation; [COMPONENTS.md](COMPONENTS.md#plugging-in-another-implementation) lists them and what a new one takes. The agent API's run orchestration is `agentapi.Runs` (default: `*runner.Runner`, in process under leases; `agentapi.WithRuns` plugs in another, such as a queue of workers), and where a turn loop runs is `runner.Agent` (local harness, remote runner, or a pool, E20).

---

## 2. Testing

### 2.1 Principle: test behavior through the public API

A test should fail only when a **feature** breaks, not when code is refactored.

- Tests live in `package foo_test` (black box) by default. White-box `package foo` tests are the exception and need a reason (e.g. a tricky internal algorithm).
- Assert on **observable outcomes**: return values, stored state, events emitted, bytes on the wire. Never assert that an internal method was called.
- **No mocking frameworks.** Use hand-written fakes from `xxxtest` packages. Fakes must pass the same conformance suite as real implementations, so they cannot drift from reality.

### 2.2 Test tiers

| Tier | What | Where | Run with | Needs | Budget |
|---|---|---|---|---|---|
| **Unit** | Pure logic, fakes, `httptest` servers | next to code | `make test` | nothing | whole repo < 30 s |
| **Integration** | Real backends: Postgres, Vault, LocalStack, real MCP servers | next to code, `//go:build integration` | `make test-integration` | `make integration STORE=postgres` starts Postgres and Redis as processes (no Docker; cloud sessions) | < 3 min |
| **E2E / smoke** | Real binaries as processes or in containers, black-box over HTTP/MCP | `test/e2e`, `//go:build e2e` | `make smoke`; `make compose-smoke`, `make k8s-smoke` against what is running ([§3](#3-local-environments-pods)) | Go and Node; Docker or Docker Desktop's Kubernetes for the others | < 2 min per topology |
| **Replay E2E** | The real binary as separate processes, against a local server replaying recorded provider responses | next to the `cmd`, no tag (skips under `-short`) | `make test` | nothing | seconds |
| **Live** | Real model providers (Anthropic, OpenAI-compatible) | next to adapter, `//go:build live` | `make test-live` | API keys | manual (Claude sessions; on-demand workflow) |
| **Eval** | Real Claude using the search connectors against faked upstreams: tool choice, arguments, calls, citations, recovery ([evals/README.md](../evals/README.md)) | `evals/`, `//go:build eval` | `make eval` | `ANTHROPIC_API_KEY` | manual; pass rates over N runs, a drop is shown, not gated |
| **Perf** | Benchmarks (rerunnable) and load tests (recorded) | `Benchmark*` next to code; load-test results in the task's Notes | `go test -bench` | nothing | not in CI until a budget is set |

- `make test-timing` runs the unit tier and prints its slowest packages and top-level tests (`scripts/test-timing.sh` over `go test -json`); CI prints the top 10 after `make check`. Look there before a package nears go test's timeout, and fix waste (a query parsed on every call, a schema migrated for every subtest) before cutting a check.
- Integration tests read connection strings from env (`AGENTKIT_PG_URL`, `AGENTKIT_VAULT_ADDR`, ...) and **skip** if unset, so a laptop without Docker still runs them cleanly. In CI `AGENTKIT_REQUIRE_INTEGRATION=1` turns those skips into failures, so nothing silently stops running.
- CI ([check.yml](../.github/workflows/check.yml), T008) runs these tiers as parallel jobs that call the same make targets and scripts as a laptop: `check` (`make check`, then `make vulncheck`), `integration` (Postgres and Redis service containers, `AGENTKIT_REQUIRE_INTEGRATION=1`), `image` (every image within its budget, then the per-image smoke), `smoke` (`make smoke`) and `web`. A pull request that changes only `docs/` and `tasks/` skips `image` and `smoke` (`scripts/ci-changes.sh`, whose `--test` in `make lint` also checks that wiring). The live tier runs in Claude sessions (no Anthropic key in GitHub, T034); [live.yml](../.github/workflows/live.yml) runs it on demand only: `make test-live LIVE_REQUIRED=1` with the repository secret `ANTHROPIC_API_KEY`, and fails, saying why, when the secret is missing.
- We do not use testcontainers; `scripts/integration.sh` starts the dependencies as processes, and it keeps the dependency list small.

- **Replay E2E** proves the pieces fit through the real entry point (flags, files, signals, several processes) without a key: see `cmd/agentkit/e2e_test.go`. Re-record from the real API with an env switch; the binary only ever gets a dummy key, and fixtures keep no account headers.
- **Evals** (scoring model quality on a task set) are a separate, later tier; nothing in the unit, replay or live tiers asserts on what a model says beyond what the test scripted.

### 2.3 How many tests: one per behavior, not per function

Each task file lists **acceptance criteria**. Each criterion maps to (roughly) one test. That is the test budget.

Write a test for:
- each acceptance criterion (the happy path of the feature);
- each error a caller is expected to handle (`ErrNotFound`, `ErrBudgetExceeded`, ...);
- boundaries that have bitten us or obviously will (empty input, cancellation mid-stream, concurrent access);
- every bug fix (regression test first, then the fix).

Do **not** write tests for:
- getters, setters, constructors with no logic, option plumbing;
- generated code (protobuf);
- wiring in `cmd/` that the e2e smoke suite already covers;
- the same behavior again at a higher tier. If a unit test proves it, the e2e test does not re-prove it.

Coverage is a signal we look at, not a target we chase.

### 2.4 Conformance suites (the main anti-duplication tool)

When an interface has several implementations (Store: SQLite, Postgres, in-memory fake; credential backends: file, Vault, AWS, k8s; MCP transports: stdio, HTTP), we write the behavior tests **once**:

```go
// pkg/store/storetest/suite.go
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
    t.Run("CreateAndGetRun", ...)
    t.Run("GetMissingRunReturnsErrNotFound", ...)
    t.Run("TenantIsolation", ...)
}

// pkg/store/sqlite/sqlite_test.go
func TestConformance(t *testing.T) { storetest.Run(t, newTestSQLite) }
```

New implementation = one line of test code + passing the suite.

### 2.5 Rules that keep tests from being flaky or brittle

- **No `time.Sleep`.** Use `clocktest.Fake`, channels, or poll-with-deadline helpers.
- **No assertions on log lines or error message strings.** Use `errors.Is` / `errors.As`.
- **Table-driven** when the same behavior varies by input; separate tests when behavior differs.
- **Golden files** only for wire formats (MCP JSON-RPC, tool protocol JSON, provider request bodies). Stored in `testdata/`, regenerated with `make golden`, reviewed in the diff.
- **Recorded fixtures for LLM providers**: an `httptest` server replays `testdata/*.jsonl` responses. Unit tests never touch the network.
- **Fuzz** anything that takes untrusted input (tool input, JSON-RPC decoding, JWT parsing). The seed corpus runs with the unit tests (it is fast and deterministic, like any test); coverage-guided fuzzing is separate and local only: `make fuzz` runs each target for `FUZZTIME` (default 10s). CI does not run it (it took about 11 minutes, twice the rest of CI); run it before merging a change to code a fuzz target covers.
- Every test calls `t.Parallel()` unless it has a documented reason not to.
- Always run with `-race -shuffle=on`. CI runs `-count=1`.

### 2.6 Invariants and simulation: for code with many paths

One test per criterion proves each criterion once, in the one scenario it uses. It says nothing about the paths no criterion mentions (a failure while another call is waiting, a cancel between two turns, a consumer that writes to what it is given), and that is where review finds bugs. For code with many interacting paths (the agent loop first of all):

- **Write the invariants down and check them after every run**, in the shared test helper, so every test checks all of them, not only the one it is about. For the harness: the model is only ever sent valid conversations, each extending the last and never changed after sending; the transcript is valid and survives save and load; events are well-formed; the record adds up.
- **Add a seeded simulation** that generates scripts (including inputs no sane caller sends) and runs them through the same helper, plus a fuzz target over the seed. A failure names its seed and reruns exactly.
- **Compare runs** where a property is a comparison: a consumer that scribbles over everything it is handed must not change the outcome.
- **When review finds a bug, ask which invariant it broke** and add that to the checks, instead of one test for the one case. Planting the bug again should fail a general check.
- **Concurrency tests are driven from the consumer's side** (what it does when it sees an event happens at a fixed point of the run), never by hoping a goroutine is scheduled first. A wait for something that must not happen is allowed only if it can make a broken implementation pass, never fail a correct one.

### 2.7 Performance tests

- **Measure before setting a budget.** A load test runs the real thing (real server, real transport) at a few concurrency levels and records throughput, latency percentiles, errors, allocations and leaks (goroutines, heap) in the task's Notes, with the machine it ran on. Profile before optimizing; say where the cost is (T018: 93% of allocations in the SDK, not our layer).
- **Keep a rerunnable `Benchmark*`** for the hot path (`b.ReportAllocs()`, `b.RunParallel` where it is concurrent), so a later change can be compared with `benchstat`. The throwaway load generator need not be kept.
- **The nightly perf report** (`.github/workflows/nightly.yml`, T034) runs every `Benchmark*` six times, one package at a time (`scripts/perf-report.sh`; `make perf` locally, `BASELINE=old/bench.txt` to compare), and puts benchstat's comparison with the previous successful night's `perf` artifact in the job summary. It is a report: a slower number does not fail it, only a benchmark that cannot run. A new `Benchmark*` joins it by existing. Shared runners are noisy, so read a change as real only when benchstat marks it (a p-value, not `~`) on more than one night.
- **No timing assertions in unit tests.** A test asserts behavior under load (no errors, limits enforced, nothing leaks, the 503 when over capacity), never "faster than X ms". A CI perf budget comes only after numbers exist, with headroom, and lives outside `make test`.
- Findings become features when they protect the service (T018 added a call timeout, an in-flight cap and a session timeout); each gets a behavior test and a mutation check like any other.

### 2.8 Connectors

Connectors call paid APIs, so their tests follow one recipe, in [CONNECTORS.md](CONNECTORS.md):

- **One suite.** Every connector passes `toolsettest.RunConnector` with one call. The suite checks what all connectors share: settings, key handling, error classes, retries, body limits, leaks, MCP and concurrency. Its checks have IDs; a connector may skip an opt-out check only with a written reason.
- **No network.** Responses are recorded from the real API (`make record-cassettes`), scrubbed and replayed. The suite hands out only a client that refuses every other host.
- **Checked against the provider's spec.** Where the provider publishes an OpenAPI spec, the connector vendors the part it uses (`make openapi-specs`), and check `cassettes.openapi` validates every cassette, recorded or synthetic, against it. A published spec the real API contradicts is corrected by a reviewed overlay, never by hand.
- **Honest fakes.** Every other test uses `toolsettest.NewFake` or `FakeFactory`, built from the real definitions and checking inputs and outputs against the real schemas.
- **Generic bugs become checks.** A bug that any connector could have becomes a suite check, with a planted bug in the toy connector that proves the check catches it.

---

## 3. Local environments ("pods")

### 3.1 Approach

- **One deployment, several ways to run it.** The split deployment of [COMPONENTS.md](COMPONENTS.md) (the agent API with the console, a runner, the toolsets) runs on a laptop as processes, in Docker Compose, or on Docker Desktop's Kubernetes. Every way uses the same config file (`deploy/k8s/base/config.json`), the same environment and, where there are containers, the same images (`make images-split`), so a change seen working one way works the others.
- **Compose and Kubernetes are parallel**: `deploy/compose/compose.yaml` mirrors the overlay `deploy/k8s/local` service for service (Postgres 17, a one-shot `migrate`, then the API with `AGENTKIT_STORE_MIGRATE=off`), with the same limits and security settings. Pick Compose for a quick containerized run with no cluster, Kubernetes for manifests, probes and anything that needs a real cluster. Neither script creates a cluster: Kubernetes is Docker Desktop's own (T006 dropped kind and k3d).
- One `Dockerfile` producing distroless images: `agentkit` (everything), the variants `agentkit-local` and `agentkit-dev`, and one image per component (`agentkit-api`, `-runner`, `-toolsets`), each the same binary built with tags that leave parts out ([COMPONENTS.md](COMPONENTS.md#images)). Topologies differ by config and environment, not by code.
- **The model**: the smoke run as processes uses the console's fake model (`web/apps/console/e2e/fake-model.ts`), so it needs no key and is deterministic. The containerized ways call the real Anthropic API with `ANTHROPIC_API_KEY`, and runs fail with `model_auth` without it. `fakellm` (T004), a scripted model server image, is planned for containerized runs with no key.
- **Dependencies for integration tests** run as processes, not containers: `make integration STORE=postgres` starts a throwaway Postgres and Redis (`scripts/integration.sh`; `--deps` keeps them up), which also works in cloud sessions, which have no Docker.

### 3.2 Topologies

| Topology | Where | What runs | Use it for |
|---|---|---|---|
| One process | `scripts/try-agent.sh` (`--docker` for the `agentkit` image) | `agent,web,mcp-server` in one process, memory store, real model | A real agent run, tools, MCP at `/mcp/<toolset>` |
| Split, processes | `make smoke` (`scripts/smoke.sh`) | API, runner, toolsets built with each image's tags, the fake model; memory store, or Postgres with `AGENTKIT_STORE` | Default check; CI runs it. No Docker, no key |
| Split, Compose | `make compose-up` (`scripts/compose-local.sh`), console on http://localhost:30081 | The split images, `migrate`, Postgres 17 ([deploy/compose](../deploy/compose/README.md)) | The images and their wiring, Postgres, on any Docker |
| All in one, Compose | `scripts/compose-local.sh up allinone`, console on http://localhost:30082 | The `agentkit-local` image alone on SQLite | The one-machine image |
| Split, Kubernetes | `make k8s-up` (`scripts/k8s-local.sh`), console on http://localhost:30080 | The split images, the API's migrate init container, Postgres 17 as a StatefulSet ([deploy/k8s](../deploy/k8s/README.md)) | Manifests, probes, services, the cluster's secret |

Fixed host ports, so scripts and tests never guess: 30080 (Kubernetes), 30081 and 30082 (Compose, bound to 127.0.0.1), 18480 to 18483 (`make smoke`'s API, model, toolsets and runner; `PORT` moves them).

### 3.3 The scripts

`scripts/compose-local.sh` and `scripts/k8s-local.sh` take the same commands:

```
up            build the images, start, block until every service is ready; print the console's URL and token
status        containers or pods, health, URLs
token         the API token the console asks for
logs [svc]    follow a service's logs (api with its migrate step, runner, toolsets, postgres)
smoke         run the e2e suite (test/e2e) against what is running
down          compose: down keeps the data, down -v (make compose-down) deletes it; k8s: delete the namespace
```

- `up` waits on the components' own readiness (`/readyz`, through `agentkit healthcheck -ready` inside the distroless containers and from the host), so when it returns, the stack is usable. `SKIP_BUILD=1` reuses the images already built.
- The secrets (random RPC and API tokens, the Postgres password, the model key) are made on the first `up` and kept after that, since Postgres keeps the password it started with: in the `agentkit` Secret on Kubernetes, in `deploy/compose/.env` (ignored by git) for Compose. Nothing prints a secret but the API token.
- `Makefile` holds build, lint and test targets (`compose-*`, `k8s-*`, `smoke`, `integration`) and delegates environment work to the scripts. The `local-env` skill picks a way for a change and reads the logs when it fails.
- `make vulncheck` runs govulncheck (pinned in the Makefile) on the code, its modules and the stdlib of go.mod's `toolchain`. It needs the network; CI's `check` job and the nightly workflow run it.

### 3.4 Layout

```
Dockerfile                one file, every image (BIN, TAGS)
Makefile
scripts/
  smoke.sh                the split deployment as processes, then the suite
  compose-local.sh        Docker Compose: up, status, token, logs, smoke, down
  k8s-local.sh            Docker Desktop's Kubernetes: the same commands
  try-agent.sh            one process (or the image) with a real model
  integration.sh          Postgres and Redis as processes, then the integration tier
deploy/
  k8s/
    base/                 the split deployment; config.json, shared by every way
    local/                overlay for Docker Desktop: Postgres, limits, node port 30080
  compose/
    compose.yaml          the same deployment in Compose; profile allinone
    README.md             ports, services, configs, secrets and mounts
  local/agent/            configs for try-agent.sh
  example/                the agentkit image with the demo toolset
cmd/
  agentkit/               the binary: parse config, start components
test/
  e2e/                    black-box smoke tests (//go:build e2e)
internal/
  testkit/                golden files, env gating, polling helpers
```

---

## 4. Agent skills

We keep coding-agent skills in the repo and improve them like code. They turn this document into steps an agent (or a person) can follow.

### 4.1 Where they live

- **`.agents/skills/<name>/SKILL.md`** is the single source. This is the open Agent Skills format (`name` + `description` front matter, Markdown body, optional `scripts/` and `references/`), read by Codex and other agents.
- **`.claude/skills`** is a symlink to `.agents/skills`, so Claude Code loads the same files.
- **`AGENTS.md`** at the root is the short repo guide every agent reads (commands, layout, links to these docs). **`CLAUDE.md`** just imports it (`@AGENTS.md`), so there is one copy.

### 4.2 The set

| Skill | Use it to | Built on |
|---|---|---|
| `plan` | Turn an idea into an epic and task files with testable acceptance criteria | §5, `tasks/README.md` |
| `design-module` | Shape a new package: interfaces, subpackages, `xxxtest`, component wiring | §1 |
| `write-tests` | Pick the tier, write one test per criterion, add or reuse a conformance suite | §2 |
| `write-connector` | Write a connector or a provider family, wire its auth, test it with the kit, run it locally and over MCP | §2.8, [CONNECTORS.md](CONNECTORS.md) |
| `implement-task` | Take a task from `todo` to `review`: branch, code, tests, `make check`, update the task file | §1, §2, §5 |
| `review` | Review a diff against our rules: boundaries, brittle tests, test budget, errors, ctx, goroutines | §1, §2 |
| `local-env` | Bring up a topology, run smoke tests, read logs | §3 |

### 4.3 Rules for skills

- **Point to the docs; don't copy them.** A skill says *what to do, in what order*, and links the section of this file that says *why*. Rules live in one place.
- **Short.** A skill body should fit on a couple of screens. Long reference material goes in `references/` and is loaded only when needed.
- **Agent-neutral.** No instructions that only one agent can follow. Where a skill can use another agent, it is optional and degrades gracefully. For example, `review` can ask for a second opinion from a different agent CLI if one is installed (`codex exec`, `claude -p`), read-only, and merges the findings. If none is installed, it skips that step.
- **Linted.** `make lint` checks the front matter, that `name` matches the directory, that every link resolves, and that `.claude/skills` points at `.agents/skills`.
- **Improve in the same PR.** When a review finds the same mistake twice, or a skill sent an agent the wrong way, fix the skill in the PR that fixes the code.
- **Written from real code.** A skill is written right after the first real piece of work it describes, using that work as its example, not ahead of time.

## 5. Workflow: tasks folder

How the planner and parallel sessions coordinate, and the lessons learned so far: [WORKFLOW.md](WORKFLOW.md).


- `tasks/` holds one Markdown file per task, plus epic files that reference tasks. Conventions and template are in [tasks/README.md](../tasks/README.md).
- A task's **acceptance criteria are its test list** (§2.3). A task is done when each criterion has a passing test (or a stated reason it is covered elsewhere) and `make check` is green.
- `make lint` checks the task files: no duplicate IDs, every `depends_on` exists, and each epic's checklist matches its tasks' `status`. Two branches that take the same ID fail CI; the second one to merge takes the next free number.

## 6. Decisions and open questions

Decided:
- Compose is the default local runtime; kind is opt-in and comes with credentials.
- `pkg/rpc` (net/http and JSON) for our protocol; official MCP Go SDK; `google/jsonschema-go` for schemas.
- Go: `go 1.27.0` minimum (for `encoding/json/v2`), `toolchain go1.27.2`. Builds keep Go 1.27's default `jsonv2` experiment on: `GOEXPERIMENT=nojsonv2` does not build `pkg/llm`.
- Our own harness and `pkg/flow` rather than an agent framework; native model adapters; ADK, Genkit and Eino are integrations through MCP ([PLAN.md decisions](PLAN.md#decisions)).
- Golden files are rewritten with `make golden` (env `AGENTKIT_UPDATE_GOLDEN=1`), not a `-update` flag, so it works across `./...`.
- Work is committed straight to `main` after `make check`; CI runs it again on every push.

Open:
- Image size budget: set after measuring the first real image (T005).
- Perf budgets: after two weeks of nightly benchstat numbers (T034); until then the perf report does not gate.
- Evals (§2): on hold. To decide when started: what to score, the task set, how to grade, and the cost per run.
- `fakellm` is Anthropic-only (replaying recorded responses); an OpenAI-compatible mode comes with E08 if needed.
- Which build tags gate which backends (decided per backend as each one lands).
